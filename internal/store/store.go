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
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

type migration struct {
	version int
	name    string
	sql     []byte
}

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
	available, err := loadMigrations(migrations)
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
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
	latest := available[len(available)-1].version
	if current > latest {
		return fmt.Errorf("database schema version %d is newer than supported version %d", current, latest)
	}
	for _, migration := range available {
		if migration.version <= current {
			continue
		}
		if _, err := tx.ExecContext(ctx, string(migration.sql)); err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", migration.version, migration.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_meta(key, value) VALUES ('version', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, migration.version); err != nil {
			return fmt.Errorf("record migration %d: %w", migration.version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

// loadMigrations discovers numbered SQL files from the embedded migration
// directory. Versions must start at one and be contiguous so a database can
// only move forward through a complete, deterministic sequence.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migration directory: %w", err)
	}
	result := make([]migration, 0, len(entries))
	seen := make(map[int]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("migration entry %q is a directory", entry.Name())
		}
		version, err := migrationVersion(entry.Name())
		if err != nil {
			return nil, err
		}
		if previous, ok := seen[version]; ok {
			return nil, fmt.Errorf("duplicate migration version %d in %q and %q", version, previous, entry.Name())
		}
		contents, err := fs.ReadFile(fsys, "migrations/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		seen[version] = entry.Name()
		result = append(result, migration{version: version, name: entry.Name(), sql: contents})
	}
	if len(result) == 0 {
		return nil, errors.New("no migrations found")
	}
	sort.Slice(result, func(i, j int) bool { return result[i].version < result[j].version })
	for i, migration := range result {
		expected := i + 1
		if migration.version != expected {
			return nil, fmt.Errorf("migration numbering gap: expected version %d, found %d (%s)", expected, migration.version, migration.name)
		}
	}
	return result, nil
}

func migrationVersion(name string) (int, error) {
	if !strings.HasSuffix(name, ".sql") {
		return 0, fmt.Errorf("migration %q must use the .sql suffix", name)
	}
	stem := strings.TrimSuffix(name, ".sql")
	digitsEnd := 0
	for digitsEnd < len(stem) && stem[digitsEnd] >= '0' && stem[digitsEnd] <= '9' {
		digitsEnd++
	}
	if digitsEnd == 0 || stem == "" {
		return 0, fmt.Errorf("migration %q must start with a numeric version", name)
	}
	version, err := strconv.Atoi(stem[:digitsEnd])
	if err != nil {
		return 0, fmt.Errorf("parse migration version %q: %w", name, err)
	}
	if version < 1 {
		return 0, fmt.Errorf("migration %q must have a positive version", name)
	}
	return version, nil
}

// UpsertCapabilities idempotently writes an inventory snapshot. The natural
// key is runtime/type/name/scope/source; observations are refreshed while
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
	runtime, capability_type, name, scope, source, enabled_state, advertisement_state, hash,
	metadata_tokens_value, metadata_tokens_confidence, metadata_tokens_basis,
	body_tokens_value, body_tokens_confidence, body_tokens_basis, first_seen, last_seen
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(runtime, capability_type, name, scope, source) DO UPDATE SET
enabled_state = excluded.enabled_state, advertisement_state = excluded.advertisement_state, hash = excluded.hash,
metadata_tokens_value = excluded.metadata_tokens_value, metadata_tokens_confidence = excluded.metadata_tokens_confidence, metadata_tokens_basis = excluded.metadata_tokens_basis,
body_tokens_value = excluded.body_tokens_value, body_tokens_confidence = excluded.body_tokens_confidence, body_tokens_basis = excluded.body_tokens_basis,
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
		metadata := capability.MetadataTokens
		body := capability.BodyTokens
		if _, err := tx.ExecContext(ctx, query, capability.Runtime, capability.Type, capability.Name, capability.Scope, capability.Source, capability.Enabled,
			capability.Advertisement, capability.Hash, metadata.Value, metadata.Confidence, metadata.Basis, body.Value, body.Confidence, body.Basis, firstSeen, lastSeen); err != nil {
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
	rows, err := s.db.QueryContext(ctx, `SELECT runtime, capability_type, name, scope, source, enabled_state, advertisement_state, hash, metadata_tokens_value, metadata_tokens_confidence, metadata_tokens_basis, body_tokens_value, body_tokens_confidence, body_tokens_basis, first_seen, last_seen FROM capabilities ORDER BY runtime, capability_type, name, scope, source`)
	if err != nil {
		return nil, fmt.Errorf("list capabilities: %w", err)
	}
	defer rows.Close()
	var result []domain.Capability
	for rows.Next() {
		var c domain.Capability
		var firstSeen, lastSeen string
		if err := rows.Scan(&c.Runtime, &c.Type, &c.Name, &c.Scope, &c.Source, &c.Enabled, &c.Advertisement, &c.Hash, &c.MetadataTokens.Value, &c.MetadataTokens.Confidence, &c.MetadataTokens.Basis, &c.BodyTokens.Value, &c.BodyTokens.Confidence, &c.BodyTokens.Basis, &firstSeen, &lastSeen); err != nil {
			return nil, fmt.Errorf("scan capability: %w", err)
		}
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
