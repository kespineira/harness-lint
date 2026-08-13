package claude

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

func TestParseHookPayloadPostToolUseBuiltin(t *testing.T) {
	observedAt := parseFixtureObservedAt(t)
	event, err := ParseHookPayload(readHookFixture(t, "v1-post-tool-use-builtin.json"), observedAt)
	if err != nil {
		t.Fatalf("ParseHookPayload() error = %v", err)
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("parsed event is invalid: %v", err)
	}
	if event.CapabilityType != domain.CapabilityTool || event.CapabilityName != "Bash" {
		t.Fatalf("capability = %q/%q, want tool/Bash", event.CapabilityType, event.CapabilityName)
	}
	if event.EventType != domain.EventInvoked || event.Provenance != domain.ProvenanceHook {
		t.Fatalf("event semantics = %q/%q, want invoked/hook", event.EventType, event.Provenance)
	}
	if event.InvocationOrigin != domain.InvocationOriginModelSelected {
		t.Fatalf("invocation origin = %q, want model_selected", event.InvocationOrigin)
	}
	if !event.ObservedAt.Equal(observedAt) || event.SourceTimestamp != nil {
		t.Fatalf("timestamps = observed %s/source %v, want observed fixture time and nil source", event.ObservedAt, event.SourceTimestamp)
	}
	if len(event.SessionID) != 64 || len(event.ProjectID) != 64 {
		t.Fatalf("identifiers are not hashed: %#v", event)
	}
	assertSafeHookEvent(t, event, "SENTINEL_TOOL_INPUT", "SENTINEL_TOOL_RESPONSE")
}

func TestParseHookPayloadDirectCapabilitiesAndOrigins(t *testing.T) {
	// These fixtures use the current Claude hooks reference fields only:
	// common session_id/cwd/hook_event_name, PostToolUse tool_name/
	// tool_input/tool_use_id/tool_response, and UserPromptExpansion
	// expansion_type/command_name/command_args/command_source/prompt.
	tests := []struct {
		fixture    string
		wantType   domain.CapabilityType
		wantName   string
		wantOrigin domain.InvocationOrigin
		sentinels  []string
	}{
		{
			fixture:    "v1-post-tool-use-builtin.json",
			wantType:   domain.CapabilityTool,
			wantName:   "Bash",
			wantOrigin: domain.InvocationOriginModelSelected,
			sentinels:  []string{"SENTINEL_TOOL_INPUT", "SENTINEL_TOOL_RESPONSE"},
		},
		{
			fixture:    "v1-post-tool-use-mcp.json",
			wantType:   domain.CapabilityMCPTool,
			wantName:   "mcp__fixture__search",
			wantOrigin: domain.InvocationOriginModelSelected,
			sentinels:  []string{"SENTINEL_MCP_INPUT", "SENTINEL_MCP_RESPONSE"},
		},
		{
			fixture:    "v1-post-tool-use-skill-model-selected.json",
			wantType:   domain.CapabilitySkill,
			wantName:   "fixture-review",
			wantOrigin: domain.InvocationOriginModelSelected,
			sentinels:  []string{"SENTINEL_SKILL_PROMPT", "SENTINEL_SKILL_RESPONSE"},
		},
		{
			fixture:    "v1-post-tool-use-agent.json",
			wantType:   domain.CapabilityAgent,
			wantName:   "fixture-explore",
			wantOrigin: domain.InvocationOriginModelSelected,
			sentinels:  []string{"SENTINEL_AGENT_PROMPT", "SENTINEL_AGENT_RESPONSE"},
		},
		{
			fixture:    "v1-user-prompt-expansion-skill-explicit.json",
			wantType:   domain.CapabilitySkill,
			wantName:   "fixture-review",
			wantOrigin: domain.InvocationOriginUserExplicit,
			sentinels:  []string{"SENTINEL_COMMAND_ARGS", "SENTINEL_EXPLICIT_PROMPT"},
		},
	}

	observedAt := parseFixtureObservedAt(t)
	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			event, err := ParseHookPayload(readHookFixture(t, test.fixture), observedAt)
			if err != nil {
				t.Fatalf("ParseHookPayload() error = %v", err)
			}
			if event.CapabilityType != test.wantType || event.CapabilityName != test.wantName {
				t.Fatalf("capability = %q/%q, want %q/%q", event.CapabilityType, event.CapabilityName, test.wantType, test.wantName)
			}
			if event.InvocationOrigin != test.wantOrigin {
				t.Fatalf("invocation origin = %q, want %q", event.InvocationOrigin, test.wantOrigin)
			}
			if event.Provenance != domain.ProvenanceHook || event.EventType != domain.EventInvoked {
				t.Fatalf("event semantics = %q/%q, want hook/invoked", event.Provenance, event.EventType)
			}
			assertSafeHookEvent(t, event, test.sentinels...)
		})
	}
}

func TestParseHookPayloadFailureAndRetryDeduplication(t *testing.T) {
	observedAt := parseFixtureObservedAt(t)
	success, err := ParseHookPayload(readHookFixture(t, "v1-post-tool-use-id-success.json"), observedAt)
	if err != nil {
		t.Fatalf("success parse error = %v", err)
	}
	failure, err := ParseHookPayload(readHookFixture(t, "v1-post-tool-use-id-failure.json"), observedAt)
	if err != nil {
		t.Fatalf("failure parse error = %v", err)
	}
	different, err := ParseHookPayload(readHookFixture(t, "v1-post-tool-use-id-different.json"), observedAt)
	if err != nil {
		t.Fatalf("different-ID parse error = %v", err)
	}
	if success.Fingerprint != failure.Fingerprint || success.SourceIdentity != failure.SourceIdentity {
		t.Fatalf("same tool_use_id did not deduplicate: success=%#v failure=%#v", success, failure)
	}
	if success.SourceIdentity == "" || len(success.SourceIdentity) != 64 {
		t.Fatalf("tool-use source identity = %q, want hashed stable identity", success.SourceIdentity)
	}
	if success.Fingerprint == different.Fingerprint {
		t.Fatalf("different tool_use_id calls collapsed: success=%q different=%q", success.Fingerprint, different.Fingerprint)
	}
	assertSafeHookEvent(t, success, "SENTINEL_DEDUP_COMMAND", "SENTINEL_DEDUP_RESPONSE")
	assertSafeHookEvent(t, failure, "SENTINEL_DEDUP_COMMAND", "SENTINEL_DEDUP_ERROR")
}

func TestParseHookPayloadOptionalTimestampAndUnknownFields(t *testing.T) {
	observedAt := parseFixtureObservedAt(t)
	missing, err := ParseHookPayload(readHookFixture(t, "v1-missing-optional-fields.json"), observedAt)
	if err != nil {
		t.Fatalf("missing optional fields parse error = %v", err)
	}
	if missing.SourceTimestamp != nil {
		t.Fatalf("missing source timestamp = %v, want nil", missing.SourceTimestamp)
	}
	if !missing.ObservedAt.Equal(observedAt) {
		t.Fatalf("missing-source ObservedAt = %s, want %s", missing.ObservedAt, observedAt)
	}

	withTimestamp, err := ParseHookPayload(readHookFixture(t, "v1-source-timestamp.json"), observedAt)
	if err != nil {
		t.Fatalf("source timestamp parse error = %v", err)
	}
	if withTimestamp.SourceTimestamp == nil || !withTimestamp.SourceTimestamp.Equal(parseFixtureSourceTimestamp(t)) {
		t.Fatalf("source timestamp = %v, want fixture timestamp", withTimestamp.SourceTimestamp)
	}
	if !withTimestamp.ObservedAt.Equal(observedAt) {
		t.Fatalf("ObservedAt = %s, want injected %s", withTimestamp.ObservedAt, observedAt)
	}

	extra, err := ParseHookInput(strings.NewReader(string(readHookFixture(t, "v1-unknown-extra-fields.json"))), observedAt)
	if err != nil {
		t.Fatalf("unknown extra fields parse error = %v", err)
	}
	assertSafeHookEvent(t, extra, "SENTINEL_EXTRA_PROMPT", "SENTINEL_EXTRA_INPUT", "SENTINEL_EXTRA_RESPONSE")
}

func TestParseHookPayloadRejectsMalformedEmptyWrongUnknownAndSubagentEvents(t *testing.T) {
	observedAt := parseFixtureObservedAt(t)
	tests := []struct {
		name     string
		payload  []byte
		wantIs   error
		sentinel string
	}{
		{name: "empty", payload: readHookFixture(t, "v1-empty.json"), wantIs: ErrMalformedHookPayload},
		{name: "malformed", payload: readHookFixture(t, "v1-malformed.json"), wantIs: ErrMalformedHookPayload, sentinel: "SENTINEL_MALFORMED"},
		{name: "unknown event", payload: readHookFixture(t, "v1-unknown-event.json"), wantIs: ErrUnsupportedHookEvent, sentinel: "SENTINEL_UNKNOWN_INPUT"},
		{name: "subagent start", payload: readHookFixture(t, "v1-subagent-start.json"), wantIs: ErrUnsupportedHookEvent},
		{name: "subagent stop", payload: readHookFixture(t, "v1-subagent-stop.json"), wantIs: ErrUnsupportedHookEvent, sentinel: "SENTINEL_SUBAGENT_MESSAGE"},
		{name: "wrong event", payload: readHookFixture(t, "v1-wrong-event.json"), wantIs: ErrUnsupportedHookEvent},
		{name: "MCP prompt expansion", payload: []byte(`{"hook_event_name":"UserPromptExpansion","session_id":"fixture","cwd":"/fixture","expansion_type":"mcp_prompt","command_name":"fixture"}`), wantIs: ErrUnsupportedHookEvent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseHookPayload(test.payload, observedAt)
			if err == nil {
				t.Fatal("ParseHookPayload() error = nil, want safe rejection")
			}
			if !errors.Is(err, test.wantIs) {
				t.Fatalf("ParseHookPayload() error = %v, want errors.Is(..., %v)", err, test.wantIs)
			}
			if test.sentinel != "" && strings.Contains(err.Error(), test.sentinel) {
				t.Fatalf("error echoed raw payload sentinel %q: %v", test.sentinel, err)
			}
		})
	}
}

func TestParseHookPayloadRequiresObservedAt(t *testing.T) {
	_, err := ParseHookPayload(readHookFixture(t, "v1-source-timestamp.json"), time.Time{})
	if err == nil || !strings.Contains(err.Error(), "observed-at") {
		t.Fatalf("zero observed time error = %v, want required observed-at error", err)
	}
}

func assertSafeHookEvent(t *testing.T, event domain.UsageEvent, sentinels ...string) {
	t.Helper()
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal(event): %v", err)
	}
	for _, sentinel := range sentinels {
		if strings.Contains(string(encoded), sentinel) {
			t.Fatalf("normalized event retained %q: %s", sentinel, encoded)
		}
	}
}

const fixtureObservedAt = "2026-08-14T10:00:00.123Z"

func readHookFixture(t *testing.T, name string) []byte {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	path := filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "testdata", "claude", "hooks", name)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return contents
}

func parseFixtureObservedAt(t *testing.T) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, fixtureObservedAt)
	if err != nil {
		t.Fatalf("parse fixture observed time: %v", err)
	}
	return parsed
}

func parseFixtureSourceTimestamp(t *testing.T) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, "2026-08-14T09:59:59.987654Z")
	if err != nil {
		t.Fatalf("parse fixture source time: %v", err)
	}
	return parsed
}
