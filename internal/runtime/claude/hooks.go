package claude

// Hook field names and lifecycle semantics follow the Claude Code hooks
// reference: https://code.claude.com/docs/en/hooks.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

// ParseHookPayload normalizes one Claude Code command-hook JSON payload. It
// never starts a scan, opens a file, or retains tool arguments/results. The
// caller supplies the local receive time because Claude's hook input does not
// document a source occurrence timestamp.
func ParseHookPayload(payload []byte, observedAt time.Time) (domain.UsageEvent, error) {
	return ParseHookInput(bytes.NewReader(payload), observedAt)
}

// ParseHookInput is the stdin-oriented form of ParseHookPayload. It is kept
// separate from Adapter.ImportUsage so a CLI hook receiver can pass its stdin
// directly without granting the parser filesystem or process access.
func ParseHookInput(input io.Reader, observedAt time.Time) (domain.UsageEvent, error) {
	if input == nil {
		return domain.UsageEvent{}, fmt.Errorf("%w: input is nil", ErrMalformedHookPayload)
	}
	payload, err := decodeDirectHookPayload(input)
	if err != nil {
		return domain.UsageEvent{}, err
	}
	return normalizeDirectHookPayload(payload, observedAt)
}

var (
	// ErrMalformedHookPayload identifies empty, truncated, oversized, or
	// syntactically invalid hook input. Error details intentionally name only
	// the safe shape problem and never include raw stdin.
	ErrMalformedHookPayload = errors.New("malformed Claude hook payload")
	// ErrUnsupportedHookEvent is returned for valid JSON events outside this
	// parser's proof-of-usage scope.
	ErrUnsupportedHookEvent = errors.New("unsupported Claude hook event")
)

const maxDirectHookPayloadBytes = 8 << 20

type directHookPayload struct {
	eventName     string
	sessionID     string
	cwd           string
	toolName      string
	toolUseID     string
	expansionType string
	commandName   string
	toolInput     directToolInput
}

// These are the only tool_input values used by the direct parser. Claude's
// hooks reference documents Agent's subagent_type; Skill's identity is the
// skill selector used by the existing Claude adapter. Prompt, description,
// command, result, and every other tool-input value is deliberately skipped.
type directToolInput struct {
	skill        string
	subagentType string
}

type hookLimitReader struct {
	reader    io.Reader
	remaining int64
}

var errHookPayloadTooLarge = errors.New("hook payload exceeds parser limit")

func (r *hookLimitReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, errHookPayloadTooLarge
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	if err != nil {
		return n, err
	}
	return n, nil
}

func decodeDirectHookPayload(input io.Reader) (directHookPayload, error) {
	limited := &hookLimitReader{reader: input, remaining: maxDirectHookPayloadBytes + 1}
	decoder := json.NewDecoder(limited)
	first, err := decoder.Token()
	if err == io.EOF {
		return directHookPayload{}, fmt.Errorf("%w: input is empty", ErrMalformedHookPayload)
	}
	if err != nil {
		return directHookPayload{}, safeHookDecodeError(err)
	}
	if delimiter, ok := first.(json.Delim); !ok || delimiter != '{' {
		return directHookPayload{}, fmt.Errorf("%w: top-level JSON value must be an object", ErrMalformedHookPayload)
	}

	var payload directHookPayload
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return directHookPayload{}, safeHookDecodeError(err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return directHookPayload{}, fmt.Errorf("%w: object field name is invalid", ErrMalformedHookPayload)
		}
		switch key {
		case "hook_event_name":
			payload.eventName, err = decodeHookString(decoder, key)
		case "session_id":
			payload.sessionID, err = decodeHookString(decoder, key)
		case "cwd":
			payload.cwd, err = decodeHookString(decoder, key)
		case "tool_name":
			payload.toolName, err = decodeHookString(decoder, key)
		case "tool_use_id":
			payload.toolUseID, err = decodeHookString(decoder, key)
		case "expansion_type":
			payload.expansionType, err = decodeHookString(decoder, key)
		case "command_name":
			payload.commandName, err = decodeHookString(decoder, key)
		case "tool_input":
			payload.toolInput, err = decodeDirectToolInput(decoder)
		case "tool_response":
			// PostToolUse documents tool_response, but it is result content,
			// not identity evidence. Consume it structurally and retain none.
			err = skipJSONValue(decoder)
		case "timestamp", "event_timestamp":
			// Neither timestamp nor event_timestamp is documented by the
			// current Claude hooks reference. Consume these unknown fields but
			// never infer a source occurrence time from them: direct hook
			// events always leave SourceTimestamp nil.
			err = skipJSONValue(decoder)
		default:
			err = skipJSONValue(decoder)
		}
		if err != nil {
			return directHookPayload{}, safeHookDecodeError(err)
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return directHookPayload{}, safeHookDecodeError(err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return directHookPayload{}, fmt.Errorf("%w: top-level JSON object is not closed", ErrMalformedHookPayload)
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return directHookPayload{}, fmt.Errorf("%w: trailing JSON is not allowed", ErrMalformedHookPayload)
		}
		return directHookPayload{}, safeHookDecodeError(err)
	}
	return payload, nil
}

func safeHookDecodeError(err error) error {
	if errors.Is(err, errHookPayloadTooLarge) {
		return fmt.Errorf("%w: input exceeds %d bytes", ErrMalformedHookPayload, maxDirectHookPayloadBytes)
	}
	return fmt.Errorf("%w: invalid JSON", ErrMalformedHookPayload)
}

func decodeHookString(decoder *json.Decoder, field string) (string, error) {
	var value string
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("field %s must be a string", field)
	}
	return strings.TrimSpace(value), nil
}

func decodeDirectToolInput(decoder *json.Decoder) (directToolInput, error) {
	var input directToolInput
	value, err := decoder.Token()
	if err != nil {
		return input, err
	}
	if value == nil {
		return input, nil
	}
	delimiter, isDelimiter := value.(json.Delim)
	if !isDelimiter || delimiter != '{' {
		return input, skipJSONToken(decoder, value)
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return input, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return input, errors.New("tool_input field name is invalid")
		}
		switch key {
		case "skill":
			input.skill, err = decodeHookString(decoder, "tool_input.skill")
		case "subagent_type":
			input.subagentType, err = decodeHookString(decoder, "tool_input.subagent_type")
		default:
			err = skipJSONValue(decoder)
		}
		if err != nil {
			return input, err
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return input, err
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return input, errors.New("tool_input object is not closed")
	}
	return input, nil
}

func skipJSONValue(decoder *json.Decoder) error {
	value, err := decoder.Token()
	if err != nil {
		return err
	}
	return skipJSONToken(decoder, value)
}

func skipJSONToken(decoder *json.Decoder, value json.Token) error {
	delimiter, ok := value.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			if _, ok := key.(string); !ok {
				return errors.New("object field name is invalid")
			}
			if err := skipJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
			return errors.New("object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := skipJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := closing.(json.Delim); !ok || delimiter != ']' {
			return errors.New("array is not closed")
		}
	}
	return nil
}

func normalizeDirectHookPayload(payload directHookPayload, observedAt time.Time) (domain.UsageEvent, error) {
	if observedAt.IsZero() {
		return domain.UsageEvent{}, errors.New("Claude hook observed-at time is required")
	}
	if payload.eventName == "" {
		return domain.UsageEvent{}, fmt.Errorf("%w: event name is required", ErrMalformedHookPayload)
	}
	if payload.sessionID == "" {
		return domain.UsageEvent{}, fmt.Errorf("%w: session_id is required", ErrMalformedHookPayload)
	}
	if payload.cwd == "" {
		return domain.UsageEvent{}, fmt.Errorf("%w: cwd is required", ErrMalformedHookPayload)
	}

	capabilityType, capabilityName, invocationOrigin, sourceIdentity, err := classifyDirectHookPayload(payload)
	if err != nil {
		return domain.UsageEvent{}, err
	}
	event := domain.UsageEvent{
		ObservedAt:       observedAt.UTC(),
		SourceTimestamp:  nil,
		Runtime:          domain.RuntimeClaudeCode,
		SessionID:        hashIdentifier(payload.sessionID),
		ProjectID:        hashIdentifier(payload.cwd),
		CapabilityType:   capabilityType,
		CapabilityName:   capabilityName,
		EventType:        domain.EventInvoked,
		Provenance:       domain.ProvenanceHook,
		InvocationOrigin: invocationOrigin,
		SchemaVersion:    domain.CurrentUsageEventSchemaVersion,
		// SourceIdentity is hashed before returning so direct hook metadata is
		// safe even when callers serialize the event before persistence.
		SourceIdentity: domain.NormalizeSourceIdentity(sourceIdentity),
	}
	normalized, err := domain.NormalizeUsageEvent(event)
	if err != nil {
		return domain.UsageEvent{}, fmt.Errorf("normalize Claude hook event: %w", err)
	}
	fingerprint, err := domain.FingerprintForUsageEvent(normalized)
	if err != nil {
		return domain.UsageEvent{}, fmt.Errorf("fingerprint Claude hook event: %w", err)
	}
	normalized.Fingerprint = fingerprint
	return normalized, nil
}

func classifyDirectHookPayload(payload directHookPayload) (domain.CapabilityType, string, domain.InvocationOrigin, string, error) {
	switch payload.eventName {
	case "PostToolUse", "PostToolUseFailure":
		if payload.toolName == "" {
			return domain.CapabilityUnknown, "", domain.InvocationOriginUnknown, "", fmt.Errorf("%w: tool_name is required", ErrMalformedHookPayload)
		}
		capabilityType, capabilityName, err := classifyDirectTool(payload.toolName, payload.toolInput)
		if err != nil {
			return domain.CapabilityUnknown, "", domain.InvocationOriginUnknown, "", err
		}
		// Claude documents PostToolUse as successful completion and
		// PostToolUseFailure as a tool that started executing and then failed;
		// both therefore prove one model-selected invocation. A shared
		// tool-use source identity makes success/failure retries idempotent.
		return capabilityType, capabilityName, domain.InvocationOriginModelSelected,
			claudeToolUseSourceIdentity(payload.sessionID, payload.toolUseID), nil
	case "UserPromptExpansion":
		// The documented slash_command variant covers both skills and
		// custom commands, and exposes no discriminator. Normalize it as a
		// command rather than inventing a Skill classification. mcp_prompt
		// is intentionally outside this direct slash-command path.
		if payload.expansionType != "slash_command" {
			return domain.CapabilityUnknown, "", domain.InvocationOriginUnknown, "", ErrUnsupportedHookEvent
		}
		if payload.commandName == "" {
			return domain.CapabilityUnknown, "", domain.InvocationOriginUnknown, "", fmt.Errorf("%w: command_name is required", ErrMalformedHookPayload)
		}
		return domain.CapabilityCommand, payload.commandName, domain.InvocationOriginUserExplicit, "", nil
	default:
		// SubagentStart supplies a documented agent_id, but counting it as
		// invocation would double count the Agent PostToolUse path; Stop is
		// completion state rather than a new invocation. Both remain an
		// explicit limitation until a separate loaded/state contract exists.
		return domain.CapabilityUnknown, "", domain.InvocationOriginUnknown, "", ErrUnsupportedHookEvent
	}
}

func classifyDirectTool(toolName string, input directToolInput) (domain.CapabilityType, string, error) {
	if strings.HasPrefix(toolName, "mcp__") {
		rest := strings.TrimPrefix(toolName, "mcp__")
		parts := strings.SplitN(rest, "__", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
			return domain.CapabilityMCPTool, toolName, nil
		}
		return domain.CapabilityUnknown, "", fmt.Errorf("%w: MCP tool name is invalid", ErrMalformedHookPayload)
	}
	switch toolName {
	case "Skill":
		if input.skill == "" {
			return domain.CapabilityUnknown, "", fmt.Errorf("%w: Skill identity is unavailable", ErrMalformedHookPayload)
		}
		return domain.CapabilitySkill, input.skill, nil
	case "Agent", "Task":
		if input.subagentType == "" {
			return domain.CapabilityUnknown, "", fmt.Errorf("%w: agent identity is unavailable", ErrMalformedHookPayload)
		}
		return domain.CapabilityAgent, input.subagentType, nil
	default:
		return domain.CapabilityTool, toolName, nil
	}
}

func claudeToolUseSourceIdentity(sessionID, toolUseID string) string {
	if strings.TrimSpace(toolUseID) == "" {
		return ""
	}
	return strings.Join([]string{"claude-code", "hook", "tool-use", strings.TrimSpace(sessionID), strings.TrimSpace(toolUseID)}, "\x00")
}
