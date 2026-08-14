package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	reportdto "github.com/kespineira/harness-lint/internal/report"
)

// TestExecuteMilestone2IsolatedHooksAndIngestE2E covers the machine-facing
// lifecycle in one hermetic tree. Parser tests own individual format details;
// this test verifies that install, status, direct ingest, reports, dry-run,
// and uninstall compose without touching a user's live configuration.
func TestExecuteMilestone2IsolatedHooksAndIngestE2E(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	dbPath := filepath.Join(root, "state", "harness-lint.db")
	claudeConfig := filepath.Join(home, ".claude", "settings.json")
	codexConfig := filepath.Join(home, ".codex", "hooks.json")
	writeMilestone2UserConfig(t, claudeConfig, "user-claude-preserved")
	writeMilestone2UserConfig(t, codexConfig, "user-codex-preserved")

	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	options := Options{
		Home:        home,
		CWD:         root,
		ProjectRoot: root,
		Now:         func() time.Time { return now },
		// The manager still performs its real install/uninstall operations; the
		// lookup is injected only so the test does not depend on a developer's
		// PATH or on a separately installed binary.
		LookPath: func(string) (string, error) { return "/opt/harness-lint", nil },
	}

	execute := func(args []string, payload []byte) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if err := ExecuteWithOptions(options, args, bytes.NewReader(payload), &stdout, &stderr); err != nil {
			t.Fatalf("execute %v: %v\nstdout=%s\nstderr=%s", args, err, stderr.String(), stdout.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("execute %v wrote stderr=%q", args, stderr.String())
		}
		return stdout.String()
	}

	execute([]string{"hooks", "install"}, nil)
	statusOutput := execute([]string{"hooks", "status", "--json"}, nil)
	var status hookStatusJSON
	if err := json.Unmarshal([]byte(statusOutput), &status); err != nil {
		t.Fatalf("decode hook status: %v\noutput=%s", err, statusOutput)
	}
	if status.SchemaVersion != 1 || len(status.Runtimes) != 2 {
		t.Fatalf("hook status envelope = %#v, want schema 1 and both runtimes", status)
	}
	for _, runtimeStatus := range status.Runtimes {
		if runtimeStatus.Status != "installed" {
			t.Fatalf("runtime %s status = %q, want installed", runtimeStatus.Runtime, runtimeStatus.Status)
		}
	}

	claudeArgs := []string{"ingest", "--runtime", "claude", "--event", "PostToolUse", "--db", dbPath}
	execute(claudeArgs, readMilestone2Fixture(t, "claude", "v1-post-tool-use-id-success.json"))
	execute(claudeArgs, readMilestone2Fixture(t, "claude", "v1-post-tool-use-id-success.json"))
	execute(claudeArgs, readMilestone2Fixture(t, "claude", "v1-post-tool-use-id-different.json"))
	execute([]string{"ingest", "--runtime", "claude", "--event", "UserPromptExpansion", "--db", dbPath}, readMilestone2Fixture(t, "claude", "v1-user-prompt-expansion-slash-command-explicit.json"))

	codexArgs := []string{"ingest", "--runtime", "codex", "--event", "PostToolUse", "--db", dbPath}
	execute(codexArgs, readMilestone2Fixture(t, "codex", "duplicate_a.json"))
	execute(codexArgs, readMilestone2Fixture(t, "codex", "duplicate_b.json"))
	execute(codexArgs, readMilestone2Fixture(t, "codex", "distinct_id.json"))
	execute(codexArgs, readMilestone2Fixture(t, "codex", "privacy_sentinel.json"))

	var report reportdto.ReportDocument
	reportOutput := execute([]string{"report", "--json", "--db", dbPath, "--days", "60"}, nil)
	if err := json.Unmarshal([]byte(reportOutput), &report); err != nil {
		t.Fatalf("decode report JSON: %v\noutput=%s", err, reportOutput)
	}
	if report.SchemaVersion != reportdto.SchemaVersion || report.GeneratedAt != now.Format(time.RFC3339Nano) || len(report.UsageOnly) != 3 {
		t.Fatalf("report envelope = %#v, want schema 1, injected time, and three usage-only groups", report)
	}
	wantCounts := map[string]int{
		"claude-code/tool/Bash":              2,
		"claude-code/command/fixture-review": 1,
		"codex/tool/Bash":                    3,
	}
	for _, usage := range report.UsageOnly {
		key := usage.Runtime + "/" + usage.Type + "/" + usage.Name
		if got, ok := wantCounts[key]; !ok || got != usage.InvocationCount {
			t.Fatalf("usage-only %s = %d, want %#v", key, usage.InvocationCount, wantCounts)
		} else {
			delete(wantCounts, key)
		}
	}
	if len(wantCounts) != 0 {
		t.Fatalf("report usage-only groups missing %#v", wantCounts)
	}

	var stale reportdto.StaleDocument
	staleOutput := execute([]string{"stale", "--json", "--db", dbPath, "--days", "60"}, nil)
	if err := json.Unmarshal([]byte(staleOutput), &stale); err != nil {
		t.Fatalf("decode stale JSON: %v\noutput=%s", err, staleOutput)
	}
	if stale.SchemaVersion != reportdto.SchemaVersion || stale.GeneratedAt != now.Format(time.RFC3339Nano) || len(stale.Capabilities) != 0 || len(stale.Findings) != 0 {
		t.Fatalf("stale JSON = %#v, want valid empty installed-inventory view", stale)
	}

	dryRun := execute([]string{"hooks", "uninstall", "--dry-run"}, nil)
	if !strings.Contains(dryRun, "would-change=true") {
		t.Fatalf("uninstall dry-run = %q, want would-change=true", dryRun)
	}
	if !strings.Contains(readMilestone2File(t, claudeConfig), "harness-lint") || !strings.Contains(readMilestone2File(t, codexConfig), "harness-lint") {
		t.Fatal("uninstall dry-run changed or removed a managed configuration")
	}

	execute([]string{"hooks", "uninstall"}, nil)
	if got := readMilestone2File(t, claudeConfig); !strings.Contains(got, "user-claude-preserved") || strings.Contains(got, "--managed-by") {
		t.Fatalf("Claude uninstall did not preserve only unrelated hook structure: %s", got)
	}
	if got := readMilestone2File(t, codexConfig); !strings.Contains(got, "user-codex-preserved") || strings.Contains(got, "--managed-by") {
		t.Fatalf("Codex uninstall did not preserve only unrelated hook structure: %s", got)
	}

	for _, path := range []string{root, reportOutput, staleOutput, statusOutput, dryRun} {
		if strings.Contains(path, "SENTINEL") {
			t.Fatalf("CLI output or test root unexpectedly contains fixture sentinel: %q", path)
		}
	}
	if database, err := os.ReadFile(dbPath); err != nil {
		t.Fatalf("read SQLite database: %v", err)
	} else if strings.Contains(string(database), "SENTINEL") {
		t.Fatal("SQLite database retained fixture prompt/input/response sentinel")
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), "SENTINEL") {
			t.Fatalf("file %s retained fixture sentinel", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func writeMilestone2UserConfig(t *testing.T, path, command string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create user config parent: %v", err)
	}
	content := `{"preferences":{"preserve":true},"hooks":{"PostToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"` + command + `"}]}]}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write user config %s: %v", path, err)
	}
}

func readMilestone2File(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func readMilestone2Fixture(t *testing.T, runtimeName, name string) []byte {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	path := filepath.Join(filepath.Dir(sourceFile), "..", "..", "testdata", runtimeName, "hooks", "v1", name)
	if runtimeName == "claude" {
		path = filepath.Join(filepath.Dir(sourceFile), "..", "..", "testdata", "claude", "hooks", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return data
}
