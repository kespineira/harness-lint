package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
)

func TestQueryEffectiveCoverageIntersectsSparseEpochsAndPreservesGaps(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Date(2026, 8, 14, 10, 0, 0, 123456789, time.FixedZone("CEST", 2*60*60))
	capability := testCapability("query-coverage", time.Time{}, time.Time{})
	key := CapabilityPresenceKey{Runtime: capability.Runtime, CapabilityType: capability.Type, CapabilityName: capability.Name, Scope: capability.Scope, Source: capability.Source}
	if err := s.OpenCaptureEpoch(ctx, domain.RuntimeCodex, base, history.CaptureStartReasonConfirmedDirectDelivery); err != nil {
		t.Fatalf("OpenCaptureEpoch(first): %v", err)
	}
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, base.Add(time.Hour), []domain.Capability{capability}); err != nil {
		t.Fatalf("RecordInventory(present): %v", err)
	}
	if err := s.CloseCaptureEpoch(ctx, domain.RuntimeCodex, base.Add(2*time.Hour), history.CaptureEndReasonConfirmedCaptureFailure); err != nil {
		t.Fatalf("CloseCaptureEpoch(first): %v", err)
	}
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, base.Add(3*time.Hour), nil); err != nil {
		t.Fatalf("RecordInventory(absent): %v", err)
	}
	if err := s.OpenCaptureEpoch(ctx, domain.RuntimeCodex, base.Add(4*time.Hour), history.CaptureStartReasonConfirmedDirectDelivery); err != nil {
		t.Fatalf("OpenCaptureEpoch(second): %v", err)
	}
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, base.Add(4*time.Hour), []domain.Capability{capability}); err != nil {
		t.Fatalf("RecordInventory(reappeared): %v", err)
	}
	if err := s.RecordInventory(ctx, domain.RuntimeCodex, base.Add(5*time.Hour), nil); err != nil {
		t.Fatalf("RecordInventory(absent again): %v", err)
	}
	if err := s.CloseCaptureEpoch(ctx, domain.RuntimeCodex, base.Add(6*time.Hour), history.CaptureEndReasonManagedHookUninstall); err != nil {
		t.Fatalf("CloseCaptureEpoch(second): %v", err)
	}

	rows, err := s.QueryEffectiveCoverage(ctx, history.CoverageQuery{Runtime: key.Runtime, CapabilityType: key.CapabilityType, CapabilityName: key.CapabilityName, Start: base, End: base.Add(7 * time.Hour)})
	if err != nil {
		t.Fatalf("QueryEffectiveCoverage(): %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("coverage rows = %#v, want one", rows)
	}
	want := []history.Interval{{Start: base.Add(time.Hour).UTC(), End: base.Add(2 * time.Hour).UTC()}, {Start: base.Add(4 * time.Hour).UTC(), End: base.Add(5 * time.Hour).UTC()}}
	if rows[0].Status != history.CoveragePartial || !reflect.DeepEqual(rows[0].Intervals, want) {
		t.Fatalf("effective coverage = %#v, status %q; want %#v partial", rows[0].Intervals, rows[0].Status, want)
	}

	// Querying by scope must not accidentally use an identically named epoch
	// from another scope.
	if rows[0].Key != (history.CoverageKey{Runtime: key.Runtime, CapabilityType: key.CapabilityType, CapabilityName: key.CapabilityName}) {
		t.Fatalf("coverage identity = %#v", rows[0].Key)
	}
}

func TestQueryCapabilityCoverageUnionsHomonymousDefinitionsAcrossScopeAndSource(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	project := testCapability("homonymous", time.Time{}, time.Time{})
	project.Scope = domain.ScopeProject
	project.Source = "/project/homonymous"
	user := project
	user.Scope = domain.ScopeUser
	user.Source = "/user/homonymous"
	if err := s.OpenCaptureEpoch(ctx, project.Runtime, base, history.CaptureStartReasonConfirmedDirectDelivery); err != nil {
		t.Fatalf("OpenCaptureEpoch(): %v", err)
	}
	if err := s.RecordInventory(ctx, project.Runtime, base, []domain.Capability{project}); err != nil {
		t.Fatalf("RecordInventory(project present): %v", err)
	}
	if err := s.RecordInventory(ctx, project.Runtime, base.Add(time.Hour), nil); err != nil {
		t.Fatalf("RecordInventory(absent): %v", err)
	}
	if err := s.RecordInventory(ctx, project.Runtime, base.Add(2*time.Hour), []domain.Capability{user}); err != nil {
		t.Fatalf("RecordInventory(user present): %v", err)
	}
	if err := s.RecordInventory(ctx, project.Runtime, base.Add(3*time.Hour), nil); err != nil {
		t.Fatalf("RecordInventory(user absent): %v", err)
	}

	key := history.CoverageKey{Runtime: project.Runtime, CapabilityType: project.Type, CapabilityName: project.Name}
	got, err := s.QueryCapabilityCoverage(ctx, key, history.CoverageQuery{Start: base, End: base.Add(4 * time.Hour)})
	if err != nil {
		t.Fatalf("QueryCapabilityCoverage(): %v", err)
	}
	want := []history.Interval{{Start: base.UTC(), End: base.Add(time.Hour).UTC()}, {Start: base.Add(2 * time.Hour).UTC(), End: base.Add(3 * time.Hour).UTC()}}
	if !reflect.DeepEqual(got.Intervals, want) || got.Status != history.CoveragePartial {
		t.Fatalf("canonical homonymous coverage = %#v, status %q; want %#v, partial", got.Intervals, got.Status, want)
	}
}

func TestQueryEffectiveCoverageClipsOpenEpochsAndNeverComplete(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Date(2026, 8, 14, 23, 59, 59, 999000000, time.FixedZone("CET", 60*60))
	capability := testCapability("open-query-coverage", time.Time{}, time.Time{})
	if err := s.OpenCaptureEpoch(ctx, capability.Runtime, base, history.CaptureStartReasonConfirmedDirectDelivery); err != nil {
		t.Fatalf("OpenCaptureEpoch(): %v", err)
	}
	if err := s.RecordInventory(ctx, capability.Runtime, base, []domain.Capability{capability}); err != nil {
		t.Fatalf("RecordInventory(): %v", err)
	}
	key := history.CoverageKey{Runtime: capability.Runtime, CapabilityType: capability.Type, CapabilityName: capability.Name}
	open, err := s.QueryCapabilityCoverage(ctx, key, history.CoverageQuery{Start: base})
	if err != nil {
		t.Fatalf("QueryCapabilityCoverage(open): %v", err)
	}
	if len(open.Intervals) != 1 || !open.Intervals[0].IsOpen() || open.Status != history.CoveragePartial {
		t.Fatalf("open coverage = %#v, status %q; want open partial", open.Intervals, open.Status)
	}
	end := base.Add(2 * time.Hour)
	clipped, err := s.QueryCapabilityCoverage(ctx, key, history.CoverageQuery{End: end})
	if err != nil {
		t.Fatalf("QueryCapabilityCoverage(end): %v", err)
	}
	want := []history.Interval{{Start: base.UTC(), End: end.UTC()}}
	if !reflect.DeepEqual(clipped.Intervals, want) || clipped.Status != history.CoveragePartial {
		t.Fatalf("as-of coverage = %#v, status %q; want %#v partial", clipped.Intervals, clipped.Status, want)
	}
	if clipped.Status == history.CoverageComplete {
		t.Fatal("effective coverage emitted complete")
	}
}

func TestQueryInvocationHistoryKeepsLegacyEvidenceSeparateAndMarksTranscriptOutsideCoverageUnknown(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	observed := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	event := testUsageEvent(observed.Add(3*time.Hour), "transcript-outside", domain.EventInvoked)
	event.Provenance = domain.ProvenanceTranscript
	if err := s.InsertUsageEvents(ctx, []domain.UsageEvent{event}); err != nil {
		t.Fatalf("InsertUsageEvents(): %v", err)
	}
	rows, err := s.QueryInvocationHistory(ctx, history.Query{Runtime: event.Runtime, CapabilityType: event.CapabilityType, CapabilityName: event.CapabilityName})
	if err != nil {
		t.Fatalf("QueryInvocationHistory(): %v", err)
	}
	if len(rows) != 1 || rows[0].Uses != 1 {
		t.Fatalf("history rows = %#v", rows)
	}
	if rows[0].EffectiveCoverage == nil || rows[0].EffectiveCoverage.Status != history.CoverageUnknown || len(rows[0].EffectiveCoverage.Intervals) != 0 {
		t.Fatalf("transcript-only effective coverage = %#v, want unknown", rows[0].EffectiveCoverage)
	}
	if rows[0].Coverage == nil {
		t.Fatalf("legacy observation evidence = %#v, want coverage", rows[0].Coverage)
	}
}

func TestQueryInvocationHistoryTranslatesInclusiveEventEndToHalfOpenCoverage(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	capability := testCapability("inclusive-end", time.Time{}, time.Time{})
	if err := s.OpenCaptureEpoch(ctx, capability.Runtime, base, history.CaptureStartReasonConfirmedDirectDelivery); err != nil {
		t.Fatalf("OpenCaptureEpoch(): %v", err)
	}
	if err := s.RecordInventory(ctx, capability.Runtime, base, []domain.Capability{capability}); err != nil {
		t.Fatalf("RecordInventory(): %v", err)
	}
	// The increment crosses a second boundary, exercising fixed-precision
	// half-open translation rather than relying on RFC3339Nano's trimmed form.
	eventEnd := base.Add(time.Hour).Add(-time.Nanosecond)
	event := testUsageEvent(eventEnd, capability.Name, domain.EventInvoked)
	if err := s.InsertUsageEvents(ctx, []domain.UsageEvent{event}); err != nil {
		t.Fatalf("InsertUsageEvents(): %v", err)
	}
	rows, err := s.QueryInvocationHistory(ctx, history.Query{Runtime: capability.Runtime, CapabilityType: capability.Type, CapabilityName: capability.Name, Start: base, End: eventEnd})
	if err != nil {
		t.Fatalf("QueryInvocationHistory(): %v", err)
	}
	if len(rows) != 1 || rows[0].EffectiveCoverage == nil || len(rows[0].EffectiveCoverage.Intervals) != 1 {
		t.Fatalf("history coverage = %#v, want one interval", rows)
	}
	got := rows[0].EffectiveCoverage.Intervals[0]
	if !got.Start.Equal(base) || !got.End.Equal(eventEnd.Add(time.Nanosecond)) {
		t.Fatalf("inclusive end translation = %#v, want [%s,%s)", got, base, eventEnd.Add(time.Nanosecond))
	}
}

func TestCoverageQueryRejectsConflictingCanonicalKeyFiltersAndHandlesMaximumEnd(t *testing.T) {
	key := history.CoverageKey{Runtime: domain.RuntimeCodex, CapabilityType: domain.CapabilityTool, CapabilityName: "max-time"}
	if _, err := coverageQueryForHistoryQuery(history.Query{End: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.FixedZone("CET", 2*60*60))}); err != nil {
		t.Fatalf("maximum event end translation: %v", err)
	}
	translated, err := coverageQueryForHistoryQuery(history.Query{End: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)})
	if err != nil {
		t.Fatalf("maximum UTC event end translation: %v", err)
	}
	if !translated.End.Equal(time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)) {
		t.Fatalf("maximum translated end = %s, want finite maximum", translated.End)
	}

	s := openTestStore(t)
	_, err = s.QueryCapabilityCoverage(context.Background(), key, history.CoverageQuery{Runtime: domain.RuntimeClaude})
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting runtime filter error = %v, want conflict", err)
	}
}

func TestExplainCoverageQueryPlanUsesEpochIndexes(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	plans, err := s.ExplainCoverageQueryPlan(ctx, history.CoverageQuery{Runtime: domain.RuntimeCodex, CapabilityType: domain.CapabilityType("tool"), CapabilityName: "plan"})
	if err != nil {
		t.Fatalf("ExplainCoverageQueryPlan(): %v", err)
	}
	joined := strings.ToLower(strings.Join(plans, "\n"))
	if !strings.Contains(joined, "capture_epochs") || !strings.Contains(joined, "capability_presence_epochs") {
		t.Fatalf("coverage plans = %#v", plans)
	}
	if !strings.Contains(joined, "capture_epochs_runtime_time_idx") || !strings.Contains(joined, "capability_presence_epochs_key_time_idx") {
		t.Fatalf("coverage plans do not show epoch indexes: %#v", plans)
	}
}
