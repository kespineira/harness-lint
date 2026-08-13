package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

func TestOpenMigratesAndReopensPersistedDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "harness-lint.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	checkSchema(t, s)
	var version string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM schema_meta WHERE key = 'version'`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != "2" {
		t.Fatalf("schema version = %q, want 2", version)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	if capabilities, err := reopened.ListCapabilities(ctx); err != nil {
		t.Fatalf("ListCapabilities() after reopen: %v", err)
	} else if len(capabilities) != 0 {
		t.Fatalf("empty capabilities = %#v, want no rows", capabilities)
	}
}

func TestOpenRejectsUnsupportedSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harness-lint.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_meta (key TEXT PRIMARY KEY NOT NULL, value TEXT NOT NULL); INSERT INTO schema_meta(key, value) VALUES ('version', '99')`); err != nil {
		t.Fatalf("seed schema metadata: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seeded database: %v", err)
	}

	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("Open() error = %v, want unsupported-version error", err)
	}
}

func TestLoadMigrationsSortsNumberedFilesWithoutSuffixAssumptions(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/002_add_indexes.sql": &fstest.MapFile{Data: []byte("second")},
		"migrations/001_bootstrap.sql":   &fstest.MapFile{Data: []byte("first")},
		"migrations/003-final.sql":       &fstest.MapFile{Data: []byte("third")},
	}

	got, err := loadMigrations(fsys)
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	if len(got) != 3 || got[0].version != 1 || got[1].version != 2 || got[2].version != 3 {
		t.Fatalf("migration versions = %#v, want [1 2 3]", got)
	}
	if string(got[0].sql) != "first" || string(got[1].sql) != "second" || string(got[2].sql) != "third" {
		t.Fatalf("migration order/content = %#v", got)
	}
}

func TestLoadMigrationsRejectsInvalidNumbering(t *testing.T) {
	tests := []struct {
		name string
		fs   fstest.MapFS
		want string
	}{
		{
			name: "gap",
			fs: fstest.MapFS{
				"migrations/001_bootstrap.sql": &fstest.MapFile{Data: []byte("first")},
				"migrations/003_later.sql":     &fstest.MapFile{Data: []byte("third")},
			},
			want: "numbering gap",
		},
		{
			name: "duplicate",
			fs: fstest.MapFS{
				"migrations/001_bootstrap.sql":   &fstest.MapFile{Data: []byte("first")},
				"migrations/001_replacement.sql": &fstest.MapFile{Data: []byte("duplicate")},
			},
			want: "duplicate migration version",
		},
		{
			name: "unnumbered",
			fs: fstest.MapFS{
				"migrations/bootstrap.sql": &fstest.MapFile{Data: []byte("first")},
			},
			want: "must start with a numeric version",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := loadMigrations(test.fs); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("loadMigrations() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestOpenMigratesLegacyCapabilityShape(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	initial, err := migrations.ReadFile("migrations/001_initial.sql")
	if err != nil {
		t.Fatalf("read initial migration: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(initial)); err != nil {
		t.Fatalf("apply initial migration: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_meta(key, value) VALUES ('version', '1')`); err != nil {
		t.Fatalf("seed legacy schema version: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO capabilities(runtime, capability_type, name, scope, source, enabled, hash, context_value, context_confidence, context_basis, input_tokens_value, input_tokens_confidence, input_tokens_basis, output_tokens_value, output_tokens_confidence, output_tokens_basis, first_seen, last_seen) VALUES ('codex', 'mcp', 'filesystem', 'user', '/config/mcp.json', 0, 'legacy-hash', 100, 'exact', 'old context', 20, 'observed', 'old input', 30, 'estimated', 'old output', '2026-08-13T10:00:00Z', '2026-08-13T10:00:00Z')`); err != nil {
		t.Fatalf("seed legacy capability: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO usage_events(timestamp, runtime, session_id, project_id, capability_type, capability_name, event_type, fingerprint) VALUES ('2026-08-13T10:00:00Z', 'codex', 'session-hash', 'project-hash', 'mcp', 'filesystem', 'loaded', 'legacy-event')`); err != nil {
		t.Fatalf("seed legacy usage event: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() migration error = %v", err)
	}
	defer s.Close()
	capabilities, err := s.ListCapabilities(ctx)
	if err != nil {
		t.Fatalf("ListCapabilities() after migration: %v", err)
	}
	if len(capabilities) != 1 {
		t.Fatalf("migrated capability count = %d, want 1", len(capabilities))
	}
	capability := capabilities[0]
	if capability.Type != domain.CapabilityMCPServer || capability.Enabled != domain.EnabledStateDisabled || capability.Source != "/config/mcp.json" {
		t.Fatalf("migrated capability identity/state = %#v", capability)
	}
	if capability.MetadataTokens != (domain.Measurement{Confidence: domain.ConfidenceUnknown}) || capability.BodyTokens != (domain.Measurement{Confidence: domain.ConfidenceUnknown}) {
		t.Fatalf("legacy runtime measurements must not be relabeled as advertised sizes: %#v", capability)
	}
	events, err := s.ListUsageEvents(ctx, time.Time{})
	if err != nil {
		t.Fatalf("ListUsageEvents() after migration: %v", err)
	}
	if len(events) != 1 || events[0].CapabilityType != domain.CapabilityMCPServer {
		t.Fatalf("migrated usage events = %#v", events)
	}
}

func TestCapabilitiesUpsertIsIdempotentAndMergesObservedRange(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)

	first := testCapability("lint", base.Add(time.Hour), base.Add(2*time.Hour))
	if err := s.UpsertCapabilities(ctx, []domain.Capability{first}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	updated := first
	updated.Enabled = domain.EnabledStateDisabled
	updated.Hash = "new-hash"
	updated.MetadataTokens = domain.Measurement{Value: 999, Confidence: domain.ConfidenceExact, Basis: "latest advertised metadata"}
	if err := s.UpsertCapabilities(ctx, []domain.Capability{updated}); err != nil {
		t.Fatalf("idempotent upsert: %v", err)
	}
	earlier := updated
	earlier.FirstSeen = base
	earlier.LastSeen = base.Add(3 * time.Hour)
	if err := s.UpsertCapabilities(ctx, []domain.Capability{earlier}); err != nil {
		t.Fatalf("range merge upsert: %v", err)
	}
	withoutObservation := earlier
	withoutObservation.FirstSeen = time.Time{}
	withoutObservation.LastSeen = time.Time{}
	if err := s.UpsertCapabilities(ctx, []domain.Capability{withoutObservation}); err != nil {
		t.Fatalf("zero timestamp upsert: %v", err)
	}

	capabilities, err := s.ListCapabilities(ctx)
	if err != nil {
		t.Fatalf("ListCapabilities(): %v", err)
	}
	if len(capabilities) != 1 {
		t.Fatalf("capability count = %d, want 1", len(capabilities))
	}
	got := capabilities[0]
	if got.Source != withoutObservation.Source || got.Enabled != withoutObservation.Enabled || got.Hash != withoutObservation.Hash {
		t.Fatalf("latest mutable fields = %#v, want source/enabled/hash from latest upsert", got)
	}
	if got.MetadataTokens != withoutObservation.MetadataTokens || got.BodyTokens != withoutObservation.BodyTokens {
		t.Fatalf("latest advertised measurements = %#v/%#v, want %#v/%#v", got.MetadataTokens, got.BodyTokens, withoutObservation.MetadataTokens, withoutObservation.BodyTokens)
	}
	if !got.FirstSeen.Equal(base) || !got.LastSeen.Equal(base.Add(3*time.Hour)) {
		t.Fatalf("seen range = %s..%s, want %s..%s", got.FirstSeen, got.LastSeen, base, base.Add(3*time.Hour))
	}
}

func TestCapabilitiesWithDifferentSourcesCoexist(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := testCapability("same-name", time.Time{}, time.Time{})
	first := base
	first.Source = "/project/skills/a/same-name"
	second := base
	second.Source = "/project/skills/b/same-name"
	second.Type = domain.CapabilitySkill
	second.Enabled = domain.EnabledStateUnknown

	if err := s.UpsertCapabilities(ctx, []domain.Capability{first, second}); err != nil {
		t.Fatalf("upsert capabilities with duplicate names: %v", err)
	}
	capabilities, err := s.ListCapabilities(ctx)
	if err != nil {
		t.Fatalf("ListCapabilities(): %v", err)
	}
	if len(capabilities) != 2 {
		t.Fatalf("capability count = %d, want 2 for distinct sources: %#v", len(capabilities), capabilities)
	}
	if capabilities[0].Source != first.Source || capabilities[1].Source != second.Source {
		t.Fatalf("capabilities ordered by source = %#v, want %q then %q", capabilities, first.Source, second.Source)
	}
	if capabilities[0].Enabled != first.Enabled || capabilities[1].Enabled != second.Enabled {
		t.Fatalf("enabled states were not preserved: %#v", capabilities)
	}
}

func TestCapabilityEnabledStatesPersistLosslessly(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	states := []domain.EnabledState{
		domain.EnabledStateEnabled,
		domain.EnabledStateDisabled,
		domain.EnabledStateUnknown,
	}
	capabilities := make([]domain.Capability, 0, len(states))
	wantBySource := make(map[string]domain.EnabledState, len(states))
	for _, state := range states {
		name := stateName(state)
		capability := testCapability("state-"+name, time.Time{}, time.Time{})
		capability.Source = "/project/skills/" + name
		capability.Enabled = state
		capabilities = append(capabilities, capability)
		wantBySource[capability.Source] = state
	}
	if err := s.UpsertCapabilities(ctx, capabilities); err != nil {
		t.Fatalf("upsert enabled states: %v", err)
	}
	got, err := s.ListCapabilities(ctx)
	if err != nil {
		t.Fatalf("ListCapabilities(): %v", err)
	}
	if len(got) != len(states) {
		t.Fatalf("capability count = %d, want %d", len(got), len(states))
	}
	for _, capability := range got {
		if !capability.Enabled.Valid() {
			t.Fatalf("invalid persisted enabled state: %#v", capability)
		}
	}
	for _, capability := range got {
		if capability.Enabled != wantBySource[capability.Source] {
			t.Fatalf("persisted enabled state for %q = %q, want %q", capability.Source, capability.Enabled, wantBySource[capability.Source])
		}
	}
}

func stateName(state domain.EnabledState) string {
	return strings.ReplaceAll(string(state), "-", "_")
}

func TestCapabilityUpsertRollsBackOnValidationError(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	valid := testCapability("valid", time.Time{}, time.Time{})
	invalid := valid
	invalid.Name = " "

	if err := s.UpsertCapabilities(ctx, []domain.Capability{valid, invalid}); err == nil {
		t.Fatal("invalid capability accepted")
	}
	capabilities, err := s.ListCapabilities(ctx)
	if err != nil {
		t.Fatalf("ListCapabilities(): %v", err)
	}
	if len(capabilities) != 0 {
		t.Fatalf("capabilities after failed transaction = %#v, want empty", capabilities)
	}
}

func TestUsageInsertIsIdempotentUTCAndDeterministicallyOrdered(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Date(2026, 8, 13, 10, 0, 0, 123, time.UTC)
	twoHoursLaterCEST := base.Add(2 * time.Hour).In(time.FixedZone("CEST", 2*60*60))
	first := testUsageEvent(twoHoursLaterCEST, "terminal", domain.EventInvoked)
	second := testUsageEvent(base.Add(time.Hour), "filesystem", domain.EventLoaded)
	duplicate := first
	duplicate.Timestamp = first.Timestamp.UTC()

	if err := s.InsertUsageEvents(ctx, []domain.UsageEvent{first, second, duplicate}); err != nil {
		t.Fatalf("InsertUsageEvents(): %v", err)
	}
	events, err := s.ListUsageEvents(ctx, time.Time{})
	if err != nil {
		t.Fatalf("ListUsageEvents(): %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
	if !events[0].Timestamp.Equal(second.Timestamp) || events[0].CapabilityName != second.CapabilityName {
		t.Fatalf("first event = %#v, want %q at %s", events[0], second.CapabilityName, second.Timestamp)
	}
	if !events[1].Timestamp.Equal(first.Timestamp) || events[1].CapabilityName != first.CapabilityName {
		t.Fatalf("second event = %#v, want %q at %s", events[1], first.CapabilityName, first.Timestamp)
	}
	if events[0].Timestamp.Location() != time.UTC || events[1].Timestamp.Location() != time.UTC {
		t.Fatalf("timestamps must be UTC: %v, %v", events[0].Timestamp.Location(), events[1].Timestamp.Location())
	}
	if events[0].Fingerprint == "" || events[1].Fingerprint == "" || events[0].Fingerprint == events[1].Fingerprint {
		t.Fatalf("fingerprints = %q and %q, want distinct non-empty values", events[0].Fingerprint, events[1].Fingerprint)
	}

	filtered, err := s.ListUsageEvents(ctx, first.Timestamp)
	if err != nil {
		t.Fatalf("ListUsageEvents(since): %v", err)
	}
	if len(filtered) != 1 || filtered[0].CapabilityName != first.CapabilityName {
		t.Fatalf("filtered events = %#v, want only %q", filtered, first.CapabilityName)
	}
}

func TestUsageInsertRollsBackOnValidationError(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	valid := testUsageEvent(time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC), "terminal", domain.EventInvoked)
	invalid := valid
	invalid.EventType = domain.EventType("installed")

	if err := s.InsertUsageEvents(ctx, []domain.UsageEvent{valid, invalid}); err == nil {
		t.Fatal("invalid usage event accepted")
	}
	events, err := s.ListUsageEvents(ctx, time.Time{})
	if err != nil {
		t.Fatalf("ListUsageEvents(): %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events after failed transaction = %#v, want empty", events)
	}
}

func TestStoreClosePropagatesClosedState(t *testing.T) {
	s := openTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close(): %v", err)
	}
	if _, err := s.ListCapabilities(context.Background()); err == nil {
		t.Fatal("ListCapabilities() on closed store succeeded")
	}
}

func TestStoreDoesNotPersistConversationOrToolPayloadColumns(t *testing.T) {
	s := openTestStore(t)
	for _, table := range []string{"capabilities", "usage_events"} {
		rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatalf("table_info(%s): %v", table, err)
		}
		var columns []string
		for rows.Next() {
			var cid int
			var name, columnType string
			var notNull, primaryKey int
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				t.Fatalf("scan table_info(%s): %v", table, err)
			}
			columns = append(columns, name)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close table_info(%s): %v", table, err)
		}
		for _, column := range columns {
			if strings.Contains(strings.ToLower(column), "prompt") || strings.Contains(strings.ToLower(column), "response") || strings.Contains(strings.ToLower(column), "conversation") || strings.Contains(strings.ToLower(column), "tool_args") || strings.Contains(strings.ToLower(column), "model_output") {
				t.Fatalf("table %s contains prohibited payload column %q", table, column)
			}
		}
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close test store: %v", err)
		}
	})
	return s
}

func testCapability(name string, firstSeen, lastSeen time.Time) domain.Capability {
	return domain.Capability{
		Runtime:        domain.RuntimeCodex,
		Type:           domain.CapabilitySkill,
		Name:           name,
		Scope:          domain.ScopeProject,
		Source:         "test-source",
		Enabled:        domain.EnabledStateEnabled,
		Hash:           "test-hash",
		MetadataTokens: domain.Measurement{Value: 20, Confidence: domain.ConfidenceExact, Basis: "advertised metadata"},
		BodyTokens:     domain.Measurement{Value: 30, Confidence: domain.ConfidenceEstimated, Basis: "advertised body"},
		FirstSeen:      firstSeen,
		LastSeen:       lastSeen,
	}
}

func testUsageEvent(timestamp time.Time, capabilityName string, eventType domain.EventType) domain.UsageEvent {
	return domain.UsageEvent{
		Timestamp:      timestamp,
		Runtime:        domain.RuntimeCodex,
		SessionID:      "session-hash",
		ProjectID:      "project-hash",
		CapabilityType: domain.CapabilityTool,
		CapabilityName: capabilityName,
		EventType:      eventType,
	}
}

func checkSchema(t *testing.T, s *Store) {
	t.Helper()
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatalf("list schema tables: %v", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan schema table: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate schema tables: %v", err)
	}
	want := []string{"capabilities", "schema_meta", "usage_events"}
	if !reflect.DeepEqual(tables, want) {
		sort.Strings(tables)
		t.Fatalf("schema tables = %v, want %v", tables, want)
	}
}
