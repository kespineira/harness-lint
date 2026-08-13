package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/hooks"
	"github.com/kespineira/harness-lint/internal/runtime/claude"
	"github.com/kespineira/harness-lint/internal/runtime/codex"
)

const maxIngestPayloadBytes = 8 << 20

// runIngest is deliberately independent from runtime adapters. A managed
// hook has already selected the runtime and sends one document; this path
// parses only that document and records one normalized observation.
func runIngest(ctx context.Context, config commandConfig, flags parsedFlags, stdin io.Reader) error {
	runtimeName, err := ingestRuntime(flags.runtime)
	if err != nil {
		return err
	}
	if flags.managedSet && flags.managedBy != hooks.ManagedMarker {
		return fmt.Errorf("unsupported --managed-by marker; want %s", hooks.ManagedMarker)
	}
	wantedEvent := strings.TrimSpace(flags.event)
	if flags.eventSet && wantedEvent == "" {
		return errors.New("--event cannot be empty")
	}
	if flags.eventSet && !validIngestEvent(runtimeName, wantedEvent) {
		return fmt.Errorf("unknown event for %s hook runtime", runtimeDisplayName(runtimeName))
	}

	payload, err := readIngestPayload(stdin)
	if err != nil {
		return err
	}
	observedAt := config.now.UTC()
	event, err := parseIngestPayload(runtimeName, payload, observedAt)
	if err != nil {
		return fmt.Errorf("ingest %s hook: %w", runtimeDisplayName(runtimeName), err)
	}
	if flags.eventSet {
		payloadEvent, ok := ingestPayloadEvent(payload)
		if !ok || payloadEvent != wantedEvent {
			return errors.New("hook payload event does not match --event")
		}
	}

	db, err := openStore(config)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	if err := db.InsertUsageEvents(ctx, []domain.UsageEvent{event}); err != nil {
		return fmt.Errorf("insert usage event: %w", err)
	}
	return nil
}

func readIngestPayload(stdin io.Reader) ([]byte, error) {
	if stdin == nil {
		return nil, errors.New("ingest requires one JSON document on stdin")
	}
	payload, err := io.ReadAll(io.LimitReader(stdin, maxIngestPayloadBytes+1))
	if err != nil {
		return nil, errors.New("read ingest payload: input could not be read")
	}
	if len(payload) > maxIngestPayloadBytes {
		return nil, fmt.Errorf("ingest payload exceeds %d bytes", maxIngestPayloadBytes)
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil, errors.New("ingest payload is empty")
	}
	return payload, nil
}

func ingestRuntime(value string) (domain.Runtime, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "claude", "claude-code":
		return domain.RuntimeClaudeCode, nil
	case "codex":
		return domain.RuntimeCodex, nil
	default:
		return domain.RuntimeUnknown, errors.New("unknown runtime; want claude or codex")
	}
}

func runtimeDisplayName(runtimeName domain.Runtime) string {
	if runtimeName == domain.RuntimeClaudeCode {
		return "Claude"
	}
	if runtimeName == domain.RuntimeCodex {
		return "Codex"
	}
	return string(runtimeName)
}

func validIngestEvent(runtimeName domain.Runtime, event string) bool {
	event = strings.TrimSpace(event)
	switch runtimeName {
	case domain.RuntimeClaudeCode:
		switch event {
		case "PostToolUse", "PostToolUseFailure", "UserPromptExpansion":
			return true
		default:
			return false
		}
	case domain.RuntimeCodex:
		return event == "PostToolUse"
	default:
		return false
	}
}

func parseIngestPayload(runtimeName domain.Runtime, payload []byte, observedAt time.Time) (domain.UsageEvent, error) {
	switch runtimeName {
	case domain.RuntimeClaudeCode:
		return claude.ParseHookPayload(payload, observedAt)
	case domain.RuntimeCodex:
		return codex.ParseHookPayload(payload, observedAt)
	default:
		return domain.UsageEvent{}, errors.New("unknown ingest runtime")
	}
}

func ingestPayloadEvent(payload []byte) (string, bool) {
	var envelope struct {
		HookEventName string `json:"hook_event_name"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return "", false
	}
	return strings.TrimSpace(envelope.HookEventName), envelope.HookEventName != ""
}
