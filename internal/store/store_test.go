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
	if version != "4" {
		t.Fatalf("schema version = %q, want 4", version)
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

func TestOpenMigratesV2CapabilityAdvertisementToUnknownWithoutDataLoss(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v2.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	for _, name := range []string{"001_initial.sql", "002_capability_corrections.sql"} {
		migration, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := db.ExecContext(ctx, string(migration)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}
	firstSeen := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	lastSeen := firstSeen.Add(time.Hour)
	if _, err := db.ExecContext(ctx, `INSERT INTO capabilities(runtime, capability_type, name, scope, source, enabled_state, hash, metadata_tokens_value, metadata_tokens_confidence, metadata_tokens_basis, body_tokens_value, body_tokens_confidence, body_tokens_basis, first_seen, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"codex", "skill", "lint", "project", "/v2/source", "disabled", "v2-hash", 42, "observed", "v2 metadata", 84, "exact", "v2 body", firstSeen.Format(time.RFC3339Nano), lastSeen.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed v2 capability: %v", err)
	}
	usageTimestamp := firstSeen.Add(30 * time.Minute)
	if _, err := db.ExecContext(ctx, `INSERT INTO usage_events(timestamp, runtime, session_id, project_id, capability_type, capability_name, event_type, fingerprint) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		usageTimestamp.Format(time.RFC3339Nano), "codex", "v2-session", "v2-project", "tool", "terminal", "invoked", "v2-event"); err != nil {
		t.Fatalf("seed v2 usage event: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_meta(key, value) VALUES ('version', '2')`); err != nil {
		t.Fatalf("seed v2 schema version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close v2 database: %v", err)
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
	wantCapability := domain.Capability{
		Runtime:       domain.RuntimeCodex,
		Type:          domain.CapabilitySkill,
		Name:          "lint",
		Scope:         domain.ScopeProject,
		Source:        "/v2/source",
		Enabled:       domain.EnabledStateDisabled,
		Advertisement: domain.AdvertisementStateUnknown,
		Hash:          "v2-hash",
		MetadataTokens: domain.Measurement{
			Value:      42,
			Confidence: domain.ConfidenceObserved,
			Basis:      "v2 metadata",
		},
		BodyTokens: domain.Measurement{
			Value:      84,
			Confidence: domain.ConfidenceExact,
			Basis:      "v2 body",
		},
		FirstSeen: firstSeen,
		LastSeen:  lastSeen,
	}
	if len(capabilities) != 1 || !reflect.DeepEqual(capabilities[0], wantCapability) {
		t.Fatalf("migrated capabilities = %#v, want %#v", capabilities, []domain.Capability{wantCapability})
	}
	events, err := s.ListUsageEvents(ctx, time.Time{})
	if err != nil {
		t.Fatalf("ListUsageEvents() after migration: %v", err)
	}
	wantEvent := domain.UsageEvent{
		Timestamp:      usageTimestamp,
		Runtime:        domain.RuntimeCodex,
		SessionID:      "v2-session",
		ProjectID:      "v2-project",
		CapabilityType: domain.CapabilityTool,
		CapabilityName: "terminal",
		EventType:      domain.EventInvoked,
		Fingerprint:    "v2-event",
	}
	if len(events) != 1 || !reflect.DeepEqual(events[0], wantEvent) {
		t.Fatalf("migrated usage events = %#v, want %#v", events, []domain.UsageEvent{wantEvent})
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
	updated.Advertisement = domain.AdvertisementStateNameOnly
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
	if got.Source != withoutObservation.Source || got.Enabled != withoutObservation.Enabled || got.Advertisement != withoutObservation.Advertisement || got.Hash != withoutObservation.Hash {
		t.Fatalf("latest mutable fields = %#v, want source/enabled/advertisement/hash from latest upsert", got)
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

func TestRecordInventoryTracksCurrentAndHistoricalRows(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	firstObserved := time.Date(2026, 8, 13, 12, 0, 0, 123, time.FixedZone("CEST", 2*60*60))
	laterObserved := firstObserved.Add(time.Hour)
	latestObserved := laterObserved.Add(time.Hour)
	first := testCapability("first", time.Time{}, time.Time{})
	first.Source = "/scan/first"
	second := testCapability("second", time.Time{}, time.Time{})
	second.Source = "/scan/second"
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, firstObserved, []domain.Capability{first, second}); err != nil {
		t.Fatalf("first inventory scan: %v", err)
	}
	current, err := s.ListCurrentCapabilities(ctx, domain.RuntimeCodex)
	if err != nil {
		t.Fatalf("ListCurrentCapabilities() after first scan: %v", err)
	}
	if len(current) != 2 || current[0].Name != "first" || current[1].Name != "second" {
		t.Fatalf("first current capabilities = %#v, want first then second", current)
	}
	for _, capability := range current {
		if !capability.FirstSeen.Equal(firstObserved.UTC()) || !capability.LastSeen.Equal(firstObserved.UTC()) {
			t.Fatalf("first scan timestamps for %q = %s..%s, want %s", capability.Name, capability.FirstSeen, capability.LastSeen, firstObserved.UTC())
		}
	}

	if err := s.RecordInventory(ctx, domain.RuntimeCodex, laterObserved, []domain.Capability{first}); err != nil {
		t.Fatalf("second inventory scan: %v", err)
	}
	historical, err := s.ListCapabilities(ctx)
	if err != nil {
		t.Fatalf("ListCapabilities() after second scan: %v", err)
	}
	if len(historical) != 2 {
		t.Fatalf("historical capabilities after missing row = %#v, want two rows", historical)
	}
	current, err = s.ListCurrentCapabilities(ctx, domain.RuntimeCodex)
	if err != nil {
		t.Fatalf("ListCurrentCapabilities() after second scan: %v", err)
	}
	if len(current) != 1 || current[0].Name != "first" || !current[0].LastSeen.Equal(laterObserved.UTC()) {
		t.Fatalf("second current capabilities = %#v, want first at %s", current, laterObserved.UTC())
	}

	if err := s.RecordInventory(ctx, domain.RuntimeCodex, latestObserved, nil); err != nil {
		t.Fatalf("empty inventory scan: %v", err)
	}
	historical, err = s.ListCapabilities(ctx)
	if err != nil {
		t.Fatalf("ListCapabilities() after empty scan: %v", err)
	}
	if len(historical) != 2 {
		t.Fatalf("historical capabilities after empty scan = %#v, want two rows", historical)
	}
	current, err = s.ListCurrentCapabilities(ctx, domain.RuntimeCodex)
	if err != nil {
		t.Fatalf("ListCurrentCapabilities() after empty scan: %v", err)
	}
	if len(current) != 0 {
		t.Fatalf("current capabilities after empty scan = %#v, want empty", current)
	}
}

func TestRecordInventorySameTimestampEmptyScanReplacesCurrentMembership(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	observedAt := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	capability := testCapability("same-timestamp", time.Time{}, time.Time{})
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, observedAt, []domain.Capability{capability}); err != nil {
		t.Fatalf("non-empty inventory scan: %v", err)
	}
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, observedAt, nil); err != nil {
		t.Fatalf("same-timestamp empty inventory scan: %v", err)
	}
	current, err := s.ListCurrentCapabilities(ctx, domain.RuntimeCodex)
	if err != nil {
		t.Fatalf("ListCurrentCapabilities() after same-timestamp empty scan: %v", err)
	}
	if len(current) != 0 {
		t.Fatalf("current capabilities after same-timestamp empty scan = %#v, want empty", current)
	}
	historical, err := s.ListCapabilities(ctx)
	if err != nil {
		t.Fatalf("ListCapabilities() after same-timestamp empty scan: %v", err)
	}
	if len(historical) != 1 || historical[0].Name != capability.Name {
		t.Fatalf("historical capabilities after same-timestamp empty scan = %#v, want preserved row", historical)
	}
}

func TestRecordInventoryRejectsOlderOutOfOrderScanWithoutRollback(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	later := time.Date(2026, 8, 13, 13, 0, 0, 0, time.UTC)
	earlier := later.Add(-time.Hour)
	existing := testCapability("existing", time.Time{}, time.Time{})
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, later, []domain.Capability{existing}); err != nil {
		t.Fatalf("latest inventory scan: %v", err)
	}
	older := testCapability("older", time.Time{}, time.Time{})
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, earlier, []domain.Capability{older}); err == nil {
		t.Fatal("older out-of-order inventory scan accepted")
	}
	current, err := s.ListCurrentCapabilities(ctx, domain.RuntimeCodex)
	if err != nil {
		t.Fatalf("ListCurrentCapabilities() after rejected older scan: %v", err)
	}
	if len(current) != 1 || current[0].Name != existing.Name || !current[0].LastSeen.Equal(later) {
		t.Fatalf("current capabilities after rejected older scan = %#v, want existing row at %s", current, later)
	}
	historical, err := s.ListCapabilities(ctx)
	if err != nil {
		t.Fatalf("ListCapabilities() after rejected older scan: %v", err)
	}
	if len(historical) != 1 || historical[0].Name != existing.Name {
		t.Fatalf("historical capabilities after rejected older scan = %#v, want only existing row", historical)
	}
}

func TestRecordInventoryMarkersAreIndependentPerRuntime(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	codexObserved := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	cursorObserved := codexObserved.Add(time.Hour)
	codex := testCapability("codex-tool", time.Time{}, time.Time{})
	codex.Source = "/codex/tool"
	cursor := testCapability("cursor-tool", time.Time{}, time.Time{})
	cursor.Runtime = domain.RuntimeCursor
	cursor.Source = "/cursor/tool"
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, codexObserved, []domain.Capability{codex}); err != nil {
		t.Fatalf("codex inventory scan: %v", err)
	}
	if err := s.RecordInventory(ctx, domain.RuntimeCursor, cursorObserved, []domain.Capability{cursor}); err != nil {
		t.Fatalf("cursor inventory scan: %v", err)
	}
	codexCurrent, err := s.ListCurrentCapabilities(ctx, domain.RuntimeCodex)
	if err != nil {
		t.Fatalf("ListCurrentCapabilities(codex): %v", err)
	}
	cursorCurrent, err := s.ListCurrentCapabilities(ctx, domain.RuntimeCursor)
	if err != nil {
		t.Fatalf("ListCurrentCapabilities(cursor): %v", err)
	}
	if len(codexCurrent) != 1 || codexCurrent[0].Name != codex.Name || !codexCurrent[0].LastSeen.Equal(codexObserved) {
		t.Fatalf("codex current capabilities = %#v, want codex row at %s", codexCurrent, codexObserved)
	}
	if len(cursorCurrent) != 1 || cursorCurrent[0].Name != cursor.Name || !cursorCurrent[0].LastSeen.Equal(cursorObserved) {
		t.Fatalf("cursor current capabilities = %#v, want cursor row at %s", cursorCurrent, cursorObserved)
	}

	if err := s.RecordInventory(ctx, domain.RuntimeCodex, cursorObserved.Add(time.Hour), nil); err != nil {
		t.Fatalf("empty codex inventory scan: %v", err)
	}
	codexCurrent, err = s.ListCurrentCapabilities(ctx, domain.RuntimeCodex)
	if err != nil {
		t.Fatalf("ListCurrentCapabilities(codex) after empty scan: %v", err)
	}
	cursorCurrent, err = s.ListCurrentCapabilities(ctx, domain.RuntimeCursor)
	if err != nil {
		t.Fatalf("ListCurrentCapabilities(cursor) after codex scan: %v", err)
	}
	if len(codexCurrent) != 0 {
		t.Fatalf("codex current capabilities after empty scan = %#v, want empty", codexCurrent)
	}
	if len(cursorCurrent) != 1 || cursorCurrent[0].Name != cursor.Name {
		t.Fatalf("cursor current capabilities after codex scan = %#v, want cursor row", cursorCurrent)
	}
}

func TestRecordInventoryValidationIsAtomic(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	firstObserved := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	secondObserved := firstObserved.Add(time.Hour)
	existing := testCapability("existing", time.Time{}, time.Time{})
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, firstObserved, []domain.Capability{existing}); err != nil {
		t.Fatalf("seed inventory scan: %v", err)
	}
	newCapability := testCapability("new", time.Time{}, time.Time{})
	invalid := testCapability("invalid", time.Time{}, time.Time{})
	invalid.Name = " "
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, secondObserved, []domain.Capability{newCapability, invalid}); err == nil {
		t.Fatal("invalid inventory scan accepted")
	}
	historical, err := s.ListCapabilities(ctx)
	if err != nil {
		t.Fatalf("ListCapabilities() after failed scan: %v", err)
	}
	if len(historical) != 1 || historical[0].Name != existing.Name || !historical[0].LastSeen.Equal(firstObserved) {
		t.Fatalf("historical capabilities after failed scan = %#v, want existing row at %s", historical, firstObserved)
	}
	current, err := s.ListCurrentCapabilities(ctx, domain.RuntimeCodex)
	if err != nil {
		t.Fatalf("ListCurrentCapabilities() after failed scan: %v", err)
	}
	if len(current) != 1 || current[0].Name != existing.Name {
		t.Fatalf("current capabilities after failed scan = %#v, want existing row", current)
	}
}

func TestRecordInventoryPersistsCurrentMarkerAndUTCExactness(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "inventory.db")
	observedAt := time.Date(2026, 8, 13, 14, 30, 0, 123456789, time.FixedZone("CEST", 2*60*60))
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	capability := testCapability("persisted", time.Time{}, time.Time{})
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, observedAt, []domain.Capability{capability}); err != nil {
		t.Fatalf("RecordInventory(): %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	current, err := reopened.ListCurrentCapabilities(ctx, domain.RuntimeCodex)
	if err != nil {
		t.Fatalf("ListCurrentCapabilities() after reopen: %v", err)
	}
	if len(current) != 1 {
		t.Fatalf("current capabilities after reopen = %#v, want one row", current)
	}
	if !current[0].LastSeen.Equal(observedAt.UTC()) || current[0].LastSeen.Location() != time.UTC {
		t.Fatalf("reopened LastSeen = %s (%s), want UTC %s", current[0].LastSeen, current[0].LastSeen.Location(), observedAt.UTC())
	}
	var marker string
	if err := reopened.db.QueryRowContext(ctx, `SELECT observed_at FROM inventory_scans WHERE runtime = ?`, domain.RuntimeCodex).Scan(&marker); err != nil {
		t.Fatalf("read persisted inventory marker: %v", err)
	}
	if marker != observedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("persisted inventory marker = %q, want %q", marker, observedAt.UTC().Format(time.RFC3339Nano))
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

func TestCapabilityAdvertisementStatesPersistLosslessly(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	states := []domain.AdvertisementState{
		domain.AdvertisementStateUnknown,
		domain.AdvertisementStateFullyAdvertised,
		domain.AdvertisementStateNameOnly,
		domain.AdvertisementStateNotAdvertised,
	}
	capabilities := make([]domain.Capability, 0, len(states))
	wantBySource := make(map[string]domain.AdvertisementState, len(states))
	wantEnabledBySource := make(map[string]domain.EnabledState, len(states))
	enabledStates := []domain.EnabledState{
		domain.EnabledStateUnknown,
		domain.EnabledStateEnabled,
		domain.EnabledStateDisabled,
		domain.EnabledStateEnabled,
	}
	for i, state := range states {
		name := strings.ReplaceAll(string(state), "-", "_")
		capability := testCapability("advertisement-"+name, time.Time{}, time.Time{})
		capability.Source = "/project/skills/advertisement-" + name
		capability.Enabled = enabledStates[i]
		capability.Advertisement = state
		capabilities = append(capabilities, capability)
		wantBySource[capability.Source] = state
		wantEnabledBySource[capability.Source] = capability.Enabled
	}
	if err := s.UpsertCapabilities(ctx, capabilities); err != nil {
		t.Fatalf("upsert advertisement states: %v", err)
	}
	got, err := s.ListCapabilities(ctx)
	if err != nil {
		t.Fatalf("ListCapabilities(): %v", err)
	}
	if len(got) != len(states) {
		t.Fatalf("capability count = %d, want %d", len(got), len(states))
	}
	for _, capability := range got {
		if !capability.Advertisement.Valid() {
			t.Fatalf("invalid persisted advertisement state: %#v", capability)
		}
		if capability.Advertisement != wantBySource[capability.Source] {
			t.Fatalf("persisted advertisement state for %q = %q, want %q", capability.Source, capability.Advertisement, wantBySource[capability.Source])
		}
		if capability.Enabled != wantEnabledBySource[capability.Source] {
			t.Fatalf("persisted enabled state for %q = %q, want %q", capability.Source, capability.Enabled, wantEnabledBySource[capability.Source])
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
		Advertisement:  domain.AdvertisementStateFullyAdvertised,
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
	want := []string{"capabilities", "current_inventory", "inventory_scans", "schema_meta", "usage_events"}
	if !reflect.DeepEqual(tables, want) {
		sort.Strings(tables)
		t.Fatalf("schema tables = %v, want %v", tables, want)
	}
}
