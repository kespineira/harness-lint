package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

func TestDatabaseStatusEmptyAndPopulatedDatabase(t *testing.T) {
	ctx := context.Background()
	empty := openTestStore(t)
	status, err := empty.DatabaseStatus(ctx)
	if err != nil {
		t.Fatalf("DatabaseStatus(empty): %v", err)
	}
	if status.Path != ":memory:" || status.Schema != (SchemaStatus{Current: 6, Latest: 6}) || status.SizeBytes != nil || status.UsageEventCount != 0 || status.OldestObservedAt != nil || status.LatestObservedAt != nil {
		t.Fatalf("empty status = %#v", status)
	}

	path := filepath.Join(t.TempDir(), "status.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(file): %v", err)
	}
	defer s.Close()
	observedFirst := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	sourceFirst := observedFirst.Add(-time.Hour)
	first := testUsageEvent(observedFirst, "first", "invoked")
	first.SourceTimestamp = &sourceFirst
	second := testUsageEvent(observedFirst.Add(2*time.Hour), "second", "loaded")
	sourceSecond := second.ObservedAt.Add(time.Hour)
	second.SourceTimestamp = &sourceSecond
	if err := s.InsertUsageEvents(ctx, []domain.UsageEvent{first, second}); err != nil {
		t.Fatalf("InsertUsageEvents(): %v", err)
	}
	status, err = s.DatabaseStatus(ctx)
	if err != nil {
		t.Fatalf("DatabaseStatus(populated): %v", err)
	}
	if status.Path != path || status.Schema != (SchemaStatus{Current: 6, Latest: 6}) || status.SizeBytes == nil || *status.SizeBytes <= 0 || status.UsageEventCount != 2 {
		t.Fatalf("populated status = %#v", status)
	}
	if status.OldestObservedAt == nil || !status.OldestObservedAt.Equal(first.ObservedAt) || status.LatestObservedAt == nil || !status.LatestObservedAt.Equal(second.ObservedAt) {
		t.Fatalf("observed range = %v/%v", status.OldestObservedAt, status.LatestObservedAt)
	}
}

func TestCheckDatabaseHealthyAndDoesNotMutateLogicalData(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	event := testUsageEvent(time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC), "safe", "invoked")
	if err := s.InsertUsageEvents(ctx, []domain.UsageEvent{event}); err != nil {
		t.Fatalf("InsertUsageEvents(): %v", err)
	}
	beforeEvents, err := s.ListUsageEvents(ctx, time.Time{})
	if err != nil {
		t.Fatalf("ListUsageEvents(before): %v", err)
	}
	var beforeEvidence int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_event_evidence`).Scan(&beforeEvidence); err != nil {
		t.Fatalf("evidence count before: %v", err)
	}
	result, err := s.CheckDatabase(ctx)
	if err != nil {
		t.Fatalf("CheckDatabase(): %v", err)
	}
	if !result.Healthy || result.QuickCheck != IntegrityOK || result.ForeignKeyCheck != IntegrityOK || result.Schema != IntegrityOK || len(result.Issues) != 0 {
		t.Fatalf("healthy result = %#v", result)
	}
	afterEvents, err := s.ListUsageEvents(ctx, time.Time{})
	if err != nil {
		t.Fatalf("ListUsageEvents(after): %v", err)
	}
	var afterEvidence int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_event_evidence`).Scan(&afterEvidence); err != nil {
		t.Fatalf("evidence count after: %v", err)
	}
	if !reflect.DeepEqual(beforeEvents, afterEvents) || beforeEvidence != afterEvidence {
		t.Fatalf("Check mutated logical data: before=%#v/%d after=%#v/%d", beforeEvents, beforeEvidence, afterEvents, afterEvidence)
	}
}

func TestCheckDatabaseSeparatesForeignKeyIssues(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO usage_event_evidence(fingerprint, provenance, observed_at, invocation_origin) VALUES ('orphan', 'import', '2026-08-14T00:00:00Z', 'unknown')`); err != nil {
		t.Fatalf("insert orphan evidence: %v", err)
	}
	result, err := s.CheckDatabase(ctx)
	if err != nil {
		t.Fatalf("CheckDatabase(): %v", err)
	}
	if result.Healthy || result.QuickCheck != IntegrityOK || result.ForeignKeyCheck != IntegrityIssues || result.Schema != IntegrityOK {
		t.Fatalf("foreign-key result = %#v", result)
	}
	for _, issue := range result.Issues {
		if issue.Check != "foreign_key_check" {
			t.Fatalf("foreign-key issue = %#v", issue)
		}
	}
}

func TestParseIntegrityRowsIsCoarseAndBounded(t *testing.T) {
	values := make([]string, maxIntegrityIssues+4)
	values[0] = "ok"
	values[1] = "table=secret prompt/tool/source sentinel"
	issues := parseIntegrityRows("quick_check", values)
	if len(issues) != maxIntegrityIssues {
		t.Fatalf("issue count = %d, want %d", len(issues), maxIntegrityIssues)
	}
	for _, issue := range issues {
		if issue.Check != "quick_check" || strings.Contains(issue.Check, "secret") || strings.Contains(issue.Check, "sentinel") {
			t.Fatalf("coarse issue = %#v", issue)
		}
	}
}

func TestCheckDatabaseReportsUnexpectedAndMalformedSchemaVersion(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "newer", raw: "99", want: "schema_version_newer"},
		{name: "outdated", raw: "5", want: "schema_version_mismatch"},
		{name: "malformed", raw: "not-a-version", want: "schema_version_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatalf("sql.Open(): %v", err)
			}
			t.Cleanup(func() { db.Close() })
			if _, err := db.ExecContext(ctx, `CREATE TABLE schema_meta(key TEXT PRIMARY KEY NOT NULL, value TEXT NOT NULL); INSERT INTO schema_meta(key, value) VALUES ('version', ?)`, test.raw); err != nil {
				t.Fatalf("seed schema version: %v", err)
			}
			result, err := (&Store{db: db, path: ":memory:"}).CheckDatabase(ctx)
			if err != nil {
				t.Fatalf("CheckDatabase(): %v", err)
			}
			if result.Healthy || result.Schema != IntegrityIssues {
				t.Fatalf("schema result = %#v", result)
			}
			if !hasIntegrityIssue(result.Issues, test.want) {
				t.Fatalf("schema issues = %#v, want %q", result.Issues, test.want)
			}
		})
	}
}

func TestParseSchemaVersionReturnsMalformedSentinel(t *testing.T) {
	_, err := parseSchemaVersion("not-a-version")
	if err == nil || !errors.Is(err, ErrMalformedSchemaVersion) || !strings.Contains(err.Error(), "not-a-version") {
		t.Fatalf("parseSchemaVersion() error = %v, want malformed sentinel and raw value", err)
	}
}

func TestIntegrityStateValidityIsAllowListed(t *testing.T) {
	for _, state := range []IntegrityState{IntegrityOK, IntegrityIssues, IntegrityUnavailable} {
		if !state.Valid() {
			t.Fatalf("IntegrityState(%q).Valid() = false", state)
		}
	}
	if IntegrityState("unexpected").Valid() {
		t.Fatal("unexpected integrity state is valid")
	}
}

func TestCheckDatabaseReportsMalformedMigrationDefinition(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	result, err := s.checkWithMigrationFS(ctx, fstest.MapFS{
		"migrations/001_bad.txt": &fstest.MapFile{Data: []byte("not migration")},
	})
	if err != nil {
		t.Fatalf("checkWithMigrationFS(): %v", err)
	}
	if result.Healthy || result.Schema != IntegrityIssues || !hasIntegrityIssue(result.Issues, "schema_migrations") {
		t.Fatalf("malformed migration result = %#v", result)
	}
}

func TestCheckDatabaseAndDatabaseStatusClosedStoreReturnsCleanError(t *testing.T) {
	s := openTestStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if _, err := s.CheckDatabase(context.Background()); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("CheckDatabase(closed) error = %v", err)
	}
	if _, err := s.DatabaseStatus(context.Background()); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("DatabaseStatus(closed) error = %v", err)
	}
}

func hasIntegrityIssue(issues []IntegrityIssue, check string) bool {
	for _, issue := range issues {
		if issue.Check == check {
			return true
		}
	}
	return false
}
