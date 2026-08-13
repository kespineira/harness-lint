package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

const (
	// Codex currently sends one JSON object to each command hook. Keep the
	// parser bounded because it is intended for a CLI stdin boundary, not a
	// transcript or arbitrary file reader.
	maxHookPayloadBytes = 8 << 20
	postToolUseEvent    = "PostToolUse"
)

// codexPostToolUsePayload is intentionally a narrow projection of the
// documented Codex PostToolUse payload. tool_input and tool_response are not
// decoded at all: their presence proves that a call occurred, but their
// contents are not usage metadata and must not cross this boundary.
type codexPostToolUsePayload struct {
	SessionID     string `json:"session_id"`
	CWD           string `json:"cwd"`
	TurnID        string `json:"turn_id"`
	ToolName      string `json:"tool_name"`
	ToolUseID     string `json:"tool_use_id"`
	HookEventName string `json:"hook_event_name"`
	// Codex does not require a source timestamp in the documented hook
	// contract. If a producer supplies this optional field, retain it only as
	// a valid SourceTimestamp; otherwise the normalized event keeps it nil.
	SourceTime json.RawMessage `json:"timestamp"`
}

// ParseHookStdin parses one Codex command-hook payload from stdin and returns
// one metadata-only usage observation. observedAt is supplied by the caller
// at local receive time; it is required even when the payload has no optional
// source timestamp.
//
// Codex PostToolUse is the only hook event normalized as an invocation. Other
// lifecycle events either describe session/turn state or user prompt content,
// and accepting them here would double-count or leak non-usage data. The
// transcript importer remains the fallback for historical session records.
func ParseHookStdin(input io.Reader, observedAt time.Time) (domain.UsageEvent, error) {
	if input == nil {
		return domain.UsageEvent{}, errors.New("parse Codex hook stdin: input reader is nil")
	}
	limited := io.LimitReader(input, maxHookPayloadBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		// Reader errors can be supplied by a caller and may contain arbitrary
		// input text; keep diagnostics independent of payload contents.
		return domain.UsageEvent{}, errors.New("read Codex hook stdin: input could not be read")
	}
	if len(payload) > maxHookPayloadBytes {
		return domain.UsageEvent{}, errors.New("parse Codex hook stdin: payload exceeds 8 MiB")
	}
	return ParseHookPayload(payload, observedAt)
}

// ParseHookPayload parses one documented Codex PostToolUse JSON payload. It is
// useful for callers that already read stdin and for deterministic fixture
// tests; use ParseHookStdin at a CLI boundary.
func ParseHookPayload(payload []byte, observedAt time.Time) (domain.UsageEvent, error) {
	if observedAt.IsZero() {
		return domain.UsageEvent{}, errors.New("parse Codex hook payload: observed-at time is required")
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return domain.UsageEvent{}, errors.New("parse Codex hook payload: input is empty")
	}
	if len(payload) > maxHookPayloadBytes {
		return domain.UsageEvent{}, errors.New("parse Codex hook payload: payload exceeds 8 MiB")
	}

	var decoded codexPostToolUsePayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		// Do not wrap decoder text: type errors may include untrusted field
		// values. The actionable part is the stable boundary name, not payload.
		return domain.UsageEvent{}, errors.New("parse Codex hook payload: malformed JSON")
	}
	if strings.TrimSpace(decoded.HookEventName) == "" {
		return domain.UsageEvent{}, errors.New("parse Codex hook payload: hook_event_name is required")
	}
	if decoded.HookEventName != postToolUseEvent {
		return domain.UsageEvent{}, errors.New("parse Codex hook payload: only PostToolUse is a usage event; other hook events are ignored")
	}
	session := strings.TrimSpace(decoded.SessionID)
	if session == "" {
		return domain.UsageEvent{}, errors.New("parse Codex hook payload: session_id is required")
	}
	project := strings.TrimSpace(decoded.CWD)
	if project == "" {
		return domain.UsageEvent{}, errors.New("parse Codex hook payload: cwd is required")
	}
	toolName := strings.TrimSpace(decoded.ToolName)
	if toolName == "" {
		return domain.UsageEvent{}, errors.New("parse Codex hook payload: tool_name is required")
	}

	sourceTimestamp, err := hookSourceTimestamp(decoded.SourceTime)
	if err != nil {
		return domain.UsageEvent{}, err
	}
	capabilityType, capabilityName := classifyHookTool(toolName)
	event := domain.UsageEvent{
		ObservedAt:       observedAt.UTC(),
		SourceTimestamp:  sourceTimestamp,
		Runtime:          domain.RuntimeCodex,
		SessionID:        hashIdentifier(session),
		ProjectID:        hashIdentifier(project),
		CapabilityType:   capabilityType,
		CapabilityName:   capabilityName,
		EventType:        domain.EventInvoked,
		Provenance:       domain.ProvenanceHook,
		InvocationOrigin: domain.InvocationOriginUnknown,
		SchemaVersion:    domain.CurrentUsageEventSchemaVersion,
		SourceIdentity:   hookSourceIdentity(session, decoded.TurnID, decoded.ToolUseID),
	}
	fingerprint, err := domain.FingerprintForUsageEvent(event)
	if err != nil {
		return domain.UsageEvent{}, errors.New("parse Codex hook payload: normalized event is invalid")
	}
	event.Fingerprint = fingerprint
	return event, nil
}

// ParseHookEvent is a concise alias for ParseHookPayload for callers that use
// "event" for the already-read stdin value.
func ParseHookEvent(payload []byte, observedAt time.Time) (domain.UsageEvent, error) {
	return ParseHookPayload(payload, observedAt)
}

// ParseHookStdin parses direct hook input using the adapter's injected clock.
// It deliberately does not read any filesystem path or inspect the adapter's
// transcript roots; ImportUsage remains the historical fallback path.
func (a *Adapter) ParseHookStdin(input io.Reader) (domain.UsageEvent, error) {
	if a == nil {
		return domain.UsageEvent{}, errors.New("parse Codex hook stdin: adapter is nil")
	}
	if a.options.now == nil {
		return domain.UsageEvent{}, errors.New("parse Codex hook stdin: adapter clock is not configured")
	}
	return ParseHookStdin(input, a.options.now())
}

// ParseHookPayload parses an already-read direct hook event using the
// adapter's injected clock. It performs no filesystem access.
func (a *Adapter) ParseHookPayload(payload []byte) (domain.UsageEvent, error) {
	if a == nil {
		return domain.UsageEvent{}, errors.New("parse Codex hook payload: adapter is nil")
	}
	if a.options.now == nil {
		return domain.UsageEvent{}, errors.New("parse Codex hook payload: adapter clock is not configured")
	}
	return ParseHookPayload(payload, a.options.now())
}

func hookSourceTimestamp(raw json.RawMessage) (*time.Time, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("parse Codex hook payload: timestamp is invalid")
	}
	timestamp, ok := parseJSONTime(value)
	if !ok {
		return nil, errors.New("parse Codex hook payload: timestamp is invalid")
	}
	timestamp = timestamp.UTC()
	return &timestamp, nil
}

func classifyHookTool(toolName string) (domain.CapabilityType, string) {
	// Codex documents spawn_agent as the local function tool and Agent as its
	// matcher alias. No tool_input inspection is needed (or allowed) to prove
	// this identity, and Skill has no equivalent documented hook identity.
	if toolName == "spawn_agent" || toolName == "Agent" {
		return domain.CapabilityAgent, "spawn_agent"
	}
	if isMCPToolIdentity(toolName) {
		return domain.CapabilityMCPTool, toolName
	}
	return domain.CapabilityTool, toolName
}

func hookSourceIdentity(session, turnID, toolUseID string) string {
	toolUseID = strings.TrimSpace(toolUseID)
	if toolUseID == "" {
		// No documented stable delivery ID: the shared event fingerprint falls
		// back to source/observed time and remains conservative.
		return ""
	}
	// Scope tool_use_id by session and turn before hashing. A raw ID is not
	// returned in normalized metadata, and a reused ID in another turn/session
	// cannot collapse a legitimate call.
	return hashBytes([]byte(strings.Join([]string{
		"codex-hook-tool-use",
		strings.TrimSpace(session),
		strings.TrimSpace(turnID),
		toolUseID,
	}, "\x00")))
}
