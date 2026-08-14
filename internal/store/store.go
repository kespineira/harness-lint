// Package store provides the daemonless SQLite persistence layer for the
// runtime-neutral domain. SessionID and ProjectID are intentionally opaque;
// the usage-event write boundary normalizes them before persistence.
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

// SchemaStatus describes the database schema version and the newest schema
// version supported by this binary. It intentionally contains no database
// handle so callers can use it for health checks without reaching into the
// store implementation.
type SchemaStatus struct {
	Current int
	Latest  int
}

// ErrMalformedSchemaVersion identifies a schema metadata value that cannot be
// parsed as a non-negative integer. Callers can use errors.Is while retaining
// the actionable parse detail carried by the wrapped error.
var ErrMalformedSchemaVersion = errors.New("malformed schema version")

const sqliteBusyTimeoutMilliseconds = 5000

// Store is a concurrency-safe database handle. SQLite serializes writes and
// each public write operation uses one transaction.
type Store struct {
	db     *sql.DB
	path   string
	closed atomic.Bool
}

// Open opens path and applies forward-only embedded migrations. Use ":memory:"
// for a transient store in tests; callers own the path and identifiers passed
// to this package.
func Open(path string) (*Store, error) {
	return openWithMigrationFS(path, migrations)
}

func openWithMigrationFS(path string, migrationFS fs.FS) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("store path is required")
	}
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, path: path}
	if err := s.migrateWithFS(context.Background(), migrationFS); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func sqliteDSN(path string) string {
	separator := "?"
	if strings.ContainsRune(path, '?') {
		separator = "&"
	}
	return path + separator + "_pragma=busy_timeout%28" + strconv.Itoa(sqliteBusyTimeoutMilliseconds) + "%29"
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
	return s.migrateWithFS(ctx, migrations)
}

func (s *Store) migrateWithFS(ctx context.Context, migrationFS fs.FS) error {
	available, err := loadMigrations(migrationFS)
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
	current, err := readSchemaVersion(ctx, tx)
	if err != nil {
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

// SchemaStatus reports the current database schema version and the latest
// version supported by the embedded migration sequence.
func (s *Store) SchemaStatus(ctx context.Context) (SchemaStatus, error) {
	if s.isClosed() {
		return SchemaStatus{}, errors.New("store is closed")
	}
	available, err := loadMigrations(migrations)
	if err != nil {
		return SchemaStatus{}, fmt.Errorf("load migrations: %w", err)
	}
	current, err := readSchemaVersion(ctx, s.db)
	if err != nil {
		return SchemaStatus{}, fmt.Errorf("read schema version: %w", err)
	}
	return SchemaStatus{Current: current, Latest: available[len(available)-1].version}, nil
}

type schemaVersionReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readSchemaVersion(ctx context.Context, reader schemaVersionReader) (int, error) {
	var raw string
	err := reader.QueryRowContext(ctx, `SELECT value FROM schema_meta WHERE key = 'version'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return parseSchemaVersion(raw)
}

func parseSchemaVersion(raw string) (int, error) {
	if raw == "" {
		return 0, fmt.Errorf("%w: schema version is empty", ErrMalformedSchemaVersion)
	}
	for _, char := range raw {
		if char < '0' || char > '9' {
			return 0, fmt.Errorf("%w: schema version %q is not a non-negative integer", ErrMalformedSchemaVersion, raw)
		}
	}
	version, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: parse schema version %q: %v", ErrMalformedSchemaVersion, raw, err)
	}
	return version, nil
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

const capabilityUpsertQuery = `INSERT INTO capabilities (
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

const capabilityColumns = `runtime, capability_type, name, scope, source, enabled_state, advertisement_state, hash, metadata_tokens_value, metadata_tokens_confidence, metadata_tokens_basis, body_tokens_value, body_tokens_confidence, body_tokens_basis, first_seen, last_seen`

const capabilityOrder = ` ORDER BY runtime, capability_type, name, scope, source`

const currentCapabilityOrder = ` ORDER BY c.runtime, c.capability_type, c.name, c.scope, c.source`

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
	if err := upsertCapabilitiesTx(ctx, tx, capabilities); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit capability upsert: %w", err)
	}
	return nil
}

func upsertCapabilitiesTx(ctx context.Context, tx *sql.Tx, capabilities []domain.Capability) error {
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
		if _, err := tx.ExecContext(ctx, capabilityUpsertQuery, capability.Runtime, capability.Type, capability.Name, capability.Scope, capability.Source, capability.Enabled,
			capability.Advertisement, capability.Hash, metadata.Value, metadata.Confidence, metadata.Basis, body.Value, body.Confidence, body.Basis, firstSeen, lastSeen); err != nil {
			return fmt.Errorf("upsert capability %q: %w", capability.Name, err)
		}
	}
	return nil
}

// RecordInventory atomically records a successful inventory observation and
// the capabilities present in that scan. Empty capabilities are a successful
// empty scan; existing rows remain historical and are never deleted.
func (s *Store) RecordInventory(ctx context.Context, runtime domain.Runtime, observedAt time.Time, capabilities []domain.Capability) error {
	if s.isClosed() {
		return errors.New("store is closed")
	}
	if !runtime.Valid() {
		return fmt.Errorf("invalid inventory runtime %q", runtime)
	}
	if observedAt.IsZero() {
		return errors.New("inventory observation time is required")
	}
	observedAt = observedAt.UTC()
	normalized := make([]domain.Capability, len(capabilities))
	for i, capability := range capabilities {
		if capability.Runtime != runtime {
			return fmt.Errorf("capability %q runtime %q does not match inventory runtime %q", capability.Name, capability.Runtime, runtime)
		}
		if err := capability.Validate(); err != nil {
			return fmt.Errorf("validate inventory capability %q: %w", capability.Name, err)
		}
		if capability.FirstSeen.IsZero() {
			capability.FirstSeen = observedAt
		}
		capability.LastSeen = observedAt
		if err := capability.Validate(); err != nil {
			return fmt.Errorf("validate inventory capability %q: %w", capability.Name, err)
		}
		normalized[i] = capability
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin inventory record: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var previousMarker string
	err = tx.QueryRowContext(ctx, `SELECT observed_at FROM inventory_scans WHERE runtime = ?`, runtime).Scan(&previousMarker)
	if errors.Is(err, sql.ErrNoRows) {
		previousMarker = ""
	} else if err != nil {
		return fmt.Errorf("read latest inventory scan: %w", err)
	} else {
		previousAt, parseErr := time.Parse(time.RFC3339Nano, previousMarker)
		if parseErr != nil {
			return fmt.Errorf("parse latest inventory scan: %w", parseErr)
		}
		if observedAt.Before(previousAt) {
			return fmt.Errorf("inventory observation time %s is older than latest recorded scan %s", observedAt.Format(time.RFC3339Nano), previousMarker)
		}
	}
	if err := upsertCapabilitiesTx(ctx, tx, normalized); err != nil {
		return fmt.Errorf("record inventory capabilities: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM current_inventory WHERE runtime = ?`, runtime); err != nil {
		return fmt.Errorf("replace current inventory: %w", err)
	}
	for _, capability := range normalized {
		if _, err := tx.ExecContext(ctx, `INSERT INTO current_inventory(runtime, capability_type, name, scope, source) VALUES (?, ?, ?, ?, ?) ON CONFLICT(runtime, capability_type, name, scope, source) DO NOTHING`, capability.Runtime, capability.Type, capability.Name, capability.Scope, capability.Source); err != nil {
			return fmt.Errorf("record current capability %q: %w", capability.Name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO inventory_scans(runtime, observed_at) VALUES (?, ?) ON CONFLICT(runtime) DO UPDATE SET observed_at = excluded.observed_at`, runtime, observedAt.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("record inventory scan: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit inventory record: %w", err)
	}
	return nil
}

// ListCapabilities returns all inventory in deterministic natural-key order.
func (s *Store) ListCapabilities(ctx context.Context) ([]domain.Capability, error) {
	if s.isClosed() {
		return nil, errors.New("store is closed")
	}
	return s.listCapabilities(ctx, `SELECT `+capabilityColumns+` FROM capabilities`+capabilityOrder)
}

// ListCurrentCapabilities returns only capabilities observed in the latest
// successful inventory scan for runtime, in deterministic natural-key order.
func (s *Store) ListCurrentCapabilities(ctx context.Context, runtime domain.Runtime) ([]domain.Capability, error) {
	if s.isClosed() {
		return nil, errors.New("store is closed")
	}
	if !runtime.Valid() {
		return nil, fmt.Errorf("invalid inventory runtime %q", runtime)
	}
	return s.listCapabilities(ctx, `SELECT c.runtime, c.capability_type, c.name, c.scope, c.source, c.enabled_state, c.advertisement_state, c.hash, c.metadata_tokens_value, c.metadata_tokens_confidence, c.metadata_tokens_basis, c.body_tokens_value, c.body_tokens_confidence, c.body_tokens_basis, c.first_seen, c.last_seen FROM current_inventory AS current INNER JOIN capabilities AS c ON c.runtime = current.runtime AND c.capability_type = current.capability_type AND c.name = current.name AND c.scope = current.scope AND c.source = current.source WHERE current.runtime = ?`+currentCapabilityOrder, runtime)
}

func (s *Store) listCapabilities(ctx context.Context, query string, args ...any) ([]domain.Capability, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
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

// InsertUsageEvents idempotently inserts metadata-only events by their safe
// fingerprint. Stable source identities deduplicate direct delivery retries;
// events without one retain observation/source timing in their conservative
// fallback fingerprint. Every batch is one small SQLite transaction.
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
		if _, err := insertUsageEventTx(ctx, tx, event); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit usage insert: %w", err)
	}
	return nil
}

// insertUsageEventTx writes one canonical event and its normalized evidence
// relation. It intentionally does not update capture delivery health;
// transcript/import backfill uses this helper through InsertUsageEvents.
func insertUsageEventTx(ctx context.Context, tx *sql.Tx, event domain.UsageEvent) (domain.UsageEvent, error) {
	normalized, err := domain.NormalizeUsageEvent(event)
	if err != nil {
		return domain.UsageEvent{}, fmt.Errorf("validate usage event: %w", err)
	}
	fingerprint, err := domain.FingerprintForUsageEvent(normalized)
	if err != nil {
		return domain.UsageEvent{}, fmt.Errorf("fingerprint usage event: %w", err)
	}
	normalized.Fingerprint = fingerprint
	var sourceTimestamp any
	if normalized.SourceTimestamp != nil {
		sourceTimestamp = normalized.SourceTimestamp.Format(time.RFC3339Nano)
	}
	// timestamp is the legacy v4 column. Keep it populated with effective
	// activity time for old readers while the explicit fields below remain
	// authoritative for the current contract.
	_, err = tx.ExecContext(ctx, `INSERT INTO usage_events(timestamp, observed_at, source_timestamp, provenance, schema_version, invocation_origin, source_identity, runtime, session_id, project_id, capability_type, capability_name, event_type, fingerprint) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(fingerprint) DO NOTHING`, normalized.EffectiveActivityTime().Format(time.RFC3339Nano), normalized.ObservedAt.Format(time.RFC3339Nano), sourceTimestamp, normalized.Provenance, normalized.SchemaVersion, normalized.InvocationOrigin, normalized.SourceIdentity, normalized.Runtime, normalized.SessionID, normalized.ProjectID, normalized.CapabilityType, normalized.CapabilityName, normalized.EventType, fingerprint)
	if err != nil {
		return domain.UsageEvent{}, fmt.Errorf("insert usage event: %w", err)
	}

	// A stable source identity proves that a retry or a second evidence path
	// refers to the same invocation. Only then may optional source timestamp or
	// invocation-origin fields enrich the canonical row. observed_at is never
	// updated, so local receipt time remains authoritative for that field.
	_, err = tx.ExecContext(ctx, `UPDATE usage_events
	SET source_timestamp = CASE
		WHEN source_identity <> '' AND source_identity = ? AND source_timestamp IS NULL AND ? IS NOT NULL THEN ?
		ELSE source_timestamp
	END,
	invocation_origin = CASE
		WHEN source_identity <> '' AND source_identity = ? AND invocation_origin = 'unknown' AND ? <> 'unknown' THEN ?
		ELSE invocation_origin
	END
	WHERE fingerprint = ? AND source_identity <> '' AND source_identity = ?`, normalized.SourceIdentity, sourceTimestamp, sourceTimestamp, normalized.SourceIdentity, normalized.InvocationOrigin, normalized.InvocationOrigin, fingerprint, normalized.SourceIdentity)
	if err != nil {
		return domain.UsageEvent{}, fmt.Errorf("enrich usage event identity fields: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO usage_event_evidence(
	fingerprint, provenance, observed_at, source_timestamp, invocation_origin, source_identity
	) VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT(fingerprint, provenance) DO UPDATE SET
	observed_at = CASE WHEN excluded.observed_at < usage_event_evidence.observed_at THEN excluded.observed_at ELSE usage_event_evidence.observed_at END,
	source_timestamp = COALESCE(usage_event_evidence.source_timestamp, excluded.source_timestamp),
	invocation_origin = CASE WHEN usage_event_evidence.invocation_origin = 'unknown' AND excluded.invocation_origin <> 'unknown' THEN excluded.invocation_origin ELSE usage_event_evidence.invocation_origin END,
	source_identity = CASE WHEN usage_event_evidence.source_identity = '' THEN excluded.source_identity ELSE usage_event_evidence.source_identity END`, fingerprint, normalized.Provenance, normalized.ObservedAt.Format(time.RFC3339Nano), sourceTimestamp, normalized.InvocationOrigin, normalized.SourceIdentity)
	if err != nil {
		return domain.UsageEvent{}, fmt.Errorf("record usage evidence: %w", err)
	}
	return normalized, nil
}

// ListUsageEvents returns events ordered by effective activity time and
// fingerprint. since applies to source occurrence time when available and to
// observed/import time otherwise.
func (s *Store) ListUsageEvents(ctx context.Context, since time.Time) ([]domain.UsageEvent, error) {
	if s.isClosed() {
		return nil, errors.New("store is closed")
	}
	query := `SELECT observed_at, source_timestamp, provenance, schema_version, invocation_origin, source_identity, runtime, session_id, project_id, capability_type, capability_name, event_type, fingerprint FROM usage_events`
	args := []any{}
	if !since.IsZero() {
		query += ` WHERE COALESCE(source_timestamp, observed_at) >= ?`
		args = append(args, since.UTC().Format(time.RFC3339Nano))
	}
	query += ` ORDER BY COALESCE(source_timestamp, observed_at), fingerprint`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list usage events: %w", err)
	}
	defer rows.Close()
	var result []domain.UsageEvent
	for rows.Next() {
		var event domain.UsageEvent
		var observedAt, sourceTimestamp sql.NullString
		if err := rows.Scan(&observedAt, &sourceTimestamp, &event.Provenance, &event.SchemaVersion, &event.InvocationOrigin, &event.SourceIdentity, &event.Runtime, &event.SessionID, &event.ProjectID, &event.CapabilityType, &event.CapabilityName, &event.EventType, &event.Fingerprint); err != nil {
			return nil, fmt.Errorf("scan usage event: %w", err)
		}
		if !observedAt.Valid || observedAt.String == "" {
			return nil, errors.New("usage event observed_at is empty")
		}
		event.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse usage observed_at: %w", err)
		}
		if sourceTimestamp.Valid && sourceTimestamp.String != "" {
			parsed, parseErr := time.Parse(time.RFC3339Nano, sourceTimestamp.String)
			if parseErr != nil {
				return nil, fmt.Errorf("parse usage source_timestamp: %w", parseErr)
			}
			event.SourceTimestamp = &parsed
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage events: %w", err)
	}
	sort.SliceStable(result, func(i, j int) bool {
		left, right := result[i].EffectiveActivityTime(), result[j].EffectiveActivityTime()
		return left.Before(right) || (left.Equal(right) && result[i].Fingerprint < result[j].Fingerprint)
	})
	return result, nil
}
