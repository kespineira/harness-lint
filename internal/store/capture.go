package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/kespineira/harness-lint/internal/capture"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
)

// HealthReader is the narrow, read-only store surface consumed by capture
// diagnostics. The self-test writes only inside a transaction that it rolls
// back; callers still cannot reach arbitrary tables or mutation operations
// through this interface.
type HealthReader interface {
	SchemaStatus(context.Context) (SchemaStatus, error)
	SelfTestCaptureIngest(context.Context) error
	GetCaptureHealth(context.Context, domain.Runtime) (capture.DeliveryHealth, error)
}

var _ HealthReader = (*Store)(nil)

// IngestUsageEvent is the direct-hook write boundary. It requires hook
// provenance and commits the canonical deduplicated event, its evidence row,
// and a successful-delivery health update in one SQLite transaction. A retry
// with the same stable source identity remains one usage row but still counts
// as a successful delivery.
func (s *Store) IngestUsageEvent(ctx context.Context, event domain.UsageEvent) error {
	if s.isClosed() {
		return errors.New("store is closed")
	}
	if event.Provenance != domain.ProvenanceHook {
		return fmt.Errorf("usage ingestion requires hook provenance, got %q", event.Provenance)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin hook usage ingest: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	normalized, err := insertUsageEventTx(ctx, tx, event)
	if err != nil {
		return fmt.Errorf("ingest hook usage event: %w", err)
	}
	if err := markCaptureSuccessTx(ctx, tx, normalized.Runtime, normalized.ObservedAt); err != nil {
		return fmt.Errorf("record successful hook delivery: %w", err)
	}
	if err := openCaptureEpochTx(ctx, tx, normalized.Runtime, normalized.ObservedAt, history.CaptureStartReasonConfirmedDirectDelivery); err != nil {
		return fmt.Errorf("open capture epoch for successful hook delivery: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit hook usage ingest: %w", err)
	}
	return nil
}

// SelfTestCaptureIngest exercises the metadata-only direct-hook write path
// and its delivery-health update inside a transaction that is always rolled
// back. It compares usage, evidence, and health state before and after so a
// startup diagnostic can prove the schema and write path without invoking a
// model, runtime, network, MCP operation, or capture lifecycle transition.
func (s *Store) SelfTestCaptureIngest(ctx context.Context) error {
	if s.isClosed() {
		return errors.New("store is closed")
	}
	before, err := s.captureSelfTestState(ctx)
	if err != nil {
		return fmt.Errorf("read capture self-test state: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin capture self-test: %w", err)
	}
	event := domain.UsageEvent{
		ObservedAt:       time.Now().UTC(),
		Runtime:          domain.RuntimeCodex,
		SessionID:        "capture-self-test-session",
		ProjectID:        "capture-self-test-project",
		CapabilityType:   domain.CapabilityTool,
		CapabilityName:   "capture-self-test",
		EventType:        domain.EventInvoked,
		Provenance:       domain.ProvenanceHook,
		InvocationOrigin: domain.InvocationOriginUnknown,
		SchemaVersion:    domain.CurrentUsageEventSchemaVersion,
		SourceIdentity:   "capture-self-test-delivery",
	}
	normalized, err := insertUsageEventTx(ctx, tx, event)
	if err != nil {
		return rollbackCaptureSelfTest(tx, fmt.Errorf("write capture self-test event: %w", err))
	}
	if err := markCaptureSuccessTx(ctx, tx, normalized.Runtime, normalized.ObservedAt); err != nil {
		return rollbackCaptureSelfTest(tx, fmt.Errorf("write capture self-test health: %w", err))
	}
	if err := tx.Rollback(); err != nil {
		return fmt.Errorf("rollback capture self-test: %w", err)
	}
	after, err := s.captureSelfTestState(ctx)
	if err != nil {
		return fmt.Errorf("read capture self-test state after rollback: %w", err)
	}
	if !reflect.DeepEqual(before, after) {
		return errors.New("capture self-test changed persisted state after rollback")
	}
	return nil
}

type captureSelfTestState struct {
	UsageCount    int64
	EvidenceCount int64
	Health        []capture.DeliveryHealth
}

func (s *Store) captureSelfTestState(ctx context.Context) (captureSelfTestState, error) {
	var state captureSelfTestState
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_events`).Scan(&state.UsageCount); err != nil {
		return captureSelfTestState{}, fmt.Errorf("count usage events: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_event_evidence`).Scan(&state.EvidenceCount); err != nil {
		return captureSelfTestState{}, fmt.Errorf("count usage evidence: %w", err)
	}
	health, err := s.ListCaptureHealth(ctx)
	if err != nil {
		return captureSelfTestState{}, fmt.Errorf("read capture health: %w", err)
	}
	state.Health = health
	return state, nil
}

func rollbackCaptureSelfTest(tx *sql.Tx, cause error) error {
	if err := tx.Rollback(); err != nil {
		return fmt.Errorf("%w; rollback capture self-test: %v", cause, err)
	}
	return cause
}

// RecordCaptureFailure records only a coarse, bounded failure category. It
// does not accept an error value, so raw error text cannot cross the
// diagnostic persistence boundary.
func (s *Store) RecordCaptureFailure(ctx context.Context, failure capture.CaptureFailure) error {
	if s.isClosed() {
		return errors.New("store is closed")
	}
	if err := failure.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin capture failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := recordCaptureFailureTx(ctx, tx, failure); err != nil {
		return err
	}
	if failure.Kind.ProvesMissedDirectDelivery() {
		if err := closeCaptureEpochTx(ctx, tx, failure.Runtime, failure.FailedAt.UTC(), history.CaptureEndReasonConfirmedCaptureFailure); err != nil {
			return fmt.Errorf("close capture epoch for failed delivery: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit capture failure: %w", err)
	}
	return nil
}

func markCaptureSuccessTx(ctx context.Context, tx *sql.Tx, runtime domain.Runtime, deliveredAt time.Time) error {
	if !runtime.Valid() {
		return fmt.Errorf("invalid capture runtime %q", runtime)
	}
	if deliveredAt.IsZero() {
		return errors.New("successful capture delivery time is required")
	}
	deliveredAt = deliveredAt.UTC()
	_, err := tx.ExecContext(ctx, `INSERT INTO capture_delivery_health(
		runtime, last_successful_delivery, last_failed_delivery,
		consecutive_failure_count, last_failure_kind
	) VALUES (?, ?, NULL, 0, NULL)
	ON CONFLICT(runtime) DO UPDATE SET
	last_successful_delivery = CASE
		WHEN capture_delivery_health.last_successful_delivery IS NULL OR capture_delivery_health.last_successful_delivery < excluded.last_successful_delivery
		THEN excluded.last_successful_delivery ELSE capture_delivery_health.last_successful_delivery END,
	consecutive_failure_count = 0`, runtime, deliveredAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("update capture success health: %w", err)
	}
	return nil
}

func recordCaptureFailureTx(ctx context.Context, tx *sql.Tx, failure capture.CaptureFailure) error {
	failedAt := failure.FailedAt.UTC()
	kind, err := failure.Kind.FailureKindText()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO capture_delivery_health(
		runtime, last_successful_delivery, last_failed_delivery,
		consecutive_failure_count, last_failure_kind
	) VALUES (?, NULL, ?, 1, ?)
	ON CONFLICT(runtime) DO UPDATE SET
	last_failed_delivery = CASE
		WHEN capture_delivery_health.last_failed_delivery IS NULL OR capture_delivery_health.last_failed_delivery < excluded.last_failed_delivery
		THEN excluded.last_failed_delivery ELSE capture_delivery_health.last_failed_delivery END,
	consecutive_failure_count = MIN(capture_delivery_health.consecutive_failure_count + 1, ?),
	last_failure_kind = CASE
		WHEN capture_delivery_health.last_failed_delivery IS NULL OR capture_delivery_health.last_failed_delivery <= excluded.last_failed_delivery
		THEN excluded.last_failure_kind ELSE capture_delivery_health.last_failure_kind END`, failure.Runtime, failedAt.Format(time.RFC3339Nano), kind, capture.MaxConsecutiveFailures)
	if err != nil {
		return fmt.Errorf("update capture failure health: %w", err)
	}
	return nil
}

// GetCaptureHealth returns zero/nullable state when no capture observation has
// yet been persisted for runtime.
func (s *Store) GetCaptureHealth(ctx context.Context, runtime domain.Runtime) (capture.DeliveryHealth, error) {
	if s.isClosed() {
		return capture.DeliveryHealth{}, errors.New("store is closed")
	}
	if !runtime.Valid() {
		return capture.DeliveryHealth{}, fmt.Errorf("invalid capture runtime %q", runtime)
	}
	var successful, failed, kind sql.NullString
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT last_successful_delivery, last_failed_delivery, consecutive_failure_count, last_failure_kind FROM capture_delivery_health WHERE runtime = ?`, runtime).Scan(&successful, &failed, &count, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return capture.DeliveryHealth{Runtime: runtime}, nil
	}
	if err != nil {
		return capture.DeliveryHealth{}, fmt.Errorf("read capture health: %w", err)
	}
	health := capture.DeliveryHealth{Runtime: runtime, ConsecutiveFailures: count}
	health.LastSuccessfulDelivery, err = parseNullableTimestamp(successful)
	if err != nil {
		return capture.DeliveryHealth{}, fmt.Errorf("parse last successful delivery: %w", err)
	}
	health.LastFailedDelivery, err = parseNullableTimestamp(failed)
	if err != nil {
		return capture.DeliveryHealth{}, fmt.Errorf("parse last failed delivery: %w", err)
	}
	if kind.Valid && kind.String != "" {
		failureKind := capture.FailureKind(kind.String)
		if !failureKind.Valid() {
			return capture.DeliveryHealth{}, fmt.Errorf("invalid persisted capture failure kind %q", kind.String)
		}
		health.LastFailureKind = &failureKind
	}
	if err := health.Validate(); err != nil {
		return capture.DeliveryHealth{}, fmt.Errorf("validate persisted capture health: %w", err)
	}
	return health, nil
}

// ListCaptureHealth returns persisted runtime rows in deterministic runtime
// order. Runtimes with no observation are omitted; GetCaptureHealth returns
// their nullable zero state without creating a row.
func (s *Store) ListCaptureHealth(ctx context.Context) ([]capture.DeliveryHealth, error) {
	if s.isClosed() {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT runtime, last_successful_delivery, last_failed_delivery, consecutive_failure_count, last_failure_kind FROM capture_delivery_health ORDER BY runtime`)
	if err != nil {
		return nil, fmt.Errorf("list capture health: %w", err)
	}
	defer rows.Close()
	var result []capture.DeliveryHealth
	for rows.Next() {
		var runtime domain.Runtime
		var successful, failed, kind sql.NullString
		var count int
		if err := rows.Scan(&runtime, &successful, &failed, &count, &kind); err != nil {
			return nil, fmt.Errorf("scan capture health: %w", err)
		}
		health := capture.DeliveryHealth{Runtime: runtime, ConsecutiveFailures: count}
		health.LastSuccessfulDelivery, err = parseNullableTimestamp(successful)
		if err != nil {
			return nil, fmt.Errorf("parse capture success time: %w", err)
		}
		health.LastFailedDelivery, err = parseNullableTimestamp(failed)
		if err != nil {
			return nil, fmt.Errorf("parse capture failure time: %w", err)
		}
		if kind.Valid && kind.String != "" {
			failureKind := capture.FailureKind(kind.String)
			if !failureKind.Valid() {
				return nil, fmt.Errorf("invalid persisted capture failure kind %q", kind.String)
			}
			health.LastFailureKind = &failureKind
		}
		if err := health.Validate(); err != nil {
			return nil, fmt.Errorf("validate persisted capture health: %w", err)
		}
		result = append(result, health)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate capture health: %w", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Runtime < result[j].Runtime })
	return result, nil
}

func parseNullableTimestamp(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}
