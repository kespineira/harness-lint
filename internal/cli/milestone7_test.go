package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/hooks"
	"github.com/kespineira/harness-lint/internal/presentation"
)

func TestM7ContextualGroupAndActionHelp(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want []string
	}{
		{name: "hooks missing action", args: []string{"hooks"}, want: []string{"Commands:", "status", "test", "install", "uninstall"}},
		{name: "hooks explicit help", args: []string{"hooks", "--help"}, want: []string{"Manage runtime hooks", "Usage:"}},
		{name: "hooks action help", args: []string{"hooks", "status", "--help"}, want: []string{"harness-lint hooks status", "--json", "--verbose"}},
		{name: "db missing action", args: []string{"db"}, want: []string{"Commands:", "status", "check", "backup"}},
		{name: "db action help", args: []string{"db", "check", "--help"}, want: []string{"harness-lint db check", "read-only SQLite", "--json"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := ExecuteWithOptions(Options{}, test.args, nil, &stdout, &stderr); err != nil {
				t.Fatalf("help error = %v", err)
			}
			if stderr.Len() != 0 {
				t.Fatalf("help stderr = %q", stderr.String())
			}
			for _, want := range test.want {
				if !strings.Contains(stdout.String(), want) {
					t.Fatalf("help = %q, missing %q", stdout.String(), want)
				}
			}
		})
	}
}

func TestM7ColorPolicyUsesInjectedTerminalAndNOColor(t *testing.T) {
	root := t.TempDir()
	claude := filepath.Join(root, "claude")
	base := Options{
		CWD:        root,
		Home:       filepath.Join(root, "home"),
		LookPath:   func(string) (string, error) { return "/opt/harness-lint", nil },
		IsTerminal: func(io.Writer) bool { return true },
		LookupEnv:  func(string) (string, bool) { return "", false },
	}
	args := []string{"hooks", "status", "claude", "--claude-config", claude, "--color", "auto"}
	run := func(options Options, arguments []string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if err := ExecuteWithOptions(options, arguments, nil, &stdout, &stderr); err != nil {
			t.Fatalf("status error = %v\nstderr=%s", err, stderr.String())
		}
		return stdout.String()
	}
	if output := run(base, args); !strings.Contains(output, "\x1b[") {
		t.Fatalf("terminal auto color output = %q, want ANSI", output)
	}
	noColor := base
	noColor.LookupEnv = func(string) (string, bool) { return "", true }
	if output := run(noColor, args); strings.Contains(output, "\x1b[") {
		t.Fatalf("NO_COLOR output = %q, want plain", output)
	}
	always := noColor
	always.IsTerminal = func(io.Writer) bool { return false }
	if output := run(always, []string{"hooks", "status", "claude", "--claude-config", claude, "--color", "always"}); !strings.Contains(output, "\x1b[") {
		t.Fatalf("explicit always output = %q, want ANSI despite NO_COLOR", output)
	}
}

func TestM7HumanRenderersUseFixedCountsAndTimes(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	renderer := presentation.NewHumanRenderer(presentation.Options{Now: now, HomeDir: "/Users/alice", Width: 80})
	var output bytes.Buffer
	renderScanView(&output, renderer, false, scanView{
		Runtimes: []scanRuntimeView{
			{Runtime: domain.RuntimeClaudeCode, Capabilities: 2, Events: 3, Findings: 0, Inventory: "not recorded"},
			{Runtime: domain.RuntimeCodex, Capabilities: 1, Events: 4, Findings: 1, Inventory: "not recorded"},
		},
		Capabilities: 3,
		Events:       7,
		Findings:     1,
	})
	want := "Scan complete\n\n  Runtime      Capabilities  Events  Findings\n  Claude Code  2             3       0\n  Codex        1             4       1\n\n3 capabilities discovered · 7 observations imported\n\n1 finding needs attention. Run `harness-lint doctor`.\n\nRun `harness-lint report` to review usage and attention items.\n"
	if output.String() != want {
		t.Fatalf("scan view =\n%s\nwant =\n%s", output.String(), want)
	}
	if got := renderer.RelativeTime(now.Add(-2 * time.Hour)); got != "2h ago" {
		t.Fatalf("relative time = %q", got)
	}
}

func TestM7JSONStatusNeverContainsANSI(t *testing.T) {
	root := t.TempDir()
	options := Options{
		CWD:        root,
		Home:       filepath.Join(root, "home"),
		LookPath:   func(string) (string, error) { return "/opt/harness-lint", nil },
		IsTerminal: func(_ io.Writer) bool { return true },
		LookupEnv:  func(string) (string, bool) { return "", false },
	}
	var stdout, stderr bytes.Buffer
	if err := ExecuteWithOptions(options, []string{"hooks", "status", "--claude-config", filepath.Join(root, "claude"), "--json", "--color", "always"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("status JSON error = %v", err)
	}
	if strings.Contains(stdout.String(), "\x1b[") {
		t.Fatalf("status JSON contains ANSI: %q", stdout.String())
	}
	var document hookStatusJSON
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("status JSON decode: %v", err)
	}
}

func TestM7VerboseWarningsAndDatabaseHumanView(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	renderer := presentation.NewHumanRenderer(presentation.Options{Now: now, HomeDir: "/Users/alice", Width: 80})
	var hooksOutput bytes.Buffer
	renderHookStatusView(&hooksOutput, renderer, true, []hooks.StatusReport{{
		Runtime:    hooks.RuntimeClaude,
		Code:       hooks.StatusPartiallyInstalled,
		ConfigPath: "/Users/alice/.claude/settings.json",
		Binary:     hooks.BinaryResolution{Name: hooks.BinaryName, Resolved: false},
		ManagedEntries: []hooks.ManagedEntry{{
			Event:         "PostToolUse",
			State:         hooks.ManagedEntryPartial,
			ExactHandlers: 1,
			Partial:       1,
		}},
		Warnings: []string{"configuration JSON is malformed: private payload should not be echoed"},
	}})
	for _, want := range []string{"Managed hooks", "Warning", "configuration is malformed", "Details", "PostToolUse", "Partial"} {
		if !strings.Contains(hooksOutput.String(), want) {
			t.Fatalf("hooks verbose output = %q, missing %q", hooksOutput.String(), want)
		}
	}

	oldest := now.Add(-2 * time.Hour).Format(time.RFC3339Nano)
	latest := now.Add(-time.Minute).Format(time.RFC3339Nano)
	var statusOutput bytes.Buffer
	renderDatabaseStatusView(&statusOutput, renderer, false, DatabaseStatusDocument{
		Path:             "/Users/alice/.config/harness-lint/harness-lint.db",
		Schema:           DatabaseSchemaDTO{Current: 7, Latest: 7},
		SizeBytes:        m7Int64Pointer(28823552),
		UsageEventCount:  1234,
		OldestObservedAt: &oldest,
		LatestObservedAt: &latest,
	})
	if !strings.Contains(statusOutput.String(), "Size       27.5 MiB") || !strings.Contains(statusOutput.String(), "Events     1,234") || !strings.Contains(statusOutput.String(), "Aug 20 → Aug 20") {
		t.Fatalf("database status human output = %q", statusOutput.String())
	}

	var checkOutput bytes.Buffer
	renderDatabaseCheckView(&checkOutput, renderer, false, DatabaseCheckDocument{
		Healthy:         false,
		QuickCheck:      "issues",
		ForeignKeyCheck: "ok",
		Schema:          "ok",
		Issues:          []DatabaseIssueDTO{{Check: "quick_check"}},
	})
	if !strings.Contains(checkOutput.String(), "✗ SQLite quick check") || !strings.Contains(checkOutput.String(), "Database needs attention.") || !strings.Contains(checkOutput.String(), "SQLite quick check reported an issue.") {
		t.Fatalf("database check human output = %q", checkOutput.String())
	}

}

func m7Int64Pointer(value int64) *int64 { return &value }
