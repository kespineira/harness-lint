package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	reportdto "github.com/kespineira/harness-lint/internal/report"
	"github.com/kespineira/harness-lint/internal/store"
	usagedto "github.com/kespineira/harness-lint/internal/usage"
)

const publicJSONObservedAt = "2026-08-14T12:00:00.000Z"

func TestPublicJSONContractsAreStableAndPrivacySafe(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	dbPath := filepath.Join(root, "state", "harness-lint.db")
	t.Setenv("HOME", home)
	now, err := time.Parse(time.RFC3339Nano, publicJSONObservedAt)
	if err != nil {
		t.Fatalf("parse fixed observation time: %v", err)
	}
	options := Options{
		Home:        home,
		CWD:         root,
		ProjectRoot: root,
		Now:         func() time.Time { return now },
		LookPath:    func(string) (string, error) { return "/opt/harness-lint", nil },
	}

	seedPublicJSONDatabase(t, dbPath, root, now)
	assertIngestFixture(t, options, dbPath, publicJSONTestdataPath(t, "codex/hooks/v1/malformed.json"), false)
	assertIngestFixture(t, options, dbPath, publicJSONTestdataPath(t, "codex/hooks/v1/extra_fields.json"), true)
	assertIngestFixture(t, options, dbPath, publicJSONTestdataPath(t, "codex/hooks/v1/privacy_sentinel.json"), true)

	installHooksForPublicJSON(t, options, home)

	outputs := map[string]string{
		"report-v2.json": runStablePublicJSON(t, options, []string{"report", "--json", "--db", dbPath, "--days", "60"}),
		"stale-v2.json":  runStablePublicJSON(t, options, []string{"stale", "--json", "--db", dbPath, "--days", "60"}),
		"usage-v2.json":  runStablePublicJSON(t, options, []string{"usage", "--json", "--db", dbPath, "--days", "60"}),
		"hooks-status-v1.json": runStablePublicJSON(t, options, []string{
			"hooks", "status", "--json", "--claude-config", filepath.Join(home, ".claude"), "--codex-home", filepath.Join(home, ".codex"),
		}),
	}

	for name, output := range outputs {
		assertSchemaVersion(t, name, output)
		assertNoPublicJSONSentinels(t, name, output)
		assertPublicJSONGolden(t, name, root, output)
	}
	assertNoSQLiteSentinels(t, dbPath)
}

func seedPublicJSONDatabase(t *testing.T, dbPath, root string, now time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatalf("create public JSON database parent: %v", err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open public JSON database: %v", err)
	}
	capability := domain.Capability{
		Runtime:        domain.RuntimeCodex,
		Type:           domain.CapabilityTool,
		Name:           "Bash",
		Scope:          domain.ScopeUser,
		Source:         filepath.Join(root, "private", "definitions", "Bash.md"),
		Enabled:        domain.EnabledStateEnabled,
		Advertisement:  domain.AdvertisementStateFullyAdvertised,
		Hash:           "inventory-hash",
		MetadataTokens: domain.Measurement{Value: 20, Confidence: domain.ConfidenceExact, Basis: "advertised metadata"},
		BodyTokens:     domain.Measurement{Value: 30, Confidence: domain.ConfidenceEstimated, Basis: "advertised body"},
		FirstSeen:      now.Add(-24 * time.Hour),
		LastSeen:       now,
	}
	if err := db.RecordInventory(context.Background(), domain.RuntimeCodex, now, []domain.Capability{capability}); err != nil {
		db.Close()
		t.Fatalf("record public JSON inventory: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close public JSON database seed: %v", err)
	}
}

func assertIngestFixture(t *testing.T, options Options, dbPath, fixturePath string, wantSuccess bool) {
	t.Helper()
	payload, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read ingest fixture %s: %v", fixturePath, err)
	}
	var stdout, stderr bytes.Buffer
	err = ExecuteWithOptions(options, []string{"ingest", "--runtime", "codex", "--event", "PostToolUse", "--db", dbPath}, bytes.NewReader(payload), &stdout, &stderr)
	if wantSuccess && err != nil {
		t.Fatalf("ingest additive/privacy fixture %s: %v", fixturePath, err)
	}
	if !wantSuccess && err == nil {
		t.Fatalf("ingest malformed fixture %s unexpectedly succeeded", fixturePath)
	}
	if strings.Contains(stdout.String()+stderr.String(), "SENTINEL_") || (err != nil && strings.Contains(err.Error(), "SENTINEL_")) {
		t.Fatalf("ingest fixture %s leaked a payload sentinel", fixturePath)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("ingest fixture %s wrote output", fixturePath)
	}
}

func installHooksForPublicJSON(t *testing.T, options Options, home string) {
	t.Helper()
	for _, args := range [][]string{
		{"hooks", "install", "--claude-config", filepath.Join(home, ".claude"), "--codex-home", filepath.Join(home, ".codex")},
	} {
		var stdout, stderr bytes.Buffer
		if err := ExecuteWithOptions(options, args, nil, &stdout, &stderr); err != nil {
			t.Fatalf("install hooks for public JSON: %v", err)
		}
		if strings.Contains(stdout.String()+stderr.String(), "SENTINEL_") {
			t.Fatal("hook installation output leaked a privacy sentinel")
		}
	}
}

func runPublicJSON(t *testing.T, options Options, args []string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := ExecuteWithOptions(options, args, nil, &stdout, &stderr); err != nil {
		t.Fatalf("execute %v: %v", args, err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("execute %v wrote stderr: %q", args, stderr.String())
	}
	return stdout.String()
}

func runStablePublicJSON(t *testing.T, options Options, args []string) string {
	t.Helper()
	first := runPublicJSON(t, options, args)
	second := runPublicJSON(t, options, args)
	if first != second {
		t.Fatalf("execute %v is not deterministic:\nfirst=%s\nsecond=%s", args, first, second)
	}
	return first
}

func assertSchemaVersion(t *testing.T, name, output string) {
	t.Helper()
	var envelope struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode %s: %v\noutput=%s", name, err, output)
	}
	want := 1
	if strings.HasPrefix(name, "report-") || strings.HasPrefix(name, "stale-") {
		want = reportdto.SchemaVersion
	} else if strings.HasPrefix(name, "usage-") {
		want = usagedto.SchemaVersion
	}
	if envelope.SchemaVersion != want {
		t.Fatalf("%s schema_version = %d, want %d", name, envelope.SchemaVersion, want)
	}
}

func assertNoPublicJSONSentinels(t *testing.T, name, output string) {
	t.Helper()
	for _, sentinel := range []string{
		"SENTINEL_",
		"codex-session-private",
		"codex-turn-private",
		"codex-tool-use-private",
		"/fixture/project",
	} {
		if strings.Contains(output, sentinel) {
			t.Fatalf("%s contains privacy sentinel %q: %s", name, sentinel, output)
		}
	}
}

func assertNoSQLiteSentinels(t *testing.T, dbPath string) {
	t.Helper()
	paths, err := filepath.Glob(dbPath + "*")
	if err != nil {
		t.Fatalf("glob SQLite files: %v", err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read SQLite file %s: %v", path, err)
		}
		if strings.Contains(string(data), "SENTINEL_") || strings.Contains(string(data), "codex-session-private") || strings.Contains(string(data), "/fixture/project") {
			t.Fatalf("SQLite file %s retained a privacy sentinel", path)
		}
	}
}

func assertPublicJSONGolden(t *testing.T, name, root, output string) {
	t.Helper()
	goldenPath := publicJSONGoldenPath(t, name)
	wantData, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read public JSON golden %s: %v", goldenPath, err)
	}
	var want, got any
	if err := json.Unmarshal(wantData, &want); err != nil {
		t.Fatalf("decode public JSON golden %s: %v", goldenPath, err)
	}
	if err := json.Unmarshal([]byte(strings.ReplaceAll(output, root, "<ROOT>")), &got); err != nil {
		t.Fatalf("decode normalized %s: %v", name, err)
	}
	assertJSONSubset(t, name, want, got, "$")
}

func assertJSONSubset(t *testing.T, name string, want, got any, path string) {
	t.Helper()
	switch expected := want.(type) {
	case map[string]any:
		actual, ok := got.(map[string]any)
		if !ok {
			t.Fatalf("%s %s = %T, want object", name, path, got)
		}
		for key, expectedValue := range expected {
			actualValue, ok := actual[key]
			if !ok {
				t.Fatalf("%s missing %s", name, path+"."+key)
			}
			assertJSONSubset(t, name, expectedValue, actualValue, path+"."+key)
		}
	case []any:
		actual, ok := got.([]any)
		if !ok {
			t.Fatalf("%s %s = %T, want array", name, path, got)
		}
		if len(actual) != len(expected) {
			t.Fatalf("%s %s length = %d, want %d", name, path, len(actual), len(expected))
		}
		for index := range expected {
			assertJSONSubset(t, name, expected[index], actual[index], path+"["+strconv.Itoa(index)+"]")
		}
	default:
		if !reflect.DeepEqual(expected, got) {
			t.Fatalf("%s %s = %#v, want %#v", name, path, got, expected)
		}
	}
}

func publicJSONGoldenPath(t *testing.T, name string) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	return filepath.Join(filepath.Dir(sourceFile), "..", "..", "testdata", "public-json", name)
}

func publicJSONTestdataPath(t *testing.T, name string) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	return filepath.Join(filepath.Dir(sourceFile), "..", "..", "testdata", filepath.FromSlash(name))
}
