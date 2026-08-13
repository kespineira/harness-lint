// Package store provides the daemonless SQLite persistence layer for the
// runtime-neutral domain. SessionID and ProjectID are intentionally opaque:
// callers must pre-normalize or hash them before insertion.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

const schemaVersion = 1

// Store is a concurrency-safe database handle. SQLite serializes writes and
// each public write operation uses one transaction.
type Store struct {
	db     *sql.DB
	closed atomic.Bool
}

// Open opens path and applies forward-only embedded migrations. Use ":memory:"
// for a transient store in tests; callers own the path and identifiers passed
// to this package.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("store path is required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying SQLite handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if s.closed.Swap(true) {
		return nil
	}
	return s.db.Close()
}

func (s *Store) isClosed() bool {
	return s == nil || s.db == nil || s.closed.Load()
}

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_meta (key TEXT PRIMARY KEY NOT NULL, value TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create schema metadata: %w", err)
	}
	var current int
	err = tx.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM schema_meta WHERE key = 'version'`).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		current = 0
	} else if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", current, schemaVersion)
	}
	for version := current + 1; version <= schemaVersion; version++ {
		migration, err := migrations.ReadFile(fmt.Sprintf("migrations/%03d_initial.sql", version))
		if err != nil {
			return fmt.Errorf("read migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, string(migration)); err != nil {
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_meta(key, value) VALUES ('version', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, version); err != nil {
			return fmt.Errorf("record migration %d: %w", version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

// UpsertCapabilities idempotently writes an inventory snapshot. The natural
// key is runtime/type/name/scope; source and observations are refreshed while
// first_seen and last_seen retain their complete observed range.
func (s *Store) UpsertCapabilities(ctx context.Context, capabilities []domain.Capability) error {
	if s.isClosed() {
		return errors.New("store is closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin capability upsert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	const query = `INSERT INTO capabilities (
runtime, capability_type, name, scope, source, enabled, hash,
context_value, context_confidence, context_basis,
input_tokens_value, input_tokens_confidence, input_tokens_basis,
output_tokens_value, output_tokens_confidence, output_tokens_basis, first_seen, last_seen
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(runtime, capability_type, name, scope) DO UPDATE SET
source = excluded.source, enabled = excluded.enabled, hash = excluded.hash,
context_value = excluded.context_value, context_confidence = excluded.context_confidence, context_basis = excluded.context_basis,
input_tokens_value = excluded.input_tokens_value, input_tokens_confidence = excluded.input_tokens_confidence, input_tokens_basis = excluded.input_tokens_basis,
output_tokens_value = excluded.output_tokens_value, output_tokens_confidence = excluded.output_tokens_confidence, output_tokens_basis = excluded.output_tokens_basis,
first_seen = CASE WHEN excluded.first_seen = '' THEN capabilities.first_seen WHEN capabilities.first_seen = '' OR excluded.first_seen < capabilities.first_seen THEN excluded.first_seen ELSE capabilities.first_seen END,
last_seen = CASE WHEN excluded.last_seen = '' THEN capabilities.last_seen WHEN capabilities.last_seen = '' OR excluded.last_seen > capabilities.last_seen THEN excluded.last_seen ELSE capabilities.last_seen END`
	for _, capability := range capabilities {
		if err := capability.Validate(); err != nil {
			return fmt.Errorf("validate capability %q: %w", capability.Name, err)
		}
		firstSeen, lastSeen := capability.FirstSeen.UTC().Format(time.RFC3339Nano), capability.LastSeen.UTC().Format(time.RFC3339Nano)
		if capability.FirstSeen.IsZero() {
			firstSeen, lastSeen = "", ""
		}
		m := capability.Context
		in := capability.InputTokens
		out := capability.OutputTokens
		if _, err := tx.ExecContext(ctx, query, capability.Runtime, capability.Type, capability.Name, capability.Scope, capability.Source, boolInt(capability.Enabled), capability.Hash,
			m.Value, m.Confidence, m.Basis, in.Value, in.Confidence, in.Basis, out.Value, out.Confidence, out.Basis, firstSeen, lastSeen); err != nil {
			return fmt.Errorf("upsert capability %q: %w", capability.Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit capability upsert: %w", err)
	}
	return nil
}

// ListCapabilities returns all inventory in deterministic natural-key order.
func (s *Store) ListCapabilities(ctx context.Context) ([]domain.Capability, error) {
	if s.isClosed() {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT runtime, capability_type, name, scope, source, enabled, hash, context_value, context_confidence, context_basis, input_tokens_value, input_tokens_confidence, input_tokens_basis, output_tokens_value, output_tokens_confidence, output_tokens_basis, first_seen, last_seen FROM capabilities ORDER BY runtime, capability_type, name, scope`)
	if err != nil {
		return nil, fmt.Errorf("list capabilities: %w", err)
	}
	defer rows.Close()
	var result []domain.Capability
	for rows.Next() {
		var c domain.Capability
		var enabled int
		var firstSeen, lastSeen string
		if err := rows.Scan(&c.Runtime, &c.Type, &c.Name, &c.Scope, &c.Source, &enabled, &c.Hash, &c.Context.Value, &c.Context.Confidence, &c.Context.Basis, &c.InputTokens.Value, &c.InputTokens.Confidence, &c.InputTokens.Basis, &c.OutputTokens.Value, &c.OutputTokens.Confidence, &c.OutputTokens.Basis, &firstSeen, &lastSeen); err != nil {
			return nil, fmt.Errorf("scan capability: %w", err)
		}
		c.Enabled = enabled != 0
		if firstSeen != "" {
			c.FirstSeen, err = time.Parse(time.RFC3339Nano, firstSeen)
			if err != nil {
				return nil, fmt.Errorf("parse capability first_seen: %w", err)
			}
			c.LastSeen, err = time.Parse(time.RFC3339Nano, lastSeen)
			if err != nil {
				return nil, fmt.Errorf("parse capability last_seen: %w", err)
			}
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate capabilities: %w", err)
	}
	return result, nil
}

// InsertUsageEvents idempotently inserts metadata-only events by fingerprint.
// Blank fingerprints are derived from the event's canonical metadata.
func (s *Store) InsertUsageEvents(ctx context.Context, events []domain.UsageEvent) error {
	if s.isClosed() {
		return errors.New("store is closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin usage insert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, event := range events {
		if err := event.Validate(); err != nil {
			return fmt.Errorf("validate usage event: %w", err)
		}
		fingerprint := event.Fingerprint
		if strings.TrimSpace(fingerprint) == "" {
			fingerprint, err = domain.FingerprintForUsageEvent(event)
			if err != nil {
				return fmt.Errorf("fingerprint usage event: %w", err)
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO usage_events(timestamp, runtime, session_id, project_id, capability_type, capability_name, event_type, fingerprint) VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(fingerprint) DO NOTHING`, event.Timestamp.UTC().Format(time.RFC3339Nano), event.Runtime, event.SessionID, event.ProjectID, event.CapabilityType, event.CapabilityName, event.EventType, fingerprint)
		if err != nil {
			return fmt.Errorf("insert usage event: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit usage insert: %w", err)
	}
	return nil
}

// ListUsageEvents returns events ordered by timestamp and fingerprint.
func (s *Store) ListUsageEvents(ctx context.Context, since time.Time) ([]domain.UsageEvent, error) {
	if s.isClosed() {
		return nil, errors.New("store is closed")
	}
	query := `SELECT timestamp, runtime, session_id, project_id, capability_type, capability_name, event_type, fingerprint FROM usage_events`
	args := []any{}
	if !since.IsZero() {
		query += ` WHERE timestamp >= ?`
		args = append(args, since.UTC().Format(time.RFC3339Nano))
	}
	query += ` ORDER BY timestamp, fingerprint`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list usage events: %w", err)
	}
	defer rows.Close()
	var result []domain.UsageEvent
	for rows.Next() {
		var event domain.UsageEvent
		var timestamp string
		if err := rows.Scan(&timestamp, &event.Runtime, &event.SessionID, &event.ProjectID, &event.CapabilityType, &event.CapabilityName, &event.EventType, &event.Fingerprint); err != nil {
			return nil, fmt.Errorf("scan usage event: %w", err)
		}
		event.Timestamp, err = time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return nil, fmt.Errorf("parse usage timestamp: %w", err)
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage events: %w", err)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Timestamp.Before(result[j].Timestamp) || (result[i].Timestamp.Equal(result[j].Timestamp) && result[i].Fingerprint < result[j].Fingerprint)
	})
	return result, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
