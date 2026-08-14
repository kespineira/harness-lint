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

	"github.com/kespineira/harness-lint/internal/capture"
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
		return recordIngestFailure(ctx, config, runtimeName, capture.FailureMalformedPayload)
	}
	observedAt := config.now.UTC()
	event, err := parseIngestPayload(runtimeName, payload, observedAt)
	if err != nil {
		return recordIngestFailure(ctx, config, runtimeName, classifyIngestParseError(err))
	}
	if flags.eventSet {
		payloadEvent, ok := ingestPayloadEvent(payload)
		if !ok || payloadEvent != wantedEvent {
			return recordIngestFailure(ctx, config, runtimeName, capture.FailureUnsupportedEvent)
		}
	}

	db, err := openStore(config)
	if err != nil {
		return safeIngestError(runtimeName, classifyStoreError(err), false)
	}
	defer db.Close()
	if err := db.IngestUsageEvent(ctx, event); err != nil {
		kind := classifyStoreError(err)
		if recordErr := db.RecordCaptureFailure(ctx, capture.CaptureFailure{
			Runtime:  runtimeName,
			FailedAt: observedAt,
			Kind:     kind,
		}); recordErr != nil {
			return safeIngestError(runtimeName, classifyStoreError(recordErr), false)
		}
		return safeIngestError(runtimeName, kind, true)
	}
	return nil
}

// recordIngestFailure opens the store only after the invocation has passed the
// safe command-line checks. This lets malformed or unsupported hook documents
// leave one bounded health observation without ever persisting their payload or
// parser error. If the store cannot be opened, the primary error remains a
// stable category and the database failure is not copied into the response.
func recordIngestFailure(ctx context.Context, config commandConfig, runtimeName domain.Runtime, kind capture.FailureKind) error {
	db, err := openStore(config)
	if err != nil {
		return safeIngestError(runtimeName, kind, false)
	}
	defer db.Close()
	if err := db.RecordCaptureFailure(ctx, capture.CaptureFailure{
		Runtime:  runtimeName,
		FailedAt: config.now.UTC(),
		Kind:     kind,
	}); err != nil {
		return safeIngestError(runtimeName, classifyStoreError(err), false)
	}
	return safeIngestError(runtimeName, kind, true)
}

func safeIngestError(runtimeName domain.Runtime, kind capture.FailureKind, recorded bool) error {
	message := "ingest " + runtimeDisplayName(runtimeName) + " hook: " + ingestFailureMessage(kind)
	if !recorded {
		message += "; capture health was not recorded"
	}
	return errors.New(message)
}

func ingestFailureMessage(kind capture.FailureKind) string {
	switch kind {
	case capture.FailureMalformedPayload:
		return "malformed payload; expected one metadata-only JSON document on stdin"
	case capture.FailureUnsupportedEvent:
		return "unsupported event; verify the runtime hook event"
	case capture.FailureDatabaseBusy:
		return "database busy; retry the hook"
	case capture.FailureDatabaseUnavailable:
		return "database unavailable; verify the configured database"
	case capture.FailureSchemaError:
		return "database schema error; reopen or migrate the database"
	default:
		return "internal ingestion error"
	}
}

func classifyIngestParseError(err error) capture.FailureKind {
	if errors.Is(err, claude.ErrUnsupportedHookEvent) || strings.Contains(err.Error(), "only PostToolUse") || strings.Contains(err.Error(), "other hook events") {
		return capture.FailureUnsupportedEvent
	}
	if errors.Is(err, claude.ErrMalformedHookPayload) || strings.Contains(err.Error(), "payload") || strings.Contains(err.Error(), "JSON") {
		return capture.FailureMalformedPayload
	}
	return capture.FailureInternalError
}

func classifyStoreError(err error) capture.FailureKind {
	if err == nil {
		return capture.FailureInternalError
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "busy"), strings.Contains(message, "locked"):
		return capture.FailureDatabaseBusy
	case strings.Contains(message, "schema"), strings.Contains(message, "migration"), strings.Contains(message, "no such table"), strings.Contains(message, "no such column"), strings.Contains(message, "constraint"):
		return capture.FailureSchemaError
	case strings.Contains(message, "open"), strings.Contains(message, "closed"), strings.Contains(message, "unavailable"), strings.Contains(message, "permission denied"), strings.Contains(message, "no such file"), strings.Contains(message, "not a directory"), strings.Contains(message, "read-only"), strings.Contains(message, "operation not permitted"), strings.Contains(message, "i/o error"), strings.Contains(message, "disk"), strings.Contains(message, "connection"):
		return capture.FailureDatabaseUnavailable
	default:
		return capture.FailureInternalError
	}
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
