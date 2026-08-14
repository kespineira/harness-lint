package analysis

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
)

func TestAnalyzeHistoryNoHistoryIsReviewWithoutUseClaims(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	capability := testCapability("no-history")

	report, err := AnalyzeHistory([]domain.Capability{capability}, nil, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("AnalyzeHistory() error = %v", err)
	}
	evidence := report.Capabilities[0]
	if evidence.Classification != REVIEW || evidence.InvocationCount != 0 || evidence.ActivityCount != 0 {
		t.Fatalf("no-history evidence = %#v, want REVIEW with no activity", evidence)
	}
	if evidence.EventCounts[domain.EventAdvertised] != 0 || evidence.EventCounts[domain.EventLoaded] != 0 || evidence.EventCounts[domain.EventInvoked] != 0 {
		t.Fatalf("no-history event counts = %#v, want all zero", evidence.EventCounts)
	}
	if evidence.ObservedAdvertisedSessions != nil || evidence.HasFirstUsed || evidence.HasLastUsed {
		t.Fatalf("no-history nullable/use evidence = %#v", evidence)
	}
	if !strings.Contains(evidence.Basis, "never observed") || strings.Contains(strings.ToLower(evidence.Basis), "never used") {
		t.Fatalf("no-history basis = %q, want observation wording only", evidence.Basis)
	}
}

func TestAnalyzeHistoryEffectiveCoverageIsAsOfClippedAndUnknownWhenFuture(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	capability := testCapability("as-of-coverage")
	key := history.CoverageKey{Runtime: capability.Runtime, CapabilityType: capability.Type, CapabilityName: capability.Name}
	future := &history.EffectiveCoverage{Key: key, Status: history.CoveragePartial, Intervals: []history.Interval{{Start: now, End: now.Add(time.Hour)}}}
	partial := &history.EffectiveCoverage{Key: key, Status: history.CoveragePartial, Intervals: []history.Interval{{Start: now.Add(-2 * time.Hour), End: now.Add(time.Hour)}}}
	capability.FirstSeen = now.Add(-24 * time.Hour)
	capability.LastSeen = now
	aggregate := history.Aggregate{Runtime: capability.Runtime, CapabilityType: capability.Type, CapabilityName: capability.Name, EffectiveCoverage: future}
	result, err := AnalyzeHistory([]domain.Capability{capability}, []history.Aggregate{aggregate}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("AnalyzeHistory(future) error = %v", err)
	}
	if got := result.Capabilities[0]; got.ModeledCoverageStatus != history.CoverageUnknown || got.ModeledCoveredDuration != 0 || got.Classification != REVIEW {
		t.Fatalf("future modeled coverage = %#v, want unknown/zero and REVIEW", got)
	}
	aggregate.EffectiveCoverage = partial
	result, err = AnalyzeHistory([]domain.Capability{capability}, []history.Aggregate{aggregate}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("AnalyzeHistory(partial) error = %v", err)
	}
	got := result.Capabilities[0]
	if got.ModeledCoverageStatus != history.CoveragePartial || got.ModeledCoveredDuration != 2*time.Hour {
		t.Fatalf("as-of modeled coverage = %s/%s, want partial/2h", got.ModeledCoverageStatus, got.ModeledCoveredDuration)
	}
	if got.FirstSeen == nil || !got.FirstSeen.Equal(capability.FirstSeen) || got.LastSeen == nil || !got.LastSeen.Equal(capability.LastSeen) {
		t.Fatalf("inventory bounds = %v/%v, want domain capability bounds", got.FirstSeen, got.LastSeen)
	}
	if !strings.Contains(got.Basis, "modeled coverage status=partial") || !strings.Contains(got.Basis, "no invocation evidence") {
		t.Fatalf("basis = %q, want classification and coverage explanation", got.Basis)
	}
}

func TestAnalyzeHistorySeparatesAdvertisedLoadedAndInvocationCounts(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	capability := testCapability("state-counts")
	observed := now.Add(-time.Hour)
	effective := now.Add(-2 * time.Hour)
	advertisedSessions := int64(1)
	invokedAdvertisedSessions := int64(1)
	aggregate := history.Aggregate{
		Runtime:                     capability.Runtime,
		CapabilityType:              capability.Type,
		CapabilityName:              capability.Name,
		AdvertisedObservations:      2,
		LoadedObservations:          1,
		Uses:                        1,
		DistinctInvocationSessions:  1,
		FirstObservedAt:             &observed,
		LastObservedAt:              &observed,
		FirstEffectiveActivityAt:    &effective,
		LastEffectiveActivityAt:     &effective,
		ObservedAdvertisedSessions:  &advertisedSessions,
		InvokedInAdvertisedSessions: &invokedAdvertisedSessions,
		InvocationEvidence:          map[domain.Provenance]int64{domain.ProvenanceHook: 1, domain.ProvenanceTranscript: 1},
	}
	report, err := AnalyzeHistory([]domain.Capability{capability}, []history.Aggregate{aggregate}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("AnalyzeHistory() error = %v", err)
	}
	evidence := report.Capabilities[0]
	if evidence.EventCount(domain.EventAdvertised) != 2 || evidence.EventCount(domain.EventLoaded) != 1 || evidence.EventCount(domain.EventInvoked) != 1 {
		t.Fatalf("history event counts = %#v, want advertised=2 loaded=1 invoked=1", evidence.EventCounts)
	}
	if evidence.ActivityCount != 2 || evidence.InvocationCount != 1 || evidence.DistinctSessionCount != 1 {
		t.Fatalf("history activity counts = %d/%d/%d, want activity=2 invocation=1 sessions=1", evidence.ActivityCount, evidence.InvocationCount, evidence.DistinctSessionCount)
	}
	if evidence.ObservedAdvertisedSessions == nil || *evidence.ObservedAdvertisedSessions != 1 || !evidence.HasLastUsed || !evidence.LastUsedAt.Equal(effective) {
		t.Fatalf("history advertisement/use evidence = %#v", evidence)
	}
}

func TestAnalyzeHistoryLoadedOnlyDoesNotCreateInvocationTimes(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	capability := testCapability("loaded-only-history")
	loadedAt := now.Add(-2 * DefaultStaleAfter)
	aggregate := history.Aggregate{
		Runtime:            capability.Runtime,
		CapabilityType:     capability.Type,
		CapabilityName:     capability.Name,
		LoadedObservations: 1,
		Coverage:           &history.Coverage{FirstInventoryObservedAt: &loadedAt, LastInventoryObservedAt: &now},
	}
	report, err := AnalyzeHistory([]domain.Capability{capability}, []history.Aggregate{aggregate}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("AnalyzeHistory() error = %v", err)
	}
	evidence := report.Capabilities[0]
	if evidence.Classification != REVIEW || evidence.ActivityCount != 1 || evidence.InvocationCount != 0 || evidence.HasFirstUsed || evidence.HasLastUsed {
		t.Fatalf("loaded-only history evidence = %#v, want REVIEW with no invocation timestamps", evidence)
	}
}

func TestAnalyzeHistoryPreservesInvocationSourcesSessionsAndInvocationOnlyTimes(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	capability := testCapability("history-use")
	observedFirst := now.Add(-3 * time.Hour)
	observedLast := now.Add(-time.Hour)
	effectiveFirst := now.Add(-4 * time.Hour)
	effectiveLast := now.Add(-2 * time.Hour)
	aggregate := history.Aggregate{
		Runtime:                    capability.Runtime,
		CapabilityType:             capability.Type,
		CapabilityName:             capability.Name,
		Uses:                       2,
		DistinctInvocationSessions: 2,
		FirstObservedAt:            &observedFirst,
		LastObservedAt:             &observedLast,
		FirstEffectiveActivityAt:   &effectiveFirst,
		LastEffectiveActivityAt:    &effectiveLast,
		InvocationEvidence: map[domain.Provenance]int64{
			domain.ProvenanceHook:       1,
			domain.ProvenanceTranscript: 1,
		},
		Installed:       true,
		InstalledScopes: []domain.Scope{domain.ScopeProject, domain.ScopeUser},
	}

	report, err := AnalyzeHistory([]domain.Capability{capability}, []history.Aggregate{aggregate}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze(history) error = %v", err)
	}
	evidence := report.Capabilities[0]
	if evidence.InvocationCount != 2 || evidence.ActivityCount != 2 || evidence.DistinctSessionCount != 2 {
		t.Fatalf("history counts = %d/%d/%d, want 2/2/2", evidence.InvocationCount, evidence.ActivityCount, evidence.DistinctSessionCount)
	}
	if evidence.EventCounts[domain.EventLoaded] != 0 || evidence.EventCounts[domain.EventInvoked] != 2 {
		t.Fatalf("history loaded/invoked counts = %#v, want loaded 0 and invoked 2", evidence.EventCounts)
	}
	if !reflect.DeepEqual(evidence.EvidenceSources, []domain.Provenance{domain.ProvenanceHook, domain.ProvenanceTranscript}) {
		t.Fatalf("history evidence sources = %#v", evidence.EvidenceSources)
	}
	if !evidence.LastUsedAt.Equal(effectiveLast) || !evidence.FirstUsedAt.Equal(effectiveFirst) {
		t.Fatalf("history effective times = %s/%s, want %s/%s", evidence.FirstUsedAt, evidence.LastUsedAt, effectiveFirst, effectiveLast)
	}
	if evidence.FirstObservedAt == nil || !evidence.FirstObservedAt.Equal(observedFirst) || evidence.LastObservedAt == nil || !evidence.LastObservedAt.Equal(observedLast) {
		t.Fatalf("history observed times = %v/%v", evidence.FirstObservedAt, evidence.LastObservedAt)
	}
	if !reflect.DeepEqual(evidence.InstalledScopes, []domain.Scope{domain.ScopeProject, domain.ScopeUser}) {
		t.Fatalf("history scopes = %#v", evidence.InstalledScopes)
	}
	if evidence.Classification != KEEP {
		t.Fatalf("history classification = %q, want KEEP", evidence.Classification)
	}
}

func TestAnalyzeHistoryRejectsDuplicateAggregateKeys(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	codex := testCapability("repeated")
	observed := now.Add(-time.Hour)
	effective := now.Add(-time.Hour)
	first := history.Aggregate{
		Runtime: codex.Runtime, CapabilityType: codex.Type, CapabilityName: codex.Name,
		Uses: 1, DistinctInvocationSessions: 1,
		FirstObservedAt: &observed, LastObservedAt: &observed,
		FirstEffectiveActivityAt: &effective, LastEffectiveActivityAt: &effective,
		InvocationEvidence: map[domain.Provenance]int64{domain.ProvenanceHook: 1},
	}
	second := first
	second.Uses = 2
	second.DistinctInvocationSessions = 2
	second.InvocationEvidence = map[domain.Provenance]int64{domain.ProvenanceTranscript: 2}
	if _, err := AnalyzeHistory([]domain.Capability{codex}, []history.Aggregate{first, second}, DefaultConfig(), now); err == nil || !strings.Contains(err.Error(), "duplicate history aggregate key") {
		t.Fatalf("AnalyzeHistory() duplicate error = %v, want explicit duplicate-key rejection", err)
	}
}

func TestAnalyzeHistoryRejectsInconsistentAggregateTimestampsAndCounts(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	capability := testCapability("invalid-history")
	observed := now.Add(-time.Hour)
	tests := []struct {
		name      string
		aggregate history.Aggregate
		want      string
	}{
		{
			name: "partial invocation timestamps",
			aggregate: history.Aggregate{
				Runtime: capability.Runtime, CapabilityType: capability.Type, CapabilityName: capability.Name,
				Uses: 1, FirstObservedAt: &observed,
			},
			want: "first and last timestamps",
		},
		{
			name: "zero-use invocation timestamp",
			aggregate: history.Aggregate{
				Runtime: capability.Runtime, CapabilityType: capability.Type, CapabilityName: capability.Name,
				FirstObservedAt: &observed, LastObservedAt: &observed,
			},
			want: "zero-use aggregate",
		},
		{
			name: "sessions exceed uses",
			aggregate: history.Aggregate{
				Runtime: capability.Runtime, CapabilityType: capability.Type, CapabilityName: capability.Name,
				Uses: 1, DistinctInvocationSessions: 2,
			},
			want: "exceeds invocation use count",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := AnalyzeHistory([]domain.Capability{capability}, []history.Aggregate{test.aggregate}, DefaultConfig(), now); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("AnalyzeHistory() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestAnalyzeHistoryOrdersMultipleRuntimesDeterministically(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	codex := testCapability("codex-use")
	claude := codex
	claude.Runtime = domain.RuntimeClaude
	claude.Name = "claude-use"
	observed := now.Add(-time.Hour)
	effective := now.Add(-time.Hour)
	other := history.Aggregate{
		Runtime: claude.Runtime, CapabilityType: claude.Type, CapabilityName: claude.Name,
		Uses: 1, DistinctInvocationSessions: 1,
		FirstObservedAt: &observed, LastObservedAt: &observed,
		FirstEffectiveActivityAt: &effective, LastEffectiveActivityAt: &effective,
		InvocationEvidence: map[domain.Provenance]int64{domain.ProvenanceImport: 1},
	}

	codexAggregate := history.Aggregate{
		Runtime: codex.Runtime, CapabilityType: codex.Type, CapabilityName: codex.Name,
		Uses: 3, DistinctInvocationSessions: 2,
		FirstObservedAt: &observed, LastObservedAt: &observed,
		FirstEffectiveActivityAt: &effective, LastEffectiveActivityAt: &effective,
		InvocationEvidence: map[domain.Provenance]int64{domain.ProvenanceHook: 2, domain.ProvenanceTranscript: 1},
	}
	firstReport, err := AnalyzeHistory([]domain.Capability{claude, codex}, []history.Aggregate{other, codexAggregate}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("AnalyzeHistory() error = %v", err)
	}
	reversed, err := AnalyzeHistory([]domain.Capability{codex, claude}, []history.Aggregate{codexAggregate, other}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("AnalyzeHistory(reversed) error = %v", err)
	}
	if !reflect.DeepEqual(firstReport, reversed) {
		t.Fatalf("history analysis depends on input ordering:\nfirst=%#v\nreversed=%#v", firstReport, reversed)
	}
	if len(firstReport.Capabilities) != 2 || firstReport.Capabilities[0].Capability.Runtime != domain.RuntimeClaude {
		t.Fatalf("multiruntime ordering = %#v", firstReport.Capabilities)
	}
	codexEvidence := firstReport.Capabilities[1]
	if codexEvidence.InvocationCount != 3 || codexEvidence.DistinctSessionCount != 2 {
		t.Fatalf("multiruntime counts = %#v", codexEvidence)
	}
	if !reflect.DeepEqual(codexEvidence.EvidenceSources, []domain.Provenance{domain.ProvenanceHook, domain.ProvenanceTranscript}) {
		t.Fatalf("repeated bucket sources = %#v", codexEvidence.EvidenceSources)
	}
}

func TestAnalyzeHistoryAdvertisedExposureRetainsUnknownAndKnownStates(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	knownCapability := testCapability("known-advertised")
	unknownCapability := testCapability("unknown-advertised")
	known := int64(2)
	knownInvoked := int64(0)
	aggregates := []history.Aggregate{
		{Runtime: knownCapability.Runtime, CapabilityType: knownCapability.Type, CapabilityName: knownCapability.Name, AdvertisedObservations: known, ObservedAdvertisedSessions: &known, InvokedInAdvertisedSessions: &knownInvoked},
		{Runtime: unknownCapability.Runtime, CapabilityType: unknownCapability.Type, CapabilityName: unknownCapability.Name},
	}

	report, err := AnalyzeHistory([]domain.Capability{unknownCapability, knownCapability}, aggregates, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("AnalyzeHistory() error = %v", err)
	}
	byName := make(map[string]CapabilityEvidence, len(report.Capabilities))
	for _, evidence := range report.Capabilities {
		byName[evidence.Capability.Name] = evidence
	}
	knownEvidence := byName[knownCapability.Name]
	if knownEvidence.ObservedAdvertisedSessions == nil || *knownEvidence.ObservedAdvertisedSessions != known || knownEvidence.EventCounts[domain.EventAdvertised] != int(known) {
		t.Fatalf("known advertised evidence = %#v", knownEvidence)
	}
	if knownEvidence.InvocationCount != 0 || knownEvidence.Classification != REVIEW {
		t.Fatalf("known advertised classification/counts = %#v", knownEvidence)
	}
	if byName[unknownCapability.Name].ObservedAdvertisedSessions != nil {
		t.Fatalf("unknown advertised evidence = %#v, want nil", byName[unknownCapability.Name])
	}
}

func TestAnalyzeHistoryRejectsImpossibleAdvertisedSessionEvidence(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	session := int64(1)
	zero := int64(0)
	tooMany := int64(2)
	base := history.Aggregate{
		Runtime:        domain.RuntimeCodex,
		CapabilityType: domain.CapabilitySkill,
		CapabilityName: "advertisement-invariant",
	}
	tests := []struct {
		name      string
		aggregate history.Aggregate
		wantError string
	}{
		{
			name:      "zero advertisements require nil sessions",
			aggregate: history.Aggregate{Runtime: base.Runtime, CapabilityType: base.CapabilityType, CapabilityName: base.CapabilityName, ObservedAdvertisedSessions: &session, InvokedInAdvertisedSessions: &session},
			wantError: "invalid history aggregate at index 0: observed advertised sessions must be nil when advertised observations are zero",
		},
		{
			name:      "positive advertisements require sessions",
			aggregate: history.Aggregate{Runtime: base.Runtime, CapabilityType: base.CapabilityType, CapabilityName: base.CapabilityName, AdvertisedObservations: 1},
			wantError: "invalid history aggregate at index 0: observed advertised sessions are required when advertised observations are positive",
		},
		{
			name:      "sessions must be positive",
			aggregate: history.Aggregate{Runtime: base.Runtime, CapabilityType: base.CapabilityType, CapabilityName: base.CapabilityName, AdvertisedObservations: 1, ObservedAdvertisedSessions: &zero, InvokedInAdvertisedSessions: &zero},
			wantError: "invalid history aggregate at index 0: observed advertised sessions must be positive",
		},
		{
			name:      "sessions cannot exceed advertisements",
			aggregate: history.Aggregate{Runtime: base.Runtime, CapabilityType: base.CapabilityType, CapabilityName: base.CapabilityName, AdvertisedObservations: 1, ObservedAdvertisedSessions: &tooMany, InvokedInAdvertisedSessions: &tooMany},
			wantError: "invalid history aggregate at index 0: observed advertised sessions exceed advertised observations",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := AnalyzeHistory(nil, []history.Aggregate{test.aggregate}, DefaultConfig(), now); err == nil || err.Error() != test.wantError {
				t.Fatalf("AnalyzeHistory() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestAnalyzeHistoryStaleUsesStrictInvocationBoundaryAndCoverageBasis(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	threshold := 60 * 24 * time.Hour
	capability := testCapability("stale-boundary")
	coverageFirst := now.Add(-365 * 24 * time.Hour)
	coverageLast := now.Add(-300 * 24 * time.Hour)
	coverage := &history.Coverage{FirstInventoryObservedAt: &coverageFirst, LastInventoryObservedAt: &coverageLast}

	for _, test := range []struct {
		name      string
		last      time.Time
		wantClass Classification
	}{
		{name: "exact threshold", last: now.Add(-threshold), wantClass: KEEP},
		{name: "older than threshold", last: now.Add(-threshold - time.Nanosecond), wantClass: STALE},
	} {
		t.Run(test.name, func(t *testing.T) {
			firstObserved := test.last
			lastObserved := test.last
			firstEffective := test.last
			lastEffective := test.last
			aggregate := history.Aggregate{
				Runtime: capability.Runtime, CapabilityType: capability.Type, CapabilityName: capability.Name,
				Uses: 1, DistinctInvocationSessions: 1,
				FirstObservedAt: &firstObserved, LastObservedAt: &lastObserved,
				FirstEffectiveActivityAt: &firstEffective, LastEffectiveActivityAt: &lastEffective,
				InvocationEvidence: map[domain.Provenance]int64{domain.ProvenanceHook: 1}, Coverage: coverage,
			}
			report, err := AnalyzeHistory([]domain.Capability{capability}, []history.Aggregate{aggregate}, Config{StaleAfter: threshold, ReviewFootprintTokens: 1000, ReviewMaxUseCount: 1}, now)
			if err != nil {
				t.Fatalf("AnalyzeHistory() error = %v", err)
			}
			evidence := report.Capabilities[0]
			if evidence.Classification != test.wantClass {
				t.Fatalf("classification = %q, want %q", evidence.Classification, test.wantClass)
			}
			if !strings.Contains(evidence.Basis, "coverage") || strings.Contains(strings.ToLower(evidence.Basis), "complete") {
				t.Fatalf("basis = %q, want observation-only coverage limitation", evidence.Basis)
			}
		})
	}
}

func TestAnalyzeHistoryLongInventoryRangeWithZeroUseNeverEmitsDead(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	capability := testCapability("long-zero")
	first := now.Add(-3 * 365 * 24 * time.Hour)
	last := now.Add(-time.Hour)
	aggregate := history.Aggregate{
		Runtime: capability.Runtime, CapabilityType: capability.Type, CapabilityName: capability.Name,
		Coverage: &history.Coverage{FirstInventoryObservedAt: &first, LastInventoryObservedAt: &last},
	}
	report, err := AnalyzeHistory([]domain.Capability{capability}, []history.Aggregate{aggregate}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("AnalyzeHistory() error = %v", err)
	}
	evidence := report.Capabilities[0]
	if evidence.Classification != REVIEW || evidence.Classification == DEAD {
		t.Fatalf("long zero-use classification = %q, want REVIEW and never DEAD", evidence.Classification)
	}
	if !strings.Contains(evidence.Basis, "not observed in period") {
		t.Fatalf("long zero-use basis = %q, want period-limited wording", evidence.Basis)
	}
}
