package npm_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const (
	rootName    = "harness-lint"
	description = "Local context hygiene and usage analyzer for coding agent harnesses."
	repository  = "https://github.com/kespineira/harness-lint"
	version     = "__VERSION__"
)

type metadata struct {
	VersionPlaceholder string `json:"versionPlaceholder"`
	Templates          struct {
		Root   string `json:"root"`
		Native string `json:"native"`
	} `json:"templates"`
	Assets struct {
		README  string `json:"readme"`
		License string `json:"license"`
	} `json:"assets"`
	Root struct {
		Name string `json:"name"`
	} `json:"root"`
	Native []struct {
		Name    string `json:"name"`
		OS      string `json:"os"`
		CPU     string `json:"cpu"`
		Archive string `json:"archive"`
	} `json:"native"`
}

type manifest struct {
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	Description      string            `json:"description"`
	License          string            `json:"license"`
	Repository       repositoryInfo    `json:"repository"`
	Homepage         string            `json:"homepage"`
	Bugs             bugsInfo          `json:"bugs"`
	Keywords         []string          `json:"keywords"`
	Engines          map[string]string `json:"engines"`
	Bin              map[string]string `json:"bin"`
	Files            []string          `json:"files"`
	PublishConfig    publishConfig     `json:"publishConfig"`
	OptionalDeps     map[string]string `json:"optionalDependencies"`
	OS               []string          `json:"os"`
	CPU              []string          `json:"cpu"`
	Scripts          map[string]string `json:"scripts"`
	Dependencies     map[string]string `json:"dependencies"`
	DevDependencies  map[string]string `json:"devDependencies"`
	PeerDependencies map[string]string `json:"peerDependencies"`
}

type repositoryInfo struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type bugsInfo struct {
	URL string `json:"url"`
}

type publishConfig struct {
	Access string `json:"access"`
}

func npmRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate test source")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "npm")
}

func readJSON(t *testing.T, path string, value any) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, value); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return raw
}

func readManifest(t *testing.T, path string, replacements map[string]string) (manifest, []byte) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for token, value := range replacements {
		raw = []byte(strings.ReplaceAll(string(raw), token, value))
	}
	var got manifest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parse rendered %s: %v", path, err)
	}
	return got, raw
}

func assertRepositoryMetadata(t *testing.T, got manifest) {
	t.Helper()
	if got.Description != description {
		t.Errorf("description = %q, want %q", got.Description, description)
	}
	if got.License != "Apache-2.0" {
		t.Errorf("license = %q, want Apache-2.0", got.License)
	}
	if got.Repository != (repositoryInfo{Type: "git", URL: repository}) {
		t.Errorf("repository = %#v", got.Repository)
	}
	if got.Homepage != repository+"#readme" {
		t.Errorf("homepage = %q", got.Homepage)
	}
	if got.Bugs != (bugsInfo{URL: repository + "/issues"}) {
		t.Errorf("bugs = %#v", got.Bugs)
	}
}

func assertNoInstallHooks(t *testing.T, raw []byte, got manifest) {
	t.Helper()
	if got.Scripts != nil {
		t.Errorf("manifest contains lifecycle scripts: %#v", got.Scripts)
	}
	if got.Dependencies != nil || got.DevDependencies != nil || got.PeerDependencies != nil {
		t.Error("manifest declares dependencies outside the root optionalDependencies contract")
	}
	text := string(raw)
	for _, forbidden := range []string{"preinstall", "install", "postinstall", "prepare", "prepublish", "postpublish", "curl", "download", "fetch"} {
		if strings.Contains(text, "\""+forbidden+"\"") {
			t.Errorf("manifest contains forbidden hook/network field %q", forbidden)
		}
	}
}

func assertAllowlist(t *testing.T, got manifest, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got.Files, want) {
		t.Errorf("files = %#v, want %#v", got.Files, want)
	}
	for _, file := range got.Files {
		if strings.ContainsAny(file, "*?") || strings.HasPrefix(file, "../") {
			t.Errorf("unsafe/non-deterministic files entry %q", file)
		}
	}
}

func TestPackageMetadataContract(t *testing.T) {
	root := npmRoot(t)
	var meta metadata
	readJSON(t, filepath.Join(root, "metadata.json"), &meta)

	if meta.VersionPlaceholder != version {
		t.Fatalf("version placeholder = %q, want %q", meta.VersionPlaceholder, version)
	}
	if meta.Root.Name != rootName {
		t.Fatalf("root name = %q, want %q", meta.Root.Name, rootName)
	}
	if got, want := len(meta.Native), 4; got != want {
		t.Fatalf("native package count = %d, want %d", got, want)
	}
	wantNative := []struct{ name, os, cpu, archive string }{
		{"@kespineira/harness-lint-darwin-arm64", "darwin", "arm64", "harness-lint_${VERSION}_darwin_arm64.tar.gz"},
		{"@kespineira/harness-lint-darwin-x64", "darwin", "x64", "harness-lint_${VERSION}_darwin_amd64.tar.gz"},
		{"@kespineira/harness-lint-linux-arm64", "linux", "arm64", "harness-lint_${VERSION}_linux_arm64.tar.gz"},
		{"@kespineira/harness-lint-linux-x64", "linux", "x64", "harness-lint_${VERSION}_linux_amd64.tar.gz"},
	}
	for i, want := range wantNative {
		got := meta.Native[i]
		if got.Name != want.name || got.OS != want.os || got.CPU != want.cpu || got.Archive != want.archive {
			t.Errorf("native[%d] = %#v, want name=%q os=%q cpu=%q archive=%q", i, got, want.name, want.os, want.cpu, want.archive)
		}
	}
}

func TestRootManifestTemplate(t *testing.T) {
	root := npmRoot(t)
	path := filepath.Join(root, "templates", "root", "package.json.tmpl")
	got, raw := readManifest(t, path, nil)
	if got.Name != rootName || got.Version != version {
		t.Fatalf("root identity = %q@%q, want %q@%q", got.Name, got.Version, rootName, version)
	}
	assertRepositoryMetadata(t, got)
	if !reflect.DeepEqual(got.Keywords, []string{"claude-code", "codex", "coding-agents", "mcp", "skills", "developer-tools", "cli", "context"}) {
		t.Errorf("keywords = %#v", got.Keywords)
	}
	if got.Engines["node"] != ">=18.0.0" {
		t.Errorf("node engine = %q, want conservative >=18.0.0", got.Engines["node"])
	}
	if !reflect.DeepEqual(got.Bin, map[string]string{"harness-lint": "bin/harness-lint.js"}) {
		t.Errorf("bin = %#v", got.Bin)
	}
	assertAllowlist(t, got, []string{"bin/harness-lint.js", "README.md", "LICENSE"})
	if got.PublishConfig.Access != "public" {
		t.Errorf("publish access = %q, want public", got.PublishConfig.Access)
	}
	wantDeps := map[string]string{
		"@kespineira/harness-lint-darwin-arm64": version,
		"@kespineira/harness-lint-darwin-x64":   version,
		"@kespineira/harness-lint-linux-arm64":  version,
		"@kespineira/harness-lint-linux-x64":    version,
	}
	if !reflect.DeepEqual(got.OptionalDeps, wantDeps) {
		t.Errorf("optionalDependencies = %#v, want %#v", got.OptionalDeps, wantDeps)
	}
	assertNoInstallHooks(t, raw, got)
}

func TestNativeManifestTemplate(t *testing.T) {
	root := npmRoot(t)
	var meta metadata
	readJSON(t, filepath.Join(root, "metadata.json"), &meta)
	path := filepath.Join(root, meta.Templates.Native)
	for _, spec := range meta.Native {
		t.Run(spec.Name, func(t *testing.T) {
			got, raw := readManifest(t, path, map[string]string{
				"__PACKAGE_NAME__": spec.Name,
				"__VERSION__":      meta.VersionPlaceholder,
				"__OS__":           spec.OS,
				"__CPU__":          spec.CPU,
			})
			if got.Name != spec.Name || got.Version != version {
				t.Fatalf("identity = %q@%q, want %q@%q", got.Name, got.Version, spec.Name, version)
			}
			assertRepositoryMetadata(t, got)
			if !reflect.DeepEqual(got.OS, []string{spec.OS}) || !reflect.DeepEqual(got.CPU, []string{spec.CPU}) {
				t.Errorf("selectors = os=%#v cpu=%#v, want %q/%q", got.OS, got.CPU, spec.OS, spec.CPU)
			}
			if !reflect.DeepEqual(got.Keywords, []string{"claude-code", "codex", "coding-agents", "mcp", "skills", "developer-tools", "cli", "context"}) {
				t.Errorf("keywords = %#v", got.Keywords)
			}
			if !reflect.DeepEqual(got.Bin, map[string]string{"harness-lint": "bin/harness-lint"}) {
				t.Errorf("bin = %#v", got.Bin)
			}
			assertAllowlist(t, got, []string{"bin/harness-lint", "README.md", "LICENSE"})
			if got.PublishConfig.Access != "public" {
				t.Errorf("publish access = %q, want public", got.PublishConfig.Access)
			}
			assertNoInstallHooks(t, raw, got)
		})
	}
}

func TestNpmPackagingAssetsAreMinimalAndCanonical(t *testing.T) {
	root := npmRoot(t)
	var meta metadata
	readJSON(t, filepath.Join(root, "metadata.json"), &meta)
	license, err := os.ReadFile(filepath.Join(root, meta.Assets.License))
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := os.ReadFile(filepath.Join(root, "..", "LICENSE"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(license, canonical) {
		t.Error("npm LICENSE differs from the repository Apache-2.0 license")
	}
	readme, err := os.ReadFile(filepath.Join(root, meta.Assets.README))
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes.TrimSpace(readme)) == 0 {
		t.Error("npm README is empty")
	}
}
