package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/store"
)

func TestExecuteIngestPersistsMetadataOnlyAndStaysQuiet(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state", "harness-lint.db")
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	payload := `{"hook_event_name":"PostToolUse","session_id":"secret-session","cwd":"/secret/project","turn_id":"turn-a","tool_name":"mcp__server__search","tool_use_id":"delivery-a","tool_input":{"query":"PROMPT_SENTINEL"},"tool_response":{"body":"OUTPUT_SENTINEL"},"prompt":"PROMPT_SENTINEL"}`
	options := Options{Now: func() time.Time { return now }}
	var stdout, stderr bytes.Buffer
	args := []string{"ingest", "--runtime", "codex", "--event", "PostToolUse", "--db", dbPath}
	if err := ExecuteWithOptions(options, args, strings.NewReader(payload), &stdout, &stderr); err != nil {
		t.Fatalf("ingest error = %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("ingest output stdout=%q stderr=%q, want quiet success", stdout.String(), stderr.String())
	}
	if err := ExecuteWithOptions(options, args, strings.NewReader(payload), &stdout, &stderr); err != nil {
		t.Fatalf("duplicate ingest error = %v", err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	events, err := db.ListUsageEvents(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want duplicate delivery collapsed to one", len(events))
	}
	if !events[0].ObservedAt.Equal(now.UTC()) || events[0].SourceTimestamp != nil {
		t.Fatalf("event timestamps = %#v, want observed clock and no source timestamp", events[0])
	}
	for _, secret := range []string{"PROMPT_SENTINEL", "OUTPUT_SENTINEL", "secret-session", "/secret/project"} {
		if strings.Contains(stdout.String()+stderr.String(), secret) {
			t.Fatalf("output contains privacy sentinel %q", secret)
		}
	}
}

func TestExecuteIngestClaudeDispatchAndTrailingInputValidation(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state", "harness-lint.db")
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	options := Options{Now: func() time.Time { return now }}
	valid := `{"hook_event_name":"PostToolUse","session_id":"s","cwd":"/project","tool_name":"Skill","tool_use_id":"skill-call","tool_input":{"skill":"review"},"tool_response":{"text":"OUTPUT_SENTINEL"}}`
	args := []string{"ingest", "--runtime", "claude", "--event", "PostToolUse", "--db", dbPath}
	var stdout, stderr bytes.Buffer
	if err := ExecuteWithOptions(options, args, strings.NewReader(valid), &stdout, &stderr); err != nil {
		t.Fatalf("Claude ingest error = %v", err)
	}
	for _, malformed := range []string{"", valid + valid, `{"hook_event_name":"PostToolUse"}`} {
		stdout.Reset()
		stderr.Reset()
		if err := ExecuteWithOptions(options, args, strings.NewReader(malformed), &stdout, &stderr); err == nil {
			t.Fatalf("Claude malformed payload %q unexpectedly succeeded", malformed)
		}
		if stdout.Len() != 0 || strings.Contains(stdout.String()+stderr.String(), "OUTPUT_SENTINEL") {
			t.Fatalf("Claude malformed output stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	events, err := db.ListUsageEvents(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Runtime != domain.RuntimeClaudeCode || events[0].CapabilityType != domain.CapabilitySkill || events[0].CapabilityName != "review" {
		t.Fatalf("Claude events = %#v, want one normalized skill event", events)
	}
}

func TestExecuteIngestRejectsUnsafeInvocationWithoutReadingPayload(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state", "harness-lint.db")
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown runtime", args: []string{"ingest", "--runtime", "other", "--db", dbPath}, want: "unknown runtime"},
		{name: "unknown event", args: []string{"ingest", "--runtime", "codex", "--event", "SessionStart", "--db", dbPath}, want: "unknown event"},
		{name: "now override", args: []string{"ingest", "--runtime", "codex", "--now", "2026-08-14T12:00:00Z", "--db", dbPath}, want: "does not accept --now"},
		{name: "marker", args: []string{"ingest", "--runtime", "codex", "--managed-by", "wrong", "--db", dbPath}, want: "unsupported --managed-by"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := ExecuteWithOptions(Options{Now: func() time.Time { return now }}, test.args, strings.NewReader(`{"prompt":"PROMPT_SENTINEL"}`), &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if stdout.Len() != 0 || strings.Contains(stdout.String()+stderr.String(), "PROMPT_SENTINEL") {
				t.Fatalf("unsafe invocation output stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("invalid ingest invocation created database: %v", err)
	}
}

func TestExecuteHooksLifecycleAndStableJSON(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	options := Options{
		Home:        filepath.Join(root, "home"),
		CWD:         root,
		ProjectRoot: root,
		Now:         func() time.Time { return now },
		LookPath:    func(string) (string, error) { return "/bin/harness-lint", nil },
	}
	var stdout, stderr bytes.Buffer
	if err := ExecuteWithOptions(options, []string{"hooks", "install", "claude"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("hook install error = %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	configPath := filepath.Join(options.Home, ".claude", "settings.json")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("installed config stat: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if err := ExecuteWithOptions(options, []string{"hooks", "status", "claude", "--json"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("hook status error = %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	var status struct {
		SchemaVersion int    `json:"schema_version"`
		GeneratedAt   string `json:"generated_at"`
		Runtimes      []struct {
			Runtime string `json:"runtime"`
			Status  string `json:"status"`
		} `json:"runtimes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatalf("status JSON = %q: %v", stdout.String(), err)
	}
	if status.SchemaVersion != 1 || status.GeneratedAt != now.Format(time.RFC3339Nano) || len(status.Runtimes) != 1 || status.Runtimes[0].Runtime != "claude-code" || status.Runtimes[0].Status != "installed" {
		t.Fatalf("status DTO = %#v, want stable clock/runtime/status", status)
	}
	stdout.Reset()
	if err := ExecuteWithOptions(options, []string{"hooks", "uninstall", "claude", "--dry-run"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("hook dry-run error = %v", err)
	}
	if !strings.Contains(stdout.String(), "would-change=true") {
		t.Fatalf("dry-run output = %q, want would-change=true", stdout.String())
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("dry-run removed config: %v", err)
	}
}
