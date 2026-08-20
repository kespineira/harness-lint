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

	"github.com/kespineira/harness-lint/internal/capture"
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

func TestExecuteIngestUnknownEventFlagRecordsUnsupportedEvent(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state", "harness-lint.db")
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	var stdout, stderr bytes.Buffer
	err := ExecuteWithOptions(Options{Now: func() time.Time { return now }}, []string{"ingest", "--runtime", "codex", "--event", "SessionStart", "--db", dbPath}, strings.NewReader(`{"prompt":"PROMPT_SENTINEL"}`), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "unsupported event") {
		t.Fatalf("unknown event error = %v, want unsupported event", err)
	}
	if strings.Contains(err.Error(), "PROMPT_SENTINEL") || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("unknown event leaked payload/output: err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	health, err := db.GetCaptureHealth(context.Background(), domain.RuntimeCodex)
	if err != nil {
		t.Fatal(err)
	}
	if health.ConsecutiveFailures != 1 || health.LastFailureKind == nil || *health.LastFailureKind != capture.FailureUnsupportedEvent {
		t.Fatalf("unknown event health = %#v, want one unsupported_event failure", health)
	}
	events, err := db.ListUsageEvents(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("unknown event wrote usage events: %#v", events)
	}
}

func TestExecuteRejectsMeaninglessIngestAndHooksFlags(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state", "harness-lint.db")
	capturePath := filepath.Join(root, "capture.jsonl")
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "ingest home before command", args: []string{"--home", filepath.Join(root, "home"), "ingest", "--runtime", "codex", "--db", dbPath}, want: "--home"},
		{name: "ingest project", args: []string{"ingest", "--runtime", "codex", "--db", dbPath, "--project", root}, want: "--project"},
		{name: "ingest Codex home", args: []string{"ingest", "--runtime", "codex", "--db", dbPath, "--codex-home", root}, want: "--codex-home"},
		{name: "ingest Claude config", args: []string{"ingest", "--runtime", "codex", "--db", dbPath, "--claude-config", root}, want: "--claude-config"},
		{name: "ingest since", args: []string{"ingest", "--runtime", "codex", "--db", dbPath, "--since", "2026-08-14T12:00:00Z"}, want: "--since"},
		{name: "ingest days", args: []string{"ingest", "--runtime", "codex", "--db", dbPath, "--days", "1"}, want: "--days"},
		{name: "ingest hook capture", args: []string{"ingest", "--runtime", "codex", "--db", dbPath, "--hook-capture", capturePath}, want: "--hook-capture"},
		{name: "hooks database", args: []string{"hooks", "status", "--db", dbPath}, want: "--db"},
		{name: "hooks project", args: []string{"hooks", "status", "--project", root}, want: "--project"},
		{name: "hooks config directory", args: []string{"hooks", "status", "--config-dir", root}, want: "--config-dir"},
		{name: "hooks since", args: []string{"hooks", "status", "--since", "2026-08-14T12:00:00Z"}, want: "--since"},
		{name: "hooks days", args: []string{"hooks", "status", "--days", "1"}, want: "--days"},
		{name: "hooks hook capture", args: []string{"hooks", "status", "--hook-capture", capturePath}, want: "--hook-capture"},
		{name: "hooks event", args: []string{"hooks", "status", "--event", "PostToolUse"}, want: "--event"},
		{name: "hooks managed by", args: []string{"hooks", "status", "--managed-by", "harness-lint-hooks/v1"}, want: "--managed-by"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := ExecuteWithOptions(Options{
				Home: filepath.Join(root, "home"),
				CWD:  root,
				Now:  func() time.Time { return time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC) },
			}, test.args, strings.NewReader(`{"prompt":"PROMPT_SENTINEL"}`), &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want rejection mentioning %q", err, test.want)
			}
			if stdout.Len() != 0 || strings.Contains(stdout.String()+stderr.String(), "PROMPT_SENTINEL") {
				t.Fatalf("rejected invocation output stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("rejected ingest invocation created database: %v", err)
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
	if !strings.Contains(stdout.String(), "Changes would be made") {
		t.Fatalf("dry-run output = %q, want a human preview", stdout.String())
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("dry-run removed config: %v", err)
	}
}

func TestExecuteIngestFailuresRecordOneBoundedPrivacySafeHealthObservation(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state", "harness-lint.db")
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	options := Options{Now: func() time.Time { return now }}
	malformed := `{"hook_event_name":"PostToolUse","prompt":"PROMPT_SENTINEL"`
	unsupported := `{"hook_event_name":"UserPromptSubmit","session_id":"s","cwd":"/project","tool_name":"Bash","tool_input":{"query":"ARGS_SENTINEL"},"tool_response":"OUTPUT_SENTINEL"}`
	mismatch := `{"hook_event_name":"UserPromptExpansion","session_id":"s","cwd":"/project","expansion_type":"slash_command","command_name":"review","prompt":"PROMPT_SENTINEL"}`
	cases := []struct {
		name     string
		args     []string
		stdin    string
		wantErr  string
		wantKind capture.FailureKind
	}{
		{name: "malformed JSON", args: []string{"ingest", "--runtime", "codex", "--db", dbPath}, stdin: malformed, wantErr: "malformed payload", wantKind: capture.FailureMalformedPayload},
		{name: "empty stdin", args: []string{"ingest", "--runtime", "codex", "--db", dbPath}, stdin: "", wantErr: "malformed payload", wantKind: capture.FailureMalformedPayload},
		{name: "unsupported event", args: []string{"ingest", "--runtime", "codex", "--db", dbPath}, stdin: unsupported, wantErr: "unsupported event", wantKind: capture.FailureUnsupportedEvent},
		{name: "payload mismatch", args: []string{"ingest", "--runtime", "claude", "--event", "PostToolUse", "--db", dbPath}, stdin: mismatch, wantErr: "unsupported event", wantKind: capture.FailureUnsupportedEvent},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := ExecuteWithOptions(options, test.args, strings.NewReader(test.stdin), &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("ingest error = %v, want %q", err, test.wantErr)
			}
			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("failure output stdout=%q stderr=%q, want quiet Execute", stdout.String(), stderr.String())
			}
			for _, sentinel := range []string{"PROMPT_SENTINEL", "ARGS_SENTINEL", "OUTPUT_SENTINEL"} {
				if strings.Contains(err.Error(), sentinel) {
					t.Fatalf("error retained privacy sentinel %q: %v", sentinel, err)
				}
			}
		})
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	health, err := db.GetCaptureHealth(context.Background(), domain.RuntimeCodex)
	if err != nil {
		t.Fatal(err)
	}
	if health.ConsecutiveFailures != 3 || health.LastFailureKind == nil || *health.LastFailureKind != capture.FailureUnsupportedEvent {
		t.Fatalf("Codex failure health = %#v, want three failures ending in unsupported_event", health)
	}
	claudeHealth, err := db.GetCaptureHealth(context.Background(), domain.RuntimeClaudeCode)
	if err != nil {
		t.Fatal(err)
	}
	if claudeHealth.ConsecutiveFailures != 1 || claudeHealth.LastFailureKind == nil || *claudeHealth.LastFailureKind != capture.FailureUnsupportedEvent {
		t.Fatalf("Claude failure health = %#v, want one payload mismatch failure", claudeHealth)
	}
	events, err := db.ListUsageEvents(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("failed ingest wrote usage events: %#v", events)
	}
}

func TestExecuteIngestSuccessUpdatesDirectHealthButTranscriptImportDoesNot(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		runtime string
		payload string
		domain  domain.Runtime
	}{
		{
			name:    "Claude",
			runtime: "claude",
			domain:  domain.RuntimeClaudeCode,
			payload: `{"hook_event_name":"PostToolUse","session_id":"claude-session","cwd":"/project","tool_name":"Skill","tool_use_id":"claude-tool","tool_input":{"skill":"review"},"tool_response":{"text":"OUTPUT_SENTINEL"}}`,
		},
		{
			name:    "Codex",
			runtime: "codex",
			domain:  domain.RuntimeCodex,
			payload: `{"hook_event_name":"PostToolUse","session_id":"codex-session","cwd":"/project","turn_id":"turn-a","tool_name":"Bash","tool_use_id":"codex-tool","tool_input":{"command":"COMMAND_SENTINEL"},"tool_response":{"body":"OUTPUT_SENTINEL"}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dbPath := filepath.Join(root, "state", "harness-lint.db")
			var stdout, stderr bytes.Buffer
			if err := ExecuteWithOptions(Options{Now: func() time.Time { return now }}, []string{"ingest", "--runtime", test.runtime, "--db", dbPath}, strings.NewReader(test.payload), &stdout, &stderr); err != nil {
				t.Fatalf("successful %s ingest error = %v", test.name, err)
			}
			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("successful ingest output stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			db, err := store.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			before, err := db.GetCaptureHealth(context.Background(), test.domain)
			if err != nil {
				t.Fatal(err)
			}
			if before.LastSuccessfulDelivery == nil || !before.LastSuccessfulDelivery.Equal(now) || before.ConsecutiveFailures != 0 {
				t.Fatalf("direct health = %#v, want successful delivery at receive time", before)
			}
			transcript := domain.UsageEvent{
				ObservedAt:       now.Add(time.Minute),
				Runtime:          test.domain,
				SessionID:        "transcript-session",
				ProjectID:        "transcript-project",
				CapabilityType:   domain.CapabilityTool,
				CapabilityName:   "transcript-tool",
				EventType:        domain.EventInvoked,
				Provenance:       domain.ProvenanceTranscript,
				InvocationOrigin: domain.InvocationOriginUnknown,
				SchemaVersion:    domain.CurrentUsageEventSchemaVersion,
			}
			if err := db.InsertUsageEvents(context.Background(), []domain.UsageEvent{transcript}); err != nil {
				t.Fatalf("transcript import error = %v", err)
			}
			after, err := db.GetCaptureHealth(context.Background(), test.domain)
			if err != nil {
				t.Fatal(err)
			}
			if after.ConsecutiveFailures != before.ConsecutiveFailures || after.LastSuccessfulDelivery == nil || !after.LastSuccessfulDelivery.Equal(*before.LastSuccessfulDelivery) {
				t.Fatalf("transcript import changed direct health: before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestExecuteIngestUnavailableDatabaseReturnsSafeError(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(blocker, "state.db")
	sentinel := `{"hook_event_name":"PostToolUse","prompt":"PROMPT_SENTINEL","tool_input":{"command":"COMMAND_SENTINEL"}`
	var stdout, stderr bytes.Buffer
	err := ExecuteWithOptions(Options{Now: func() time.Time { return time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC) }}, []string{"ingest", "--runtime", "codex", "--db", dbPath}, strings.NewReader(sentinel), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "malformed payload") || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("unavailable database error = %v, want malformed payload and database unavailable", err)
	}
	if strings.Contains(err.Error(), "PROMPT_SENTINEL") || strings.Contains(err.Error(), "COMMAND_SENTINEL") || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("unavailable database leaked data/output: err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}
