package codex

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

var hookFixtureObservedAt = time.Date(2026, 8, 14, 10, 20, 30, 400000000, time.FixedZone("CEST", 2*60*60))

func TestParseHookVersionedFixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		fixture             string
		wantType            domain.CapabilityType
		wantName            string
		wantSourceTimestamp bool
		wantSourceIdentity  bool
	}{
		{fixture: "valid_builtin.json", wantType: domain.CapabilityTool, wantName: "Bash", wantSourceIdentity: true},
		{fixture: "valid_mcp.json", wantType: domain.CapabilityMCPTool, wantName: "mcp__filesystem__read_file", wantSourceIdentity: true},
		{fixture: "valid_agent.json", wantType: domain.CapabilityAgent, wantName: "spawn_agent", wantSourceIdentity: true},
		{fixture: "skill_not_proven.json", wantType: domain.CapabilityTool, wantName: "Skill", wantSourceIdentity: true},
		{fixture: "missing_optional.json", wantType: domain.CapabilityTool, wantName: "update_plan"},
		{fixture: "extra_fields.json", wantType: domain.CapabilityTool, wantName: "update_plan", wantSourceIdentity: true},
		{fixture: "with_source_timestamp.json", wantType: domain.CapabilityTool, wantName: "apply_patch", wantSourceTimestamp: true, wantSourceIdentity: true},
		{fixture: "no_source_timestamp.json", wantType: domain.CapabilityTool, wantName: "Read", wantSourceIdentity: true},
	}
	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			payload := readHookFixture(t, test.fixture)
			event, err := ParseHookStdin(bytes.NewReader(payload), hookFixtureObservedAt)
			if err != nil {
				t.Fatalf("ParseHookStdin() error = %v", err)
			}
			if err := event.Validate(); err != nil {
				t.Fatalf("normalized event is invalid: %v", err)
			}
			if event.ObservedAt.IsZero() || !event.ObservedAt.Equal(hookFixtureObservedAt.UTC()) {
				t.Fatalf("ObservedAt = %s, want injected UTC time %s", event.ObservedAt, hookFixtureObservedAt.UTC())
			}
			if test.wantSourceTimestamp && event.SourceTimestamp != nil && event.SourceTimestamp.Equal(event.ObservedAt) {
				t.Fatalf("SourceTimestamp replaced/inherited ObservedAt: %s", event.SourceTimestamp)
			}
			if event.Runtime != domain.RuntimeCodex || event.EventType != domain.EventInvoked || event.Provenance != domain.ProvenanceHook {
				t.Fatalf("contract fields = %#v, want Codex invoked direct-hook event", event)
			}
			if event.SchemaVersion != domain.CurrentUsageEventSchemaVersion || event.InvocationOrigin != domain.InvocationOriginUnknown {
				t.Fatalf("schema/origin = %d/%q, want current/unknown", event.SchemaVersion, event.InvocationOrigin)
			}
			if event.CapabilityType != test.wantType || event.CapabilityName != test.wantName {
				t.Fatalf("capability = %s/%q, want %s/%q", event.CapabilityType, event.CapabilityName, test.wantType, test.wantName)
			}
			if (event.SourceTimestamp != nil) != test.wantSourceTimestamp {
				t.Fatalf("SourceTimestamp present = %t, want %t", event.SourceTimestamp != nil, test.wantSourceTimestamp)
			}
			if (event.SourceIdentity != "") != test.wantSourceIdentity {
				t.Fatalf("SourceIdentity present = %t, want %t", event.SourceIdentity != "", test.wantSourceIdentity)
			}
			assertSHA256Hex(t, event.SessionID)
			assertSHA256Hex(t, event.ProjectID)
			assertSHA256Hex(t, event.Fingerprint)
		})
	}
}

func TestParseHookPrivacyIgnoresPromptInputAndResponse(t *testing.T) {
	payload := readHookFixture(t, "privacy_sentinel.json")
	event, err := ParseHookPayload(payload, hookFixtureObservedAt)
	if err != nil {
		t.Fatalf("ParseHookPayload() error = %v", err)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal(normalized event): %v", err)
	}
	for _, sentinel := range []string{
		"SENTINEL_PROMPT_NEVER_METADATA",
		"SENTINEL_TOOL_INPUT_NEVER_METADATA",
		"SENTINEL_TOOL_RESPONSE_NEVER_METADATA",
	} {
		if strings.Contains(string(encoded), sentinel) {
			t.Fatalf("normalized metadata leaked %q: %s", sentinel, encoded)
		}
	}
	for _, rawIdentifier := range []string{"codex-session-private", "/fixture/project"} {
		if strings.Contains(string(encoded), rawIdentifier) {
			t.Fatalf("normalized metadata leaked raw identifier %q: %s", rawIdentifier, encoded)
		}
	}

	mutated := payload
	for _, replacement := range []struct{ old, new string }{
		{old: "SENTINEL_PROMPT_NEVER_METADATA", new: "DIFFERENT_PROMPT"},
		{old: "SENTINEL_TOOL_INPUT_NEVER_METADATA", new: "DIFFERENT_INPUT"},
		{old: "SENTINEL_TOOL_RESPONSE_NEVER_METADATA", new: "DIFFERENT_RESPONSE"},
	} {
		mutated = bytes.ReplaceAll(mutated, []byte(replacement.old), []byte(replacement.new))
	}
	mutatedEvent, err := ParseHookPayload(mutated, hookFixtureObservedAt)
	if err != nil {
		t.Fatalf("ParseHookPayload(mutated) error = %v", err)
	}
	if !reflect.DeepEqual(event, mutatedEvent) {
		t.Fatalf("tool payload/prompt changed normalized event:\noriginal=%#v\nmutated=%#v", event, mutatedEvent)
	}
}

func TestParseHookRejectsMalformedEmptyAndUnknownWithoutLeakingPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantErr string
	}{
		{name: "malformed", payload: readHookFixture(t, "malformed.json"), wantErr: "malformed JSON"},
		{name: "empty", payload: readHookFixture(t, "empty.json"), wantErr: "input is empty"},
		{name: "unknown event", payload: readHookFixture(t, "unknown_event.json"), wantErr: "only PostToolUse"},
		{name: "unknown event with prompt", payload: []byte(`{"hook_event_name":"UserPromptSubmit","prompt":"SENTINEL_ERROR_PAYLOAD"}`), wantErr: "only PostToolUse"},
		{name: "malformed with payload", payload: []byte(`{"hook_event_name":"PostToolUse","tool_input":"SENTINEL_ERROR_PAYLOAD"`), wantErr: "malformed JSON"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseHookStdin(bytes.NewReader(test.payload), hookFixtureObservedAt)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
			if strings.Contains(err.Error(), "SENTINEL_ERROR_PAYLOAD") {
				t.Fatalf("error leaked payload sentinel: %v", err)
			}
		})
	}
}

func TestParseHookDeduplicatesStableIDsAndPreservesDistinctCalls(t *testing.T) {
	duplicateA := parseHookFixture(t, "duplicate_a.json")
	duplicateB := parseHookFixture(t, "duplicate_b.json")
	distinct := parseHookFixture(t, "distinct_id.json")
	if duplicateA.Fingerprint != duplicateB.Fingerprint {
		t.Fatalf("duplicate delivery fingerprints differ: %q != %q", duplicateA.Fingerprint, duplicateB.Fingerprint)
	}
	if duplicateA.Fingerprint == distinct.Fingerprint {
		t.Fatalf("distinct tool_use_id collapsed into duplicate fingerprint: %q", duplicateA.Fingerprint)
	}

	base := []byte(`{"hook_event_name":"PostToolUse","session_id":"same-session","cwd":"/same/project","turn_id":"turn-a","tool_name":"Bash","tool_use_id":"same-id"}`)
	otherTurn := bytes.Replace(base, []byte(`"turn-a"`), []byte(`"turn-b"`), 1)
	otherSession := bytes.Replace(base, []byte(`"same-session"`), []byte(`"other-session"`), 1)
	first := parseHookPayload(t, base, hookFixtureObservedAt)
	if first.Fingerprint == parseHookPayload(t, otherTurn, hookFixtureObservedAt).Fingerprint {
		t.Fatal("same tool_use_id in another turn collapsed into the first call")
	}
	if first.Fingerprint == parseHookPayload(t, otherSession, hookFixtureObservedAt).Fingerprint {
		t.Fatal("same tool_use_id in another session collapsed into the first call")
	}

	noID := []byte(`{"hook_event_name":"PostToolUse","session_id":"same-session","cwd":"/same/project","turn_id":"turn-a","tool_name":"Bash"}`)
	if parseHookPayload(t, noID, hookFixtureObservedAt).Fingerprint == parseHookPayload(t, noID, hookFixtureObservedAt.Add(time.Nanosecond)).Fingerprint {
		t.Fatal("stable fallback collapsed unrelated timestamp-less calls")
	}
}

func TestParseHookRequiresInjectedObservedAt(t *testing.T) {
	payload := readHookFixture(t, "valid_builtin.json")
	if _, err := ParseHookPayload(payload, time.Time{}); err == nil || !strings.Contains(err.Error(), "observed-at") {
		t.Fatalf("zero observed-at error = %v, want actionable required-time error", err)
	}
}

func TestAdapterParseHookUsesInjectedClock(t *testing.T) {
	want := time.Date(2026, 8, 14, 11, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	adapter := New(Options{Now: func() time.Time { return want }})
	event, err := adapter.ParseHookStdin(bytes.NewReader(readHookFixture(t, "valid_builtin.json")))
	if err != nil {
		t.Fatalf("Adapter.ParseHookStdin() error = %v", err)
	}
	if !event.ObservedAt.Equal(want.UTC()) {
		t.Fatalf("ObservedAt = %s, want adapter clock %s", event.ObservedAt, want.UTC())
	}
	if _, err := adapter.ParseHookPayload(readHookFixture(t, "valid_builtin.json")); err != nil {
		t.Fatalf("Adapter.ParseHookPayload() error = %v", err)
	}
}

func TestParseHookAgentAliasIsDocumentedIdentity(t *testing.T) {
	payload := []byte(`{"hook_event_name":"PostToolUse","session_id":"s","cwd":"/p","tool_name":"Agent","tool_use_id":"id"}`)
	event, err := ParseHookPayload(payload, hookFixtureObservedAt)
	if err != nil {
		t.Fatalf("ParseHookPayload() error = %v", err)
	}
	if event.CapabilityType != domain.CapabilityAgent || event.CapabilityName != "spawn_agent" {
		t.Fatalf("Agent alias normalized as %#v, want documented spawn_agent agent", event)
	}
}

func readHookFixture(t *testing.T, name string) []byte {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	path := filepath.Join(filepath.Dir(sourceFile), "../../../testdata/codex/hooks/v1", name)
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	return payload
}

func parseHookFixture(t *testing.T, name string) domain.UsageEvent {
	t.Helper()
	return parseHookPayload(t, readHookFixture(t, name), hookFixtureObservedAt)
}

func parseHookPayload(t *testing.T, payload []byte, observedAt time.Time) domain.UsageEvent {
	t.Helper()
	event, err := ParseHookPayload(payload, observedAt)
	if err != nil {
		t.Fatalf("ParseHookPayload() error = %v", err)
	}
	return event
}

func assertSHA256Hex(t *testing.T, value string) {
	t.Helper()
	if len(value) != hex.EncodedLen(32) {
		t.Fatalf("hash %q has length %d, want 64", value, len(value))
	}
	if _, err := hex.DecodeString(value); err != nil {
		t.Fatalf("hash %q is not SHA-256 hex: %v", value, err)
	}
}
