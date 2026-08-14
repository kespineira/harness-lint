package store

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
)

const storageScalePrivacySentinel = "storage-scale-raw-identity-sentinel"

type storageScaleForbiddenPayload struct {
	Prompt        string
	Response      string
	ToolArguments string
	LocalPath     string
}

func storageScaleForbiddenPayloadLikeData() storageScaleForbiddenPayload {
	return storageScaleForbiddenPayload{
		Prompt:        "prompt containing " + storageScalePrivacySentinel,
		Response:      "response containing " + storageScalePrivacySentinel,
		ToolArguments: "tool arguments containing " + storageScalePrivacySentinel,
		LocalPath:     "/private/project/" + storageScalePrivacySentinel,
	}
}

// TestStorageSmallMetadataOnlyCorrectness keeps the storage/query contract
// covered in normal CI. The tagged scale test below reuses the same setup and
// assertions with 100,000 rows without making the default test suite slow.
func TestStorageSmallMetadataOnlyCorrectness(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	const eventCount = 256
	base := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	if err := seedStorageScaleCoverage(ctx, s, base); err != nil {
		t.Fatalf("seed coverage: %v", err)
	}
	forbiddenPayload := storageScaleForbiddenPayloadLikeData()
	events := metadataOnlyScaleEvents(eventCount, base)
	assertStorageScaleMetadataOnlyInput(t, events, forbiddenPayload)
	if err := s.InsertUsageEvents(ctx, events); err != nil {
		t.Fatalf("InsertUsageEvents(%d): %v", eventCount, err)
	}
	assertStorageScaleQueries(t, ctx, s, eventCount, base)
	assertStorageScalePrivacy(t, ctx, s, forbiddenPayload, "source", s.path)
}

func metadataOnlyScaleEvents(count int, base time.Time) []domain.UsageEvent {
	events := make([]domain.UsageEvent, 0, count)
	for index := 0; index < count; index++ {
		monthBase := base
		if index >= count/2 {
			monthBase = base.AddDate(0, 1, 0)
		}
		withinMonth := index
		if index >= count/2 {
			withinMonth -= count / 2
		}
		sourceAt := monthBase.Add(time.Duration(withinMonth) * time.Second)
		observedAt := sourceAt.Add(5 * time.Minute)
		events = append(events, domain.UsageEvent{
			ObservedAt:       observedAt,
			SourceTimestamp:  &sourceAt,
			Runtime:          domain.RuntimeCodex,
			SessionID:        fmt.Sprintf("%064x", (index%17)+1),
			ProjectID:        strings.Repeat("b", 64),
			CapabilityType:   domain.CapabilityTool,
			CapabilityName:   "storage-scale-tool",
			EventType:        domain.EventInvoked,
			Provenance:       domain.ProvenanceImport,
			InvocationOrigin: domain.InvocationOriginUnknown,
			SchemaVersion:    domain.CurrentUsageEventSchemaVersion,
		})
	}
	return events
}

func seedStorageScaleCoverage(ctx context.Context, s *Store, base time.Time) error {
	if err := s.OpenCaptureEpoch(ctx, domain.RuntimeCodex, base, history.CaptureStartReasonConfirmedDirectDelivery); err != nil {
		return fmt.Errorf("open capture epoch: %w", err)
	}
	capability := testCapability("storage-scale-tool", base, base)
	capability.Type = domain.CapabilityTool
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, base.Add(time.Minute), []domain.Capability{capability}); err != nil {
		return fmt.Errorf("record inventory: %w", err)
	}
	return nil
}

type storageScaleQueryDurations struct {
	History  time.Duration
	Monthly  time.Duration
	Coverage time.Duration
}

func assertStorageScaleQueries(t *testing.T, ctx context.Context, s *Store, eventCount int, base time.Time) storageScaleQueryDurations {
	t.Helper()
	query := history.Query{
		Start:          base.Add(-time.Minute),
		End:            base.AddDate(0, 1, 1),
		Runtime:        domain.RuntimeCodex,
		CapabilityType: domain.CapabilityTool,
		CapabilityName: "storage-scale-tool",
	}
	historyStarted := time.Now()
	aggregates, err := s.QueryInvocationHistory(ctx, query)
	historyDuration := time.Since(historyStarted)
	if err != nil {
		t.Fatalf("QueryInvocationHistory(): %v", err)
	}
	if len(aggregates) != 1 {
		t.Fatalf("history aggregates = %#v, want one canonical key", aggregates)
	}
	aggregate := aggregates[0]
	if aggregate.Uses != int64(eventCount) || aggregate.DistinctInvocationSessions != 17 || aggregate.InvocationEvidenceCount(domain.ProvenanceImport) != int64(eventCount) {
		t.Fatalf("history aggregate = %#v, want %d uses, 17 sessions, and import evidence", aggregate, eventCount)
	}
	if aggregate.EffectiveCoverage == nil || aggregate.EffectiveCoverage.Status != history.CoveragePartial || len(aggregate.EffectiveCoverage.Intervals) != 1 {
		t.Fatalf("effective coverage = %#v, want one partial interval", aggregate.EffectiveCoverage)
	}

	monthlyStarted := time.Now()
	monthly, err := s.QueryMonthlyInvocations(ctx, query)
	monthlyDuration := time.Since(monthlyStarted)
	if err != nil {
		t.Fatalf("QueryMonthlyInvocations(): %v", err)
	}
	if len(monthly) != 2 || monthly[0].Uses != int64(eventCount/2) || monthly[1].Uses != int64(eventCount-eventCount/2) {
		t.Fatalf("monthly aggregates = %#v, want two half-count month buckets", monthly)
	}

	coverageQuery := history.CoverageQuery{
		Start:          query.Start,
		End:            query.End,
		Runtime:        query.Runtime,
		CapabilityType: query.CapabilityType,
		CapabilityName: query.CapabilityName,
	}
	coverageStarted := time.Now()
	coverage, err := s.QueryEffectiveCoverage(ctx, coverageQuery)
	coverageDuration := time.Since(coverageStarted)
	if err != nil {
		t.Fatalf("QueryEffectiveCoverage(): %v", err)
	}
	if len(coverage) != 1 || coverage[0].Status != history.CoveragePartial || len(coverage[0].Intervals) != 1 {
		t.Fatalf("coverage results = %#v, want one partial interval", coverage)
	}
	t.Logf("storage scale query durations: QueryInvocationHistory=%s QueryMonthlyInvocations=%s QueryEffectiveCoverage=%s results=history:%d monthly:%d coverage:%d", historyDuration.Round(time.Millisecond), monthlyDuration.Round(time.Millisecond), coverageDuration.Round(time.Millisecond), len(aggregates), len(monthly), len(coverage))

	historyPlan, err := s.ExplainHistoryQueryPlan(ctx, query)
	if err != nil {
		t.Fatalf("ExplainHistoryQueryPlan(): %v", err)
	}
	joinedHistoryPlan := strings.ToLower(strings.Join(historyPlan, "\n"))
	if !strings.Contains(joinedHistoryPlan, "usage_events_history_filter_idx") {
		t.Fatalf("history query plan = %#v, want bounded runtime/type/name/time index", historyPlan)
	}
	t.Logf("history plan: %s", strings.Join(historyPlan, " | "))

	coveragePlan, err := s.ExplainCoverageQueryPlan(ctx, coverageQuery)
	if err != nil {
		t.Fatalf("ExplainCoverageQueryPlan(): %v", err)
	}
	joinedCoveragePlan := strings.ToLower(strings.Join(coveragePlan, "\n"))
	for _, expected := range []string{"capture_epochs_runtime_time_idx", "capability_presence_epochs_key_time_idx"} {
		if !strings.Contains(joinedCoveragePlan, expected) {
			t.Fatalf("coverage query plan = %#v, want %s", coveragePlan, expected)
		}
	}
	if strings.Contains(joinedCoveragePlan, "usage_events") || strings.Contains(joinedCoveragePlan, "current_inventory") {
		t.Fatalf("coverage query plan unexpectedly scans non-epoch tables: %#v", coveragePlan)
	}
	t.Logf("coverage plan: %s", strings.Join(coveragePlan, " | "))
	return storageScaleQueryDurations{History: historyDuration, Monthly: monthlyDuration, Coverage: coverageDuration}
}

func assertStorageScaleMetadataOnlyInput(t *testing.T, events []domain.UsageEvent, forbidden storageScaleForbiddenPayload) {
	t.Helper()
	for _, value := range []string{forbidden.Prompt, forbidden.Response, forbidden.ToolArguments, forbidden.LocalPath} {
		if !strings.Contains(value, storageScalePrivacySentinel) {
			t.Fatalf("forbidden payload fixture = %q, want sentinel", value)
		}
	}
	for index, event := range events {
		for field, value := range map[string]string{
			"session_id":      event.SessionID,
			"project_id":      event.ProjectID,
			"capability_name": event.CapabilityName,
			"source_identity": event.SourceIdentity,
		} {
			if strings.Contains(value, storageScalePrivacySentinel) {
				t.Fatalf("metadata-only event %d field %s contains forbidden payload sentinel", index, field)
			}
		}
	}
}

func assertStorageScalePrivacy(t *testing.T, ctx context.Context, s *Store, forbidden storageScaleForbiddenPayload, label, path string) {
	t.Helper()
	if !strings.Contains(forbidden.Prompt+forbidden.Response+forbidden.ToolArguments+forbidden.LocalPath, storageScalePrivacySentinel) {
		t.Fatalf("privacy fixture %s does not contain sentinel", label)
	}
	tables, err := storageTableNames(ctx, s.db)
	if err != nil {
		t.Fatalf("privacy table listing (%s): %v", label, err)
	}
	for _, table := range tables {
		if err := assertStorageScaleTablePrivacy(ctx, s.db, table); err != nil {
			t.Fatalf("privacy sentinel in %s table %q: %v", label, table, err)
		}
	}
	if path == "" || strings.HasPrefix(path, ":memory:") {
		return
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s database bytes: %v", label, err)
	}
	if bytes.Contains(contents, []byte(storageScalePrivacySentinel)) {
		t.Fatalf("privacy sentinel appears in complete %s database bytes", label)
	}
}

func storageTableNames(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

func assertStorageScaleTablePrivacy(ctx context.Context, db *sql.DB, table string) error {
	quotedTable := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
	rows, err := db.QueryContext(ctx, `SELECT * FROM `+quotedTable)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(values))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return err
		}
		for _, value := range values {
			if strings.Contains(fmt.Sprint(value), storageScalePrivacySentinel) {
				return fmt.Errorf("sentinel found in row value")
			}
		}
	}
	return rows.Err()
}

func storageObjectCounts(ctx context.Context, db *sql.DB) (map[string]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT type, COUNT(*) FROM sqlite_master WHERE type IN ('table', 'index') GROUP BY type ORDER BY type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int64, 2)
	for rows.Next() {
		var objectType string
		var count int64
		if err := rows.Scan(&objectType, &count); err != nil {
			return nil, err
		}
		counts[objectType] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return counts, nil
}
