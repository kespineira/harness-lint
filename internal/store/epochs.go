package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
)

// OpenCaptureEpoch starts a reliable capture interval after confirmed direct
// delivery evidence. A same-or-later delivery while an interval is active is
// an idempotent continuation; an earlier delivery is rejected.
func (s *Store) OpenCaptureEpoch(ctx context.Context, runtime domain.Runtime, startedAt time.Time, reason history.CaptureStartReason) error {
	if s.isClosed() {
		return errors.New("store is closed")
	}
	if !runtime.Valid() {
		return fmt.Errorf("invalid capture epoch runtime %q", runtime)
	}
	if startedAt.IsZero() {
		return errors.New("capture epoch start time is required")
	}
	if !reason.Valid() {
		return fmt.Errorf("invalid capture epoch start reason %q", reason)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin capture epoch open: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := openCaptureEpochTx(ctx, tx, runtime, startedAt.UTC(), reason); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit capture epoch open: %w", err)
	}
	return nil
}

func openCaptureEpochTx(ctx context.Context, tx *sql.Tx, runtime domain.Runtime, startedAt time.Time, reason history.CaptureStartReason) error {
	var existingStart, existingEnd sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT started_at, ended_at FROM capture_epochs WHERE runtime = ? ORDER BY started_at DESC, id DESC LIMIT 1`, runtime).Scan(&existingStart, &existingEnd)
	if err == nil {
		parsedStart, parseErr := parseEpochTimestamp(existingStart.String, "capture epoch start")
		if parseErr != nil {
			return parseErr
		}
		startedAt = startedAt.UTC()
		if !existingEnd.Valid || existingEnd.String == "" {
			if startedAt.Before(parsedStart) {
				return fmt.Errorf("capture epoch start %s precedes open epoch start %s", startedAt.Format(time.RFC3339Nano), parsedStart.Format(time.RFC3339Nano))
			}
			// A confirmed delivery at the same or a later time proves that the
			// current epoch is still open; it does not create a new interval.
			return nil
		}
		parsedEnd, parseErr := parseEpochTimestamp(existingEnd.String, "capture epoch end")
		if parseErr != nil {
			return parseErr
		}
		if startedAt.Before(parsedEnd) {
			return fmt.Errorf("capture epoch start %s precedes latest epoch end %s", startedAt.Format(time.RFC3339Nano), parsedEnd.Format(time.RFC3339Nano))
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read latest capture epoch: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO capture_epochs(runtime, started_at, start_reason) VALUES (?, ?, ?)`, runtime, formatEpochTimestamp(startedAt), reason)
	if err != nil {
		return fmt.Errorf("insert capture epoch: %w", err)
	}
	return nil
}

// CloseCaptureEpoch closes the active reliable capture interval with bounded
// lifecycle evidence. Repeating the exact close operation is a no-op.
func (s *Store) CloseCaptureEpoch(ctx context.Context, runtime domain.Runtime, endedAt time.Time, reason history.CaptureEndReason) error {
	if s.isClosed() {
		return errors.New("store is closed")
	}
	if !runtime.Valid() {
		return fmt.Errorf("invalid capture epoch runtime %q", runtime)
	}
	if endedAt.IsZero() {
		return errors.New("capture epoch end time is required")
	}
	if !reason.Valid() {
		return fmt.Errorf("invalid capture epoch end reason %q", reason)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin capture epoch close: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := closeCaptureEpochTx(ctx, tx, runtime, endedAt.UTC(), reason); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit capture epoch close: %w", err)
	}
	return nil
}

func closeCaptureEpochTx(ctx context.Context, tx *sql.Tx, runtime domain.Runtime, endedAt time.Time, reason history.CaptureEndReason) error {
	var id int64
	var started string
	err := tx.QueryRowContext(ctx, `SELECT id, started_at FROM capture_epochs WHERE runtime = ? AND ended_at IS NULL ORDER BY started_at DESC, id DESC LIMIT 1`, runtime).Scan(&id, &started)
	if errors.Is(err, sql.ErrNoRows) {
		// Closing an already-closed or never-opened runtime is deliberately
		// idempotent; uninstall delivery may be repeated by lifecycle code.
		return nil
	}
	if err != nil {
		return fmt.Errorf("read latest capture epoch: %w", err)
	}
	end := endedAt.UTC()
	start, err := parseEpochTimestamp(started, "capture epoch start")
	if err != nil {
		return err
	}
	if end.Before(start) {
		return fmt.Errorf("capture epoch end %s must be after start %s", end.Format(time.RFC3339Nano), start.Format(time.RFC3339Nano))
	}
	if end.Equal(start) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM capture_epochs WHERE id = ? AND ended_at IS NULL`, id); err != nil {
			return fmt.Errorf("discard zero-length capture epoch: %w", err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE capture_epochs SET ended_at = ?, end_reason = ? WHERE id = ? AND ended_at IS NULL`, formatEpochTimestamp(end), reason, id); err != nil {
		return fmt.Errorf("close capture epoch: %w", err)
	}
	return nil
}

// ListCaptureEpochs returns capture intervals in chronological order. A zero
// runtime lists all runtimes; a non-zero runtime narrows the query.
func (s *Store) ListCaptureEpochs(ctx context.Context, runtimes ...domain.Runtime) ([]history.CaptureEpoch, error) {
	if s.isClosed() {
		return nil, errors.New("store is closed")
	}
	query := `SELECT runtime, started_at, ended_at, start_reason, end_reason FROM capture_epochs`
	args := make([]any, 0, 1)
	if len(runtimes) > 1 {
		return nil, errors.New("at most one capture epoch runtime filter is allowed")
	}
	if len(runtimes) == 1 {
		if !runtimes[0].Valid() {
			return nil, fmt.Errorf("invalid capture epoch runtime %q", runtimes[0])
		}
		query += ` WHERE runtime = ?`
		args = append(args, runtimes[0])
	}
	query += ` ORDER BY runtime, started_at, id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list capture epochs: %w", err)
	}
	defer rows.Close()
	result := make([]history.CaptureEpoch, 0)
	for rows.Next() {
		var epoch history.CaptureEpoch
		var started, ended, startReason, endReason sql.NullString
		if err := rows.Scan(&epoch.Runtime, &started, &ended, &startReason, &endReason); err != nil {
			return nil, fmt.Errorf("scan capture epoch: %w", err)
		}
		epoch.Interval.Start, err = parseEpochTimestamp(started.String, "capture epoch start")
		if err != nil {
			return nil, err
		}
		if ended.Valid && ended.String != "" {
			epoch.Interval.End, err = parseEpochTimestamp(ended.String, "capture epoch end")
			if err != nil {
				return nil, err
			}
		}
		epoch.StartReason = history.CaptureStartReason(startReason.String)
		if endReason.Valid {
			epoch.EndReason = history.CaptureEndReason(endReason.String)
		}
		if err := epoch.Validate(); err != nil {
			return nil, fmt.Errorf("validate persisted capture epoch: %w", err)
		}
		result = append(result, epoch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate capture epochs: %w", err)
	}
	return result, nil
}

// ListCapabilityPresenceEpochs returns persisted presence intervals in
// chronological full-identity order. A key filter is optional; no flattened
// inventory ranges or canonical history aggregates are read.
func (s *Store) ListCapabilityPresenceEpochs(ctx context.Context, keys ...CapabilityPresenceKey) ([]CapabilityPresenceEpoch, error) {
	if s.isClosed() {
		return nil, errors.New("store is closed")
	}
	if len(keys) > 1 {
		return nil, errors.New("at most one capability presence key filter is allowed")
	}
	query := `SELECT runtime, capability_type, capability_name, scope, source, started_at, ended_at FROM capability_presence_epochs`
	args := make([]any, 0, 5)
	if len(keys) == 1 {
		if err := keys[0].Validate(); err != nil {
			return nil, err
		}
		query += ` WHERE runtime = ? AND capability_type = ? AND capability_name = ? AND scope = ? AND source = ?`
		args = append(args, keys[0].Runtime, keys[0].CapabilityType, keys[0].CapabilityName, keys[0].Scope, keys[0].Source)
	}
	query += ` ORDER BY runtime, capability_type, capability_name, scope, source, started_at, id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list capability presence epochs: %w", err)
	}
	defer rows.Close()
	result := make([]CapabilityPresenceEpoch, 0)
	for rows.Next() {
		var row CapabilityPresenceEpoch
		var started, ended sql.NullString
		if err := rows.Scan(&row.Key.Runtime, &row.Key.CapabilityType, &row.Key.CapabilityName, &row.Key.Scope, &row.Key.Source, &started, &ended); err != nil {
			return nil, fmt.Errorf("scan capability presence epoch: %w", err)
		}
		row.Interval.Start, err = parseEpochTimestamp(started.String, "capability presence start")
		if err != nil {
			return nil, err
		}
		if ended.Valid && ended.String != "" {
			row.Interval.End, err = parseEpochTimestamp(ended.String, "capability presence end")
			if err != nil {
				return nil, err
			}
		}
		if err := row.Validate(); err != nil {
			return nil, fmt.Errorf("validate persisted capability presence epoch: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate capability presence epochs: %w", err)
	}
	return result, nil
}

// CapabilityPresenceKey identifies one persisted capability definition. It
// intentionally retains scope and source; history.CoverageKey is a separate
// canonical aggregation key for effective coverage queries.
type CapabilityPresenceKey struct {
	Runtime        domain.Runtime
	CapabilityType domain.CapabilityType
	CapabilityName string
	Scope          domain.Scope
	Source         string
}

func (k CapabilityPresenceKey) Validate() error {
	if !k.Runtime.Valid() {
		return fmt.Errorf("invalid capability presence runtime %q", k.Runtime)
	}
	if !k.CapabilityType.Valid() {
		return fmt.Errorf("invalid capability presence type %q", k.CapabilityType)
	}
	if strings.TrimSpace(k.CapabilityName) == "" {
		return errors.New("capability presence name is required")
	}
	if !k.Scope.Valid() {
		return fmt.Errorf("invalid capability presence scope %q", k.Scope)
	}
	return nil
}

// CapabilityPresenceEpoch is one persisted, full-identity capability
// presence interval. It is deliberately not a history.CapabilityPresenceEpoch
// because persistence retains definition scope and source.
type CapabilityPresenceEpoch struct {
	Key CapabilityPresenceKey
	history.Interval
}

func (e CapabilityPresenceEpoch) Validate() error {
	if err := e.Key.Validate(); err != nil {
		return fmt.Errorf("invalid capability presence key: %w", err)
	}
	if err := e.Interval.Validate(); err != nil {
		return fmt.Errorf("invalid capability presence interval: %w", err)
	}
	return nil
}

func transitionPresenceEpochsTx(ctx context.Context, tx *sql.Tx, runtime domain.Runtime, observedAt time.Time, capabilities []domain.Capability) error {
	newKeys := make(map[CapabilityPresenceKey]struct{}, len(capabilities))
	for _, capability := range capabilities {
		newKeys[CapabilityPresenceKey{Runtime: capability.Runtime, CapabilityType: capability.Type, CapabilityName: capability.Name, Scope: capability.Scope, Source: capability.Source}] = struct{}{}
	}
	oldKeys := make(map[CapabilityPresenceKey]struct{})
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT runtime, capability_type, name, scope, source FROM current_inventory WHERE runtime = ?`, runtime)
	if err != nil {
		return fmt.Errorf("read previous inventory presence keys: %w", err)
	}
	for rows.Next() {
		var key CapabilityPresenceKey
		if err := rows.Scan(&key.Runtime, &key.CapabilityType, &key.CapabilityName, &key.Scope, &key.Source); err != nil {
			rows.Close()
			return fmt.Errorf("scan previous inventory presence key: %w", err)
		}
		oldKeys[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate previous inventory presence keys: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close previous inventory presence keys: %w", err)
	}
	openKeys, err := openPresenceKeysTx(ctx, tx, runtime)
	if err != nil {
		return err
	}
	for key := range oldKeys {
		if _, present := newKeys[key]; !present {
			if err := closePresenceEpochTx(ctx, tx, key, observedAt); err != nil {
				return err
			}
		}
	}
	for key := range openKeys {
		if _, present := newKeys[key]; !present {
			if err := closePresenceEpochTx(ctx, tx, key, observedAt); err != nil {
				return err
			}
		}
	}
	for key := range newKeys {
		if _, open := openKeys[key]; open {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO capability_presence_epochs(runtime, capability_type, capability_name, scope, source, started_at) VALUES (?, ?, ?, ?, ?, ?)`, key.Runtime, key.CapabilityType, key.CapabilityName, key.Scope, key.Source, formatEpochTimestamp(observedAt)); err != nil {
			return fmt.Errorf("open capability presence %q: %w", key.CapabilityName, err)
		}
	}
	return nil
}

func openPresenceKeysTx(ctx context.Context, tx *sql.Tx, runtime domain.Runtime) (map[CapabilityPresenceKey]struct{}, error) {
	rows, err := tx.QueryContext(ctx, `SELECT runtime, capability_type, capability_name, scope, source FROM capability_presence_epochs WHERE runtime = ? AND ended_at IS NULL`, runtime)
	if err != nil {
		return nil, fmt.Errorf("read open capability presence epochs: %w", err)
	}
	defer rows.Close()
	result := make(map[CapabilityPresenceKey]struct{})
	for rows.Next() {
		var key CapabilityPresenceKey
		if err := rows.Scan(&key.Runtime, &key.CapabilityType, &key.CapabilityName, &key.Scope, &key.Source); err != nil {
			return nil, fmt.Errorf("scan open capability presence key: %w", err)
		}
		result[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate open capability presence keys: %w", err)
	}
	return result, nil
}

func closePresenceEpochTx(ctx context.Context, tx *sql.Tx, key CapabilityPresenceKey, endedAt time.Time) error {
	var id int64
	var started string
	err := tx.QueryRowContext(ctx, `SELECT id, started_at FROM capability_presence_epochs WHERE runtime = ? AND capability_type = ? AND capability_name = ? AND scope = ? AND source = ? AND ended_at IS NULL ORDER BY started_at DESC, id DESC LIMIT 1`, key.Runtime, key.CapabilityType, key.CapabilityName, key.Scope, key.Source).Scan(&id, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read open capability presence %q: %w", key.CapabilityName, err)
	}
	start, err := parseEpochTimestamp(started, "capability presence start")
	if err != nil {
		return err
	}
	endedAt = endedAt.UTC()
	if endedAt.Equal(start) {
		// A same-timestamp replacement has no positive half-open interval;
		// do not persist a zero-length epoch.
		if _, err := tx.ExecContext(ctx, `DELETE FROM capability_presence_epochs WHERE id = ?`, id); err != nil {
			return fmt.Errorf("discard zero-length capability presence %q: %w", key.CapabilityName, err)
		}
		return nil
	}
	if endedAt.Before(start) {
		return fmt.Errorf("capability presence end %s precedes start %s", endedAt.Format(time.RFC3339Nano), start.Format(time.RFC3339Nano))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE capability_presence_epochs SET ended_at = ? WHERE id = ? AND ended_at IS NULL`, formatEpochTimestamp(endedAt), id); err != nil {
		return fmt.Errorf("close capability presence %q: %w", key.CapabilityName, err)
	}
	return nil
}

func parseEpochTimestamp(raw, label string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("%s is empty", label)
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %s: %w", label, err)
	}
	return parsed.UTC(), nil
}

const epochTimestampLayout = "2006-01-02T15:04:05.000000000Z"

func formatEpochTimestamp(value time.Time) string {
	return value.UTC().Format(epochTimestampLayout)
}
