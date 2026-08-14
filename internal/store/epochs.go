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
// delivery evidence. Repeating the same open operation is a no-op; a second
// open while an interval is active is rejected rather than silently extending
// or replacing the interval.
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
	var id int64
	var existingStart, existingEnd, existingStartReason, existingEndReason sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id, started_at, ended_at, start_reason, end_reason FROM capture_epochs WHERE runtime = ? ORDER BY started_at DESC, id DESC LIMIT 1`, runtime).Scan(&id, &existingStart, &existingEnd, &existingStartReason, &existingEndReason)
	if err == nil {
		parsedStart, parseErr := parseEpochTimestamp(existingStart.String, "capture epoch start")
		if parseErr != nil {
			return parseErr
		}
		startedAt = startedAt.UTC()
		if !existingEnd.Valid || existingEnd.String == "" {
			if parsedStart.Equal(startedAt) && existingStartReason.String == string(reason) {
				return nil
			}
			return fmt.Errorf("capture epoch for %q is already open", runtime)
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
	_, err = tx.ExecContext(ctx, `INSERT INTO capture_epochs(runtime, started_at, start_reason) VALUES (?, ?, ?)`, runtime, startedAt.UTC().Format(time.RFC3339Nano), reason)
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
	var started, existingEnd, existingReason sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id, started_at, ended_at, end_reason FROM capture_epochs WHERE runtime = ? ORDER BY started_at DESC, id DESC LIMIT 1`, runtime).Scan(&id, &started, &existingEnd, &existingReason)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("no capture epoch exists for %q", runtime)
	}
	if err != nil {
		return fmt.Errorf("read latest capture epoch: %w", err)
	}
	end := endedAt.UTC()
	if existingEnd.Valid && existingEnd.String != "" {
		parsedEnd, parseErr := parseEpochTimestamp(existingEnd.String, "capture epoch end")
		if parseErr != nil {
			return parseErr
		}
		if parsedEnd.Equal(end) && existingReason.String == string(reason) {
			return nil
		}
		return fmt.Errorf("capture epoch for %q is already closed", runtime)
	}
	start, err := parseEpochTimestamp(started.String, "capture epoch start")
	if err != nil {
		return err
	}
	if !end.After(start) {
		return fmt.Errorf("capture epoch end %s must be after start %s", end.Format(time.RFC3339Nano), start.Format(time.RFC3339Nano))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE capture_epochs SET ended_at = ?, end_reason = ? WHERE id = ? AND ended_at IS NULL`, end.Format(time.RFC3339Nano), reason, id); err != nil {
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

// ListCapabilityPresenceEpochs returns presence intervals in chronological
// key order. A key filter is optional; no flattened inventory ranges are read.
func (s *Store) ListCapabilityPresenceEpochs(ctx context.Context, keys ...history.CoverageKey) ([]history.CapabilityPresenceEpoch, error) {
	if s.isClosed() {
		return nil, errors.New("store is closed")
	}
	if len(keys) > 1 {
		return nil, errors.New("at most one capability presence key filter is allowed")
	}
	query := `SELECT runtime, capability_type, capability_name, started_at, ended_at FROM capability_presence_epochs`
	args := make([]any, 0, 3)
	if len(keys) == 1 {
		if err := keys[0].Validate(); err != nil {
			return nil, err
		}
		query += ` WHERE runtime = ? AND capability_type = ? AND capability_name = ?`
		args = append(args, keys[0].Runtime, keys[0].CapabilityType, keys[0].CapabilityName)
	}
	query += ` ORDER BY runtime, capability_type, capability_name, started_at, id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list capability presence epochs: %w", err)
	}
	defer rows.Close()
	result := make([]history.CapabilityPresenceEpoch, 0)
	for rows.Next() {
		var epoch history.CapabilityPresenceEpoch
		var started, ended sql.NullString
		if err := rows.Scan(&epoch.Runtime, &epoch.CapabilityType, &epoch.CapabilityName, &started, &ended); err != nil {
			return nil, fmt.Errorf("scan capability presence epoch: %w", err)
		}
		epoch.Interval.Start, err = parseEpochTimestamp(started.String, "capability presence start")
		if err != nil {
			return nil, err
		}
		if ended.Valid && ended.String != "" {
			epoch.Interval.End, err = parseEpochTimestamp(ended.String, "capability presence end")
			if err != nil {
				return nil, err
			}
		}
		if err := epoch.Validate(); err != nil {
			return nil, fmt.Errorf("validate persisted capability presence epoch: %w", err)
		}
		result = append(result, epoch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate capability presence epochs: %w", err)
	}
	return result, nil
}

type presenceKey struct {
	runtime domain.Runtime
	typ     domain.CapabilityType
	name    string
}

func transitionPresenceEpochsTx(ctx context.Context, tx *sql.Tx, runtime domain.Runtime, observedAt time.Time, capabilities []domain.Capability) error {
	newKeys := make(map[presenceKey]struct{}, len(capabilities))
	for _, capability := range capabilities {
		newKeys[presenceKey{runtime: capability.Runtime, typ: capability.Type, name: capability.Name}] = struct{}{}
	}
	oldKeys := make(map[presenceKey]struct{})
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT runtime, capability_type, name FROM current_inventory WHERE runtime = ?`, runtime)
	if err != nil {
		return fmt.Errorf("read previous inventory presence keys: %w", err)
	}
	for rows.Next() {
		var key presenceKey
		if err := rows.Scan(&key.runtime, &key.typ, &key.name); err != nil {
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO capability_presence_epochs(runtime, capability_type, capability_name, started_at) VALUES (?, ?, ?, ?)`, key.runtime, key.typ, key.name, observedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("open capability presence %q: %w", key.name, err)
		}
	}
	return nil
}

func openPresenceKeysTx(ctx context.Context, tx *sql.Tx, runtime domain.Runtime) (map[presenceKey]struct{}, error) {
	rows, err := tx.QueryContext(ctx, `SELECT runtime, capability_type, capability_name FROM capability_presence_epochs WHERE runtime = ? AND ended_at IS NULL`, runtime)
	if err != nil {
		return nil, fmt.Errorf("read open capability presence epochs: %w", err)
	}
	defer rows.Close()
	result := make(map[presenceKey]struct{})
	for rows.Next() {
		var key presenceKey
		if err := rows.Scan(&key.runtime, &key.typ, &key.name); err != nil {
			return nil, fmt.Errorf("scan open capability presence key: %w", err)
		}
		result[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate open capability presence keys: %w", err)
	}
	return result, nil
}

func closePresenceEpochTx(ctx context.Context, tx *sql.Tx, key presenceKey, endedAt time.Time) error {
	var id int64
	var started string
	err := tx.QueryRowContext(ctx, `SELECT id, started_at FROM capability_presence_epochs WHERE runtime = ? AND capability_type = ? AND capability_name = ? AND ended_at IS NULL ORDER BY started_at DESC, id DESC LIMIT 1`, key.runtime, key.typ, key.name).Scan(&id, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read open capability presence %q: %w", key.name, err)
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
			return fmt.Errorf("discard zero-length capability presence %q: %w", key.name, err)
		}
		return nil
	}
	if endedAt.Before(start) {
		return fmt.Errorf("capability presence end %s precedes start %s", endedAt.Format(time.RFC3339Nano), start.Format(time.RFC3339Nano))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE capability_presence_epochs SET ended_at = ? WHERE id = ? AND ended_at IS NULL`, endedAt.Format(time.RFC3339Nano), id); err != nil {
		return fmt.Errorf("close capability presence %q: %w", key.name, err)
	}
	return nil
}

func parseEpochTimestamp(raw, label string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, fmt.Errorf("%s is empty", label)
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %s: %w", label, err)
	}
	return parsed.UTC(), nil
}
