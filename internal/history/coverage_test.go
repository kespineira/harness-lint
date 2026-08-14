package history

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

func TestNormalizeIntervalsMergesOverlapAndAdjacency(t *testing.T) {
	utc := time.UTC
	a := time.Date(2026, 8, 14, 10, 0, 0, 0, utc)
	b := a.Add(time.Hour)
	c := a.Add(2 * time.Hour)
	d := a.Add(3 * time.Hour)
	got, err := NormalizeIntervals([]Interval{
		{Start: c, End: d},
		{Start: a, End: b},
		{Start: b, End: c},
	})
	if err != nil {
		t.Fatalf("NormalizeIntervals() error = %v", err)
	}
	want := []Interval{{Start: a, End: d}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeIntervals() = %#v, want %#v", got, want)
	}
}

func TestNormalizeIntervalsKeepsOpenTailAndNormalizesUTC(t *testing.T) {
	zone := time.FixedZone("test", 2*60*60)
	start := time.Date(2026, 8, 14, 12, 0, 0, 0, zone)
	end := start.Add(time.Hour)
	got, err := NormalizeIntervals([]Interval{
		{Start: end, End: time.Time{}},
		{Start: start, End: end},
	})
	if err != nil {
		t.Fatalf("NormalizeIntervals() error = %v", err)
	}
	if len(got) != 1 || !got[0].IsOpen() || !got[0].Start.Equal(start.UTC()) || got[0].Start.Location() != time.UTC {
		t.Fatalf("open normalized intervals = %#v, want one UTC open tail", got)
	}
}

func TestIntersectIntervalsUsesHalfOpenBoundaries(t *testing.T) {
	a := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	b := a.Add(time.Hour)
	c := a.Add(2 * time.Hour)
	got, err := IntersectIntervals(
		[]Interval{{Start: a, End: c}},
		[]Interval{{Start: b, End: time.Time{}}},
	)
	if err != nil {
		t.Fatalf("IntersectIntervals() error = %v", err)
	}
	want := []Interval{{Start: b, End: c}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IntersectIntervals() = %#v, want %#v", got, want)
	}

	noOverlap, err := IntersectIntervals([]Interval{{Start: a, End: b}}, []Interval{{Start: b, End: c}})
	if err != nil {
		t.Fatalf("touching IntersectIntervals() error = %v", err)
	}
	if len(noOverlap) != 0 {
		t.Fatalf("touching intervals intersected as %#v, want no positive interval", noOverlap)
	}
}

func TestComputeEffectiveCoverageIntersectsEpochsAndPreservesGaps(t *testing.T) {
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	key := CoverageKey{Runtime: domain.RuntimeCodex, CapabilityType: domain.CapabilityTool, CapabilityName: "formatter"}
	capture := []CaptureEpoch{
		{Runtime: key.Runtime, Interval: Interval{Start: base, End: base.Add(2 * time.Hour)}, StartReason: CaptureStartReasonConfirmedDirectDelivery, EndReason: CaptureEndReasonConfirmedCaptureFailure},
		// This is a new epoch after a confirmed failure; it must not bridge
		// the gap between the two deliveries.
		{Runtime: key.Runtime, Interval: Interval{Start: base.Add(4 * time.Hour), End: base.Add(6 * time.Hour)}, StartReason: CaptureStartReasonConfirmedDirectDelivery, EndReason: CaptureEndReasonManagedHookUninstall},
	}
	presence := []CapabilityPresenceEpoch{
		{CoverageKey: key, Interval: Interval{Start: base.Add(time.Hour), End: base.Add(5 * time.Hour)}},
	}
	got, err := ComputeEffectiveCoverage(key, capture, presence, nil)
	if err != nil {
		t.Fatalf("ComputeEffectiveCoverage() error = %v", err)
	}
	want := []Interval{
		{Start: base.Add(time.Hour), End: base.Add(2 * time.Hour)},
		{Start: base.Add(4 * time.Hour), End: base.Add(5 * time.Hour)},
	}
	if !reflect.DeepEqual(got.Intervals, want) || got.Status != CoveragePartial {
		t.Fatalf("effective coverage = %#v, status %q; want %#v, partial", got.Intervals, got.Status, want)
	}
}

func TestComputeEffectiveCoverageClipsToQueryAndAsOf(t *testing.T) {
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	key := CoverageKey{Runtime: domain.RuntimeCodex, CapabilityType: domain.CapabilityTool, CapabilityName: "formatter"}
	capture := []CaptureEpoch{{Runtime: key.Runtime, Interval: Interval{Start: base, End: time.Time{}}, StartReason: CaptureStartReasonConfirmedDirectDelivery}}
	presence := []CapabilityPresenceEpoch{{CoverageKey: key, Interval: Interval{Start: base, End: time.Time{}}}}
	asOf := base.Add(3 * time.Hour)
	clip := Interval{Start: base.Add(time.Hour), End: base.Add(4 * time.Hour)}
	got, err := ComputeEffectiveCoverage(key, capture, presence, &clip)
	if err != nil {
		t.Fatalf("query-clipped ComputeEffectiveCoverage() error = %v", err)
	}
	want := []Interval{{Start: base.Add(time.Hour), End: base.Add(4 * time.Hour)}}
	if !reflect.DeepEqual(got.Intervals, want) || got.Status != CoveragePartial {
		t.Fatalf("query-clipped coverage = %#v, status %q; want %#v, partial", got.Intervals, got.Status, want)
	}

	clipped, err := ClipIntervalsAsOf(got.Intervals, asOf)
	if err != nil {
		t.Fatalf("ClipIntervalsAsOf() error = %v", err)
	}
	want = []Interval{{Start: base.Add(time.Hour), End: asOf}}
	if !reflect.DeepEqual(clipped, want) {
		t.Fatalf("as-of clipped coverage = %#v, want %#v", clipped, want)
	}

	open, err := ComputeEffectiveCoverage(key, capture, presence, nil)
	if err != nil {
		t.Fatalf("open ComputeEffectiveCoverage() error = %v", err)
	}
	if len(open.Intervals) != 1 || !open.Intervals[0].IsOpen() || open.Status != CoveragePartial {
		t.Fatalf("open coverage = %#v, status %q; want open partial", open.Intervals, open.Status)
	}
}

func TestComputeEffectiveCoverageUnknownWithoutProvableIntersection(t *testing.T) {
	base := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	key := CoverageKey{Runtime: domain.RuntimeCodex, CapabilityType: domain.CapabilityTool, CapabilityName: "formatter"}
	// Presence alone is not capture, and capture alone has no matching
	// capability presence. Installation/configuration therefore cannot start
	// coverage.
	got, err := ComputeEffectiveCoverage(key,
		[]CaptureEpoch{{Runtime: key.Runtime, Interval: Interval{Start: base, End: base.Add(time.Hour)}, StartReason: CaptureStartReasonConfirmedDirectDelivery, EndReason: CaptureEndReasonConfirmedCaptureFailure}},
		[]CapabilityPresenceEpoch{{CoverageKey: key, Interval: Interval{Start: base.Add(2 * time.Hour), End: base.Add(3 * time.Hour)}}},
		nil,
	)
	if err != nil {
		t.Fatalf("ComputeEffectiveCoverage() error = %v", err)
	}
	if len(got.Intervals) != 0 || got.Status != CoverageUnknown {
		t.Fatalf("unknown coverage = %#v, status %q; want empty unknown", got.Intervals, got.Status)
	}
}

func TestIntervalValidationRejectsReversedAndZeroLengthClosedIntervals(t *testing.T) {
	start := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	for name, interval := range map[string]Interval{
		"reversed":    {Start: start.Add(time.Hour), End: start},
		"zero length": {Start: start, End: start},
	} {
		t.Run(name, func(t *testing.T) {
			if err := interval.Validate(); err == nil {
				t.Fatalf("Interval.Validate() = nil for %s interval", name)
			}
		})
	}
}

func TestCoverageContractRejectsReservedCompleteAndKeepsIdentityIndependent(t *testing.T) {
	key := CoverageKey{Runtime: domain.RuntimeCodex, CapabilityType: domain.CapabilityTool, CapabilityName: "formatter"}
	if err := (EffectiveCoverage{Key: key, Status: CoverageComplete}).Validate(); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved complete validation error = %v, want reserved error", err)
	}
	if err := (EffectiveCoverage{Key: key, Status: CoverageUnknown, Intervals: []Interval{{Start: time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC), End: time.Date(2026, 8, 14, 11, 0, 0, 0, time.UTC)}}}).Validate(); err == nil {
		t.Fatal("unknown status with positive coverage validated, want status/evidence mismatch")
	}
}

func TestCaptureEpochValidationRequiresConfirmedDirectDeliveryStartReason(t *testing.T) {
	start := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	base := CaptureEpoch{
		Runtime:  domain.RuntimeCodex,
		Interval: Interval{Start: start},
	}
	for name, reason := range map[string]CaptureStartReason{
		"missing": "",
		"invalid": "configuration",
	} {
		t.Run(name, func(t *testing.T) {
			epoch := base
			epoch.StartReason = reason
			if err := epoch.Validate(); err == nil {
				t.Fatalf("CaptureEpoch.Validate() = nil for %s start reason", name)
			}
		})
	}

	base.StartReason = CaptureStartReasonConfirmedDirectDelivery
	if err := base.Validate(); err != nil {
		t.Fatalf("CaptureEpoch.Validate() with confirmed direct delivery = %v", err)
	}
}

func TestCaptureEpochValidationRequiresEndReasonOnlyWhenClosed(t *testing.T) {
	start := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	confirmed := CaptureStartReasonConfirmedDirectDelivery

	closedWithoutEnd := CaptureEpoch{
		Runtime:     domain.RuntimeCodex,
		Interval:    Interval{Start: start, End: start.Add(time.Hour)},
		StartReason: confirmed,
	}
	if err := closedWithoutEnd.Validate(); err == nil {
		t.Fatal("closed CaptureEpoch.Validate() = nil without end reason")
	}

	openWithEnd := CaptureEpoch{
		Runtime:     domain.RuntimeCodex,
		Interval:    Interval{Start: start},
		StartReason: confirmed,
		EndReason:   CaptureEndReasonConfirmedCaptureFailure,
	}
	if err := openWithEnd.Validate(); err == nil {
		t.Fatal("open CaptureEpoch.Validate() = nil with end reason")
	}
}

func TestCaptureEpochValidationAllowsConfirmedFailureAndManagedUninstall(t *testing.T) {
	start := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	for _, reason := range []CaptureEndReason{
		CaptureEndReasonConfirmedCaptureFailure,
		CaptureEndReasonManagedHookUninstall,
	} {
		epoch := CaptureEpoch{
			Runtime:     domain.RuntimeCodex,
			Interval:    Interval{Start: start, End: start.Add(time.Hour)},
			StartReason: CaptureStartReasonConfirmedDirectDelivery,
			EndReason:   reason,
		}
		if err := epoch.Validate(); err != nil {
			t.Fatalf("CaptureEpoch.Validate() with end reason %q = %v", reason, err)
		}
	}

	invalid := CaptureEpoch{
		Runtime:     domain.RuntimeCodex,
		Interval:    Interval{Start: start, End: start.Add(time.Hour)},
		StartReason: CaptureStartReasonConfirmedDirectDelivery,
		EndReason:   "configuration",
	}
	if err := invalid.Validate(); err == nil {
		t.Fatal("CaptureEpoch.Validate() = nil with invalid end reason")
	}
}
