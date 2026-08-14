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

	rows, err := s.QueryEffectiveCoverage(ctx, history.Query{Runtime: key.Runtime, CapabilityType: key.CapabilityType, CapabilityName: key.CapabilityName, Start: base, End: base.Add(7 * time.Hour)})
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

func TestQueryEffectiveCoverageClipsOpenEpochsAtAsOfAndNeverComplete(t *testing.T) {
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
	open, err := s.QueryCapabilityCoverage(ctx, key, history.Query{Runtime: key.Runtime, CapabilityType: key.CapabilityType, CapabilityName: key.CapabilityName, Start: base})
	if err != nil {
		t.Fatalf("QueryCapabilityCoverage(open): %v", err)
	}
	if len(open.Intervals) != 1 || !open.Intervals[0].IsOpen() || open.Status != history.CoveragePartial {
		t.Fatalf("open coverage = %#v, status %q; want open partial", open.Intervals, open.Status)
	}
	asOf := base.Add(2 * time.Hour)
	clipped, err := s.QueryCapabilityCoverage(ctx, key, history.Query{Runtime: key.Runtime, CapabilityType: key.CapabilityType, CapabilityName: key.CapabilityName, AsOf: asOf})
	if err != nil {
		t.Fatalf("QueryCapabilityCoverage(as-of): %v", err)
	}
	want := []history.Interval{{Start: base.UTC(), End: asOf.UTC()}}
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
	if rows[0].Coverage == nil || rows[0].ObservationOnlyCoverage == nil || rows[0].Coverage != rows[0].ObservationOnlyCoverage {
		t.Fatalf("legacy observation evidence = %#v/%#v, want separate alias", rows[0].Coverage, rows[0].ObservationOnlyCoverage)
	}
}

func TestExplainCoverageQueryPlanUsesEpochIndexes(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	plans, err := s.ExplainCoverageQueryPlan(ctx, history.Query{Runtime: domain.RuntimeCodex, CapabilityType: domain.CapabilityType("tool"), CapabilityName: "plan"})
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
