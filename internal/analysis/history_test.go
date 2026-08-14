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

	report, err := Analyze([]domain.Capability{capability}, []history.Aggregate{aggregate}, DefaultConfig(), now)
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

func TestAnalyzeHistoryMergesRepeatedBucketsAndOrdersMultipleRuntimes(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	codex := testCapability("repeated")
	claude := codex
	claude.Runtime = domain.RuntimeClaude
	claude.Name = "other-runtime"
	first := history.Aggregate{
		Runtime: codex.Runtime, CapabilityType: codex.Type, CapabilityName: codex.Name,
		Uses: 1, DistinctInvocationSessions: 1,
		InvocationEvidence: map[domain.Provenance]int64{domain.ProvenanceHook: 1},
	}
	second := first
	second.Uses = 2
	second.DistinctInvocationSessions = 2
	second.InvocationEvidence = map[domain.Provenance]int64{domain.ProvenanceTranscript: 2}
	other := history.Aggregate{
		Runtime: claude.Runtime, CapabilityType: claude.Type, CapabilityName: claude.Name,
		Uses: 1, DistinctInvocationSessions: 1,
		InvocationEvidence: map[domain.Provenance]int64{domain.ProvenanceImport: 1},
	}

	firstReport, err := AnalyzeHistory([]domain.Capability{claude, codex}, []history.Aggregate{other, second, first}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("AnalyzeHistory() error = %v", err)
	}
	reversed, err := AnalyzeHistory([]domain.Capability{codex, claude}, []history.Aggregate{first, other, second}, DefaultConfig(), now)
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
	if codexEvidence.InvocationCount != 3 || codexEvidence.DistinctSessionCount != 3 {
		t.Fatalf("repeated bucket counts = %#v", codexEvidence)
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
	aggregates := []history.Aggregate{
		{Runtime: knownCapability.Runtime, CapabilityType: knownCapability.Type, CapabilityName: knownCapability.Name, ObservedAdvertisedSessions: &known},
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
