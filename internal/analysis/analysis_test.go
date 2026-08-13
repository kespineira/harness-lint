package analysis

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

func TestAnalyzeNeverObservedCapabilityRequiresReview(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	capability := testCapability("never-observed")

	report, err := Analyze([]domain.Capability{capability}, nil, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if len(report.Capabilities) != 1 {
		t.Fatalf("capability evidence count = %d, want 1", len(report.Capabilities))
	}
	evidence := report.Capabilities[0]
	if evidence.Classification != REVIEW {
		t.Fatalf("classification = %q, want %q", evidence.Classification, REVIEW)
	}
	if evidence.EventCounts[domain.EventAdvertised] != 0 || evidence.EventCounts[domain.EventLoaded] != 0 || evidence.EventCounts[domain.EventInvoked] != 0 {
		t.Fatalf("event counts = %#v, want all zero", evidence.EventCounts)
	}
	if evidence.InvocationCount != 0 || evidence.ActivityCount != 0 || evidence.DistinctSessionCount != 0 || evidence.HasFirstUsed || evidence.HasLastUsed {
		t.Fatalf("unused evidence = %#v", evidence)
	}
	if !strings.Contains(evidence.Basis, "never observed") || !strings.Contains(evidence.Basis, "insufficient") {
		t.Fatalf("never-observed basis = %q, want explicit insufficient lifetime evidence", evidence.Basis)
	}
	if evidence.EvidenceConfidence != domain.ConfidenceUnknown {
		t.Fatalf("evidence confidence = %q, want unknown", evidence.EvidenceConfidence)
	}
}

func TestAnalyzeClassifiesStaleAtStrictBoundaryAndClampsFutureAge(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	threshold := 60 * 24 * time.Hour

	tests := []struct {
		name         string
		timestamp    time.Time
		wantClass    Classification
		wantAge      time.Duration
		wantInFuture bool
	}{
		{
			name:      "exact threshold remains keep",
			timestamp: now.Add(-threshold),
			wantClass: KEEP,
			wantAge:   threshold,
		},
		{
			name:      "older than threshold is stale",
			timestamp: now.Add(-threshold - time.Nanosecond),
			wantClass: STALE,
			wantAge:   threshold + time.Nanosecond,
		},
		{
			name:         "future timestamp",
			timestamp:    now.Add(time.Hour),
			wantClass:    KEEP,
			wantAge:      0,
			wantInFuture: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capability := testCapability(test.name)
			report, err := Analyze([]domain.Capability{capability}, []domain.UsageEvent{testEvent(test.timestamp, capability.Name, domain.EventInvoked, "session")}, Config{StaleAfter: threshold, ReviewFootprintTokens: 1000, ReviewMaxUseCount: 1}, now)
			if err != nil {
				t.Fatalf("Analyze() error = %v", err)
			}
			evidence := report.Capabilities[0]
			if evidence.Classification != test.wantClass {
				t.Fatalf("classification = %q, want %q", evidence.Classification, test.wantClass)
			}
			if evidence.LastUsedAge != test.wantAge || evidence.LastUsedInFuture != test.wantInFuture {
				t.Fatalf("last-used age/future = %s/%v, want %s/%v", evidence.LastUsedAge, evidence.LastUsedInFuture, test.wantAge, test.wantInFuture)
			}
		})
	}
}

func TestAnalyzeUsesSourceTimestampForInvocationButPreservesObservedAt(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	capability := testCapability("source-time")
	event := testEvent(now.Add(-time.Hour), capability.Name, domain.EventInvoked, "session")
	source := now.Add(-2 * time.Hour)
	event.SourceTimestamp = &source

	report, err := Analyze([]domain.Capability{capability}, []domain.UsageEvent{event}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	evidence := report.Capabilities[0]
	if !evidence.LastUsedAt.Equal(source) {
		t.Fatalf("last-used activity time = %s, want source time %s", evidence.LastUsedAt, source)
	}
	if !event.ObservedAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("analysis mutated observed-at time: %s", event.ObservedAt)
	}
}

func TestAnalyzePreservesEventTypesAndSessions(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	capability := testCapability("events")
	events := []domain.UsageEvent{
		testEvent(now.Add(-3*time.Hour), capability.Name, domain.EventAdvertised, "advertising-session"),
		testEvent(now.Add(-2*time.Hour), capability.Name, domain.EventLoaded, "load-session"),
		testEvent(now.Add(-time.Hour), capability.Name, domain.EventInvoked, "invoke-session-a"),
		testEvent(now.Add(-30*time.Minute), capability.Name, domain.EventInvoked, "invoke-session-b"),
	}

	report, err := Analyze([]domain.Capability{capability}, events, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	evidence := report.Capabilities[0]
	if got, want := evidence.EventCounts[domain.EventAdvertised], 1; got != want {
		t.Fatalf("advertised count = %d, want %d", got, want)
	}
	if got, want := evidence.EventCounts[domain.EventLoaded], 1; got != want {
		t.Fatalf("loaded count = %d, want %d", got, want)
	}
	if got, want := evidence.EventCounts[domain.EventInvoked], 2; got != want {
		t.Fatalf("invoked count = %d, want %d", got, want)
	}
	if evidence.InvocationCount != 2 || evidence.ActivityCount != 3 {
		t.Fatalf("invocation/activity counts = %d/%d, want 2/3", evidence.InvocationCount, evidence.ActivityCount)
	}
	if evidence.DistinctSessionCount != 2 {
		t.Fatalf("distinct invocation session count = %d, want 2", evidence.DistinctSessionCount)
	}
	if !evidence.FirstUsedAt.Equal(now.Add(-time.Hour)) || !evidence.LastUsedAt.Equal(now.Add(-30*time.Minute)) {
		t.Fatalf("first/last used timestamps = %s/%s, want %s/%s", evidence.FirstUsedAt, evidence.LastUsedAt, now.Add(-time.Hour), now.Add(-30*time.Minute))
	}
}

func TestAnalyzeLoadedWithoutInvocationRequiresReviewWithoutCallingItUse(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	capability := testCapability("loaded-only")
	report, err := Analyze([]domain.Capability{capability}, []domain.UsageEvent{testEvent(now.Add(-time.Hour), capability.Name, domain.EventLoaded, "session")}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	evidence := report.Capabilities[0]
	if evidence.Classification != REVIEW || evidence.InvocationCount != 0 || evidence.ActivityCount != 1 || evidence.EventCounts[domain.EventLoaded] != 1 || evidence.DistinctSessionCount != 0 || evidence.HasLastUsed {
		t.Fatalf("loaded-only evidence = %#v", evidence)
	}
	if !strings.Contains(evidence.Basis, "loaded activity") || strings.Contains(evidence.Basis, "use") {
		t.Fatalf("loaded-only basis = %q, want loaded evidence without use wording", evidence.Basis)
	}
}

func TestAnalyzeReviewUsesConfigurableEstimatedFootprintAndLowInvocationUse(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	capability := testCapability("large-low-use")
	capability.MetadataTokens = domain.Measurement{Value: 700, Confidence: domain.ConfidenceEstimated, Basis: "tokenizer estimate"}
	capability.BodyTokens = domain.Measurement{Value: 400, Confidence: domain.ConfidenceExact, Basis: "manifest"}
	config := Config{StaleAfter: 24 * time.Hour, ReviewFootprintTokens: 1000, ReviewMaxUseCount: 1}
	loaded := testEvent(now.Add(-time.Hour), capability.Name, domain.EventLoaded, "session")

	report, err := Analyze([]domain.Capability{capability}, []domain.UsageEvent{loaded}, config, now)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	evidence := report.Capabilities[0]
	if evidence.Classification != REVIEW {
		t.Fatalf("classification = %q, want %q", evidence.Classification, REVIEW)
	}
	if evidence.Confidence != domain.ConfidenceEstimated || !strings.Contains(evidence.Basis, "estimated") {
		t.Fatalf("review confidence/basis = %q/%q", evidence.Confidence, evidence.Basis)
	}
}

func TestAnalyzeExactFootprintTriggersReviewWithoutEstimatedConfidence(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	capability := testCapability("large-exact")
	capability.MetadataTokens = domain.Measurement{Value: 2000, Confidence: domain.ConfidenceExact, Basis: "manifest"}
	capability.BodyTokens = domain.Measurement{Value: 2000, Confidence: domain.ConfidenceExact, Basis: "manifest"}
	event := testEvent(now.Add(-time.Hour), capability.Name, domain.EventLoaded, "session")

	report, err := Analyze([]domain.Capability{capability}, []domain.UsageEvent{event}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	evidence := report.Capabilities[0]
	if evidence.Classification != REVIEW {
		t.Fatalf("classification = %q, want %q for exact-only footprint", evidence.Classification, REVIEW)
	}
	if evidence.Confidence != domain.ConfidenceExact {
		t.Fatalf("classification confidence = %q, want exact", evidence.Confidence)
	}
	if strings.Contains(evidence.Basis, "estimated") {
		t.Fatalf("exact-only review basis mislabeled as estimated: %q", evidence.Basis)
	}
}

func TestAnalyzeReviewBoundariesAreInclusiveOnlyWhereDocumented(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	config := Config{StaleAfter: 24 * time.Hour, ReviewFootprintTokens: 1000, ReviewMaxUseCount: 1}

	tests := []struct {
		name               string
		footprint          int64
		invocationCount    int
		wantClassification Classification
	}{
		{name: "exact footprint and use thresholds review", footprint: 1000, invocationCount: 1, wantClassification: REVIEW},
		{name: "below footprint threshold keep", footprint: 999, invocationCount: 1, wantClassification: KEEP},
		{name: "above low-use threshold keep", footprint: 1000, invocationCount: 2, wantClassification: KEEP},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capability := testCapability(test.name)
			capability.MetadataTokens = domain.Measurement{Value: test.footprint, Confidence: domain.ConfidenceEstimated, Basis: "tokenizer estimate"}
			events := []domain.UsageEvent{testEvent(now.Add(-time.Hour), capability.Name, domain.EventLoaded, "session")}
			for i := 0; i < test.invocationCount; i++ {
				events = append(events, testEvent(now.Add(-time.Minute-time.Duration(i)), capability.Name, domain.EventInvoked, "session"))
			}

			report, err := Analyze([]domain.Capability{capability}, events, config, now)
			if err != nil {
				t.Fatalf("Analyze() error = %v", err)
			}
			if got := report.Capabilities[0].Classification; got != test.wantClassification {
				t.Fatalf("classification = %q, want %q", got, test.wantClassification)
			}
		})
	}
}

func TestDefaultConfigUsesSixtyDayStaleThreshold(t *testing.T) {
	config := DefaultConfig()
	if config.StaleAfter != 60*24*time.Hour {
		t.Fatalf("default stale threshold = %s, want 60 days", config.StaleAfter)
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("DefaultConfig().Validate() error = %v", err)
	}
}

func TestAnalyzeReportsDuplicateDefinitionsAndDeterministicOrdering(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	first := testCapability("same-name")
	first.Source = "z-source"
	first.Scope = domain.ScopeUser
	second := first
	second.Source = "a-source"
	second.Scope = domain.ScopeProject
	other := testCapability("other")
	input := []domain.Capability{first, other, second}

	report, err := Analyze(input, nil, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if len(report.Duplicates) != 1 || len(report.Duplicates[0].Definitions) != 2 {
		t.Fatalf("duplicates = %#v, want one two-definition duplicate", report.Duplicates)
	}
	if report.Duplicates[0].Definitions[0].Source != "a-source" || report.Duplicates[0].Definitions[1].Source != "z-source" {
		t.Fatalf("duplicate definitions order = %#v", report.Duplicates[0].Definitions)
	}
	if report.Capabilities[0].Capability.Name != "other" || report.Capabilities[1].Capability.Source != "a-source" || report.Capabilities[2].Capability.Source != "z-source" {
		t.Fatalf("capability evidence order = %#v", report.Capabilities)
	}

	reversed, err := Analyze([]domain.Capability{second, first, other}, nil, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze(reversed) error = %v", err)
	}
	if !reflect.DeepEqual(report, reversed) {
		t.Fatalf("analysis is input-order dependent:\nfirst: %#v\nsecond: %#v", report, reversed)
	}
}

func TestAnalyzePreservesAdvertisementStatesAndOrdersDefinitionsDeterministically(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	base := testCapability("visibility")
	fully := base
	fully.Advertisement = domain.AdvertisementStateFullyAdvertised
	nameOnly := base
	nameOnly.Advertisement = domain.AdvertisementStateNameOnly
	notAdvertised := base
	notAdvertised.Advertisement = domain.AdvertisementStateNotAdvertised

	report, err := Analyze([]domain.Capability{notAdvertised, fully, nameOnly}, nil, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	wantStates := []domain.AdvertisementState{
		domain.AdvertisementStateFullyAdvertised,
		domain.AdvertisementStateNameOnly,
		domain.AdvertisementStateNotAdvertised,
	}
	if len(report.Capabilities) != len(wantStates) {
		t.Fatalf("capability evidence count = %d, want %d", len(report.Capabilities), len(wantStates))
	}
	for index, evidence := range report.Capabilities {
		if evidence.Capability.Advertisement != wantStates[index] {
			t.Fatalf("advertisement state at index %d = %q, want %q", index, evidence.Capability.Advertisement, wantStates[index])
		}
		if evidence.EventCounts[domain.EventLoaded] != 0 || evidence.EventCounts[domain.EventInvoked] != 0 || evidence.Classification != REVIEW {
			t.Fatalf("hidden/exposure-only evidence = %#v", evidence)
		}
		if !strings.Contains(evidence.Basis, "never observed") {
			t.Fatalf("exposure-only basis = %q, want never observed", evidence.Basis)
		}
	}

	reversed, err := Analyze([]domain.Capability{nameOnly, fully, notAdvertised}, nil, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze(reversed) error = %v", err)
	}
	if !reflect.DeepEqual(report, reversed) {
		t.Fatalf("advertisement ordering depends on input order:\nfirst: %#v\nsecond: %#v", report, reversed)
	}
}

func TestAnalyzeSharesUnscopedEventAcrossDuplicateDefinitions(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	first := testCapability("shared-event")
	first.Source = "source-a"
	second := first
	second.Source = "source-b"
	event := testEvent(now.Add(-time.Hour), first.Name, domain.EventInvoked, "session")

	report, err := Analyze([]domain.Capability{first, second}, []domain.UsageEvent{event}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if len(report.Capabilities) != 2 {
		t.Fatalf("capability evidence count = %d, want 2", len(report.Capabilities))
	}
	for _, evidence := range report.Capabilities {
		if evidence.InvocationCount != 1 || evidence.EventCounts[domain.EventLoaded] != 0 || evidence.EventCounts[domain.EventInvoked] != 1 {
			t.Fatalf("duplicate definition event attribution for %q = %#v", evidence.Capability.Source, evidence)
		}
	}
}

func TestDetectDuplicateNamesRequiresDifferentDefinitionScopeOrSource(t *testing.T) {
	base := testCapability("same-name")
	sameDefinition := base
	secondSource := base
	secondSource.Source = "another-source"
	secondScope := base
	secondScope.Scope = domain.ScopeUser

	duplicates, err := DetectDuplicateNames([]domain.Capability{base, sameDefinition})
	if err != nil {
		t.Fatalf("DetectDuplicateNames(same definition) error = %v", err)
	}
	if len(duplicates) != 0 {
		t.Fatalf("same source/scope definitions reported as duplicates: %#v", duplicates)
	}

	duplicates, err = DetectDuplicateNames([]domain.Capability{base, secondSource, secondScope})
	if err != nil {
		t.Fatalf("DetectDuplicateNames(different definitions) error = %v", err)
	}
	if len(duplicates) != 1 || len(duplicates[0].Definitions) != 3 {
		t.Fatalf("different source/scope definitions = %#v, want one group with three definitions", duplicates)
	}
}

func TestSummarizeContextAggregatesKnownValuesAndRetainsUncertainty(t *testing.T) {
	first := testCapability("first")
	first.MetadataTokens = domain.Measurement{Value: 10, Confidence: domain.ConfidenceExact, Basis: "manifest"}
	first.BodyTokens = domain.Measurement{Value: 5, Confidence: domain.ConfidenceExact, Basis: "manifest"}
	second := testCapability("second")
	second.MetadataTokens = domain.Measurement{Value: 20, Confidence: domain.ConfidenceEstimated, Basis: "tokenizer"}
	second.BodyTokens = domain.Measurement{Value: 7, Confidence: domain.ConfidenceObserved, Basis: "runtime metadata"}
	third := testCapability("third")
	third.MetadataTokens = domain.Measurement{Confidence: domain.ConfidenceUnknown}
	third.BodyTokens = domain.Measurement{Confidence: domain.ConfidenceUnknown}
	otherRuntime := testCapability("other-runtime")
	otherRuntime.Runtime = domain.RuntimeClaude
	otherRuntime.MetadataTokens = domain.Measurement{Value: 3, Confidence: domain.ConfidenceExact, Basis: "manifest"}
	otherRuntime.BodyTokens = domain.Measurement{Confidence: domain.ConfidenceUnknown}

	context, err := SummarizeContext([]domain.Capability{third, otherRuntime, second, first})
	if err != nil {
		t.Fatalf("SummarizeContext() error = %v", err)
	}
	if len(context.Groups) != 2 {
		t.Fatalf("context groups = %#v, want two", context.Groups)
	}
	group := context.Groups[0]
	if group.Runtime != domain.RuntimeClaude || context.Groups[1].Runtime != domain.RuntimeCodex {
		t.Fatalf("context group ordering = %#v", context.Groups)
	}
	group = context.Groups[1]
	if group.MetadataTokens.Value != 30 || group.MetadataTokens.KnownCount != 2 || group.MetadataTokens.UnknownCount != 1 {
		t.Fatalf("metadata aggregate = %#v, want known sum 30 and one unknown", group.MetadataTokens)
	}
	if group.MetadataTokens.Confidence != domain.ConfidenceUnknown || !group.MetadataTokens.Estimated || group.MetadataTokens.Complete {
		t.Fatalf("metadata uncertainty = %#v", group.MetadataTokens)
	}
	if !strings.Contains(group.MetadataTokens.Basis, "configured baseline exposure for Skill metadata") || !strings.Contains(group.MetadataTokens.Basis, "estimated") || !strings.Contains(group.MetadataTokens.Basis, "unknown") {
		t.Fatalf("metadata basis = %q, want Skill baseline, estimate, and unknown labels", group.MetadataTokens.Basis)
	}
	if group.BodyTokens.Value != 12 || group.BodyTokens.KnownCount != 2 || group.BodyTokens.UnknownCount != 1 {
		t.Fatalf("body aggregate = %#v, want known sum 12 and one unknown", group.BodyTokens)
	}
	if !strings.Contains(group.BodyTokens.Basis, "on-load body footprint") || strings.Contains(group.BodyTokens.Basis, "advertised") {
		t.Fatalf("body basis = %q, want separate on-load wording without advertised label", group.BodyTokens.Basis)
	}
}

func TestSummarizeContextLabelsBaselineAndBodySemanticsByTypeAndExposure(t *testing.T) {
	skill := testCapability("skill")
	skill.Advertisement = domain.AdvertisementStateFullyAdvertised
	skill.MetadataTokens = domain.Measurement{Value: 10, Confidence: domain.ConfidenceExact, Basis: "configured metadata"}
	skill.BodyTokens = domain.Measurement{Value: 20, Confidence: domain.ConfidenceEstimated, Basis: "on-load estimate"}

	nameOnlySkill := testCapability("name-only-skill")
	nameOnlySkill.Advertisement = domain.AdvertisementStateNameOnly
	nameOnlySkill.MetadataTokens = domain.Measurement{Value: 5, Confidence: domain.ConfidenceEstimated, Basis: "name estimate"}
	nameOnlySkill.BodyTokens = domain.Measurement{Confidence: domain.ConfidenceUnknown}

	hiddenSkill := testCapability("hidden-skill")
	hiddenSkill.Advertisement = domain.AdvertisementStateNotAdvertised
	hiddenSkill.MetadataTokens = domain.Measurement{Confidence: domain.ConfidenceUnknown}
	hiddenSkill.BodyTokens = domain.Measurement{Confidence: domain.ConfidenceUnknown}

	instructions := testCapability("instructions")
	instructions.Type = domain.CapabilityInstructionFile
	instructions.MetadataTokens = domain.Measurement{Confidence: domain.ConfidenceUnknown}
	instructions.BodyTokens = domain.Measurement{Value: 40, Confidence: domain.ConfidenceExact, Basis: "instruction content"}

	other := testCapability("other")
	other.Type = domain.CapabilityCommand
	other.MetadataTokens = domain.Measurement{Value: 30, Confidence: domain.ConfidenceExact, Basis: "configured metadata"}
	other.BodyTokens = domain.Measurement{Confidence: domain.ConfidenceUnknown}

	context, err := SummarizeContext([]domain.Capability{instructions, hiddenSkill, nameOnlySkill, other, skill})
	if err != nil {
		t.Fatalf("SummarizeContext() error = %v", err)
	}
	var skillGroup, otherGroup, instructionGroup *ContextGroup
	for index := range context.Groups {
		group := &context.Groups[index]
		switch {
		case group.Runtime == domain.RuntimeCodex && group.CapabilityType == domain.CapabilitySkill:
			skillGroup = group
		case group.Runtime == domain.RuntimeCodex && group.CapabilityType == domain.CapabilityCommand:
			otherGroup = group
		case group.CapabilityType == domain.CapabilityInstructionFile:
			instructionGroup = group
		}
	}
	if skillGroup == nil || !strings.Contains(skillGroup.MetadataTokens.Basis, "configured baseline exposure") || !strings.Contains(skillGroup.BodyTokens.Basis, "on-load body footprint") {
		t.Fatalf("fully advertised skill context = %#v, want baseline metadata and on-load body labels", skillGroup)
	}
	if skillGroup.CapabilityCount != 3 || skillGroup.MetadataTokens.Value != 15 || skillGroup.MetadataTokens.KnownCount != 2 || skillGroup.MetadataTokens.UnknownCount != 1 || skillGroup.MetadataTokens.Confidence != domain.ConfidenceUnknown || !skillGroup.MetadataTokens.Estimated {
		t.Fatalf("mixed skill metadata context = %#v, want all three exposure states with known/estimated/unknown subtotal", skillGroup)
	}
	if skillGroup.BodyTokens.Value != 20 || skillGroup.BodyTokens.KnownCount != 1 || skillGroup.BodyTokens.UnknownCount != 2 {
		t.Fatalf("mixed skill body context = %#v, want on-load subtotal plus two unknown values", skillGroup.BodyTokens)
	}
	if otherGroup == nil || strings.Contains(otherGroup.MetadataTokens.Basis, "configured baseline exposure") || !strings.Contains(otherGroup.MetadataTokens.Basis, "metadata footprint") || !strings.Contains(otherGroup.BodyTokens.Basis, "loading semantics runtime-dependent") {
		t.Fatalf("other capability context = %#v, want neutral metadata and runtime-dependent body labels", otherGroup)
	}
	if instructionGroup == nil || !strings.Contains(instructionGroup.BodyTokens.Basis, "configured baseline instruction content") || strings.Contains(instructionGroup.BodyTokens.Basis, "on-load body footprint") {
		t.Fatalf("instruction-file body context = %#v, want configured baseline instruction wording", instructionGroup)
	}
}

func TestAnalyzeDoesNotMutateInputSlices(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	capabilities := []domain.Capability{testCapability("zeta"), testCapability("alpha")}
	events := []domain.UsageEvent{
		testEvent(now, "zeta", domain.EventInvoked, "session-z"),
		testEvent(now.Add(-time.Hour), "alpha", domain.EventLoaded, "session-a"),
	}
	wantCapabilities := append([]domain.Capability(nil), capabilities...)
	wantEvents := append([]domain.UsageEvent(nil), events...)

	if _, err := Analyze(capabilities, events, DefaultConfig(), now); err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if !reflect.DeepEqual(capabilities, wantCapabilities) {
		t.Fatalf("Analyze() mutated capabilities: got %#v, want %#v", capabilities, wantCapabilities)
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("Analyze() mutated events: got %#v, want %#v", events, wantEvents)
	}
}

func TestSummarizeContextRejectsMeasurementOverflow(t *testing.T) {
	first := testCapability("overflow-a")
	first.MetadataTokens = domain.Measurement{Value: int64(^uint64(0) >> 1), Confidence: domain.ConfidenceExact, Basis: "manifest"}
	second := testCapability("overflow-b")
	second.MetadataTokens = domain.Measurement{Value: 1, Confidence: domain.ConfidenceExact, Basis: "manifest"}

	if _, err := SummarizeContext([]domain.Capability{first, second}); err == nil || !strings.Contains(err.Error(), "overflows int64") {
		t.Fatalf("SummarizeContext() error = %v, want int64 overflow", err)
	}
}

func TestAnalyzeRejectsInvalidConfigInputAndTime(t *testing.T) {
	capability := testCapability("valid")
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		caps   []domain.Capability
		events []domain.UsageEvent
		config Config
		now    time.Time
		want   string
	}{
		{name: "negative stale threshold", caps: []domain.Capability{capability}, config: Config{StaleAfter: -time.Hour, ReviewFootprintTokens: 1, ReviewMaxUseCount: 0}, now: now, want: "stale threshold"},
		{name: "zero partial stale threshold", caps: []domain.Capability{capability}, config: Config{ReviewFootprintTokens: 1, ReviewMaxUseCount: 0}, now: now, want: "stale threshold"},
		{name: "zero now", caps: []domain.Capability{capability}, config: DefaultConfig(), want: "analysis time"},
		{name: "invalid capability", caps: []domain.Capability{{Name: "missing runtime"}}, config: DefaultConfig(), now: now, want: "invalid capability"},
		{name: "invalid event", caps: []domain.Capability{capability}, events: []domain.UsageEvent{{Runtime: domain.RuntimeCodex}}, config: DefaultConfig(), now: now, want: "invalid usage event"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Analyze(test.caps, test.events, test.config, test.now)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Analyze() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestAnalyzeAggregatesDistinctEvidenceSourcesWithoutDeduplicating(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	capability := testCapability("mixed-evidence")
	events := []domain.UsageEvent{
		testEvent(now.Add(-4*time.Hour), capability.Name, domain.EventAdvertised, "advertised-session"),
		testEventWithProvenance(now.Add(-3*time.Hour), capability.Name, domain.EventLoaded, "loaded-session", domain.ProvenanceTranscript),
		testEventWithProvenance(now.Add(-2*time.Hour), capability.Name, domain.EventInvoked, "invoke-a", domain.ProvenanceHook),
		testEventWithProvenance(now.Add(-time.Hour), capability.Name, domain.EventInvoked, "invoke-b", domain.ProvenanceTranscript),
		testEventWithProvenance(now.Add(-30*time.Minute), capability.Name, domain.EventInvoked, "invoke-a", domain.ProvenanceImport),
	}

	report, err := Analyze([]domain.Capability{capability}, events, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	evidence := report.Capabilities[0]
	if evidence.InvocationCount != 3 || evidence.ActivityCount != 4 {
		t.Fatalf("invocation/activity counts = %d/%d, want 3/4", evidence.InvocationCount, evidence.ActivityCount)
	}
	if evidence.DistinctSessionCount != 2 {
		t.Fatalf("distinct invocation sessions = %d, want 2", evidence.DistinctSessionCount)
	}
	wantSources := []domain.Provenance{domain.ProvenanceHook, domain.ProvenanceImport, domain.ProvenanceTranscript}
	if !reflect.DeepEqual(evidence.EvidenceSources, wantSources) {
		t.Fatalf("evidence sources = %#v, want sorted distinct %#v", evidence.EvidenceSources, wantSources)
	}
	if strings.Contains(evidence.EvidenceCoverage, "direct hook") || !strings.Contains(evidence.EvidenceCoverage, "transcript backfill/fallback") {
		t.Fatalf("mixed evidence coverage = %q, want transcript fallback wording without direct-hook conflation", evidence.EvidenceCoverage)
	}
}

func TestAnalyzeUsesEffectiveSourceTimeForLateTranscriptImport(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	capability := testCapability("late-transcript")
	event := testEvent(now, capability.Name, domain.EventInvoked, "session")
	sourceTime := now.Add(-48 * time.Hour)
	event.SourceTimestamp = &sourceTime

	report, err := Analyze([]domain.Capability{capability}, []domain.UsageEvent{event}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	evidence := report.Capabilities[0]
	if !evidence.FirstUsedAt.Equal(sourceTime) || !evidence.LastUsedAt.Equal(sourceTime) {
		t.Fatalf("first/last used = %s/%s, want source timestamp %s", evidence.FirstUsedAt, evidence.LastUsedAt, sourceTime)
	}
	if evidence.LastUsedAge != 48*time.Hour {
		t.Fatalf("last-used age = %s, want 48h", evidence.LastUsedAge)
	}
}

func TestAnalyzeUsesObservedAtWhenSourceTimeIsUnavailable(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	capability := testCapability("observed-time")
	observedAt := now.Add(-3 * time.Hour)
	event := testEvent(observedAt, capability.Name, domain.EventInvoked, "session")

	report, err := Analyze([]domain.Capability{capability}, []domain.UsageEvent{event}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	evidence := report.Capabilities[0]
	if !evidence.FirstUsedAt.Equal(observedAt) || !evidence.LastUsedAt.Equal(observedAt) {
		t.Fatalf("first/last used = %s/%s, want observed-at %s", evidence.FirstUsedAt, evidence.LastUsedAt, observedAt)
	}
}

func TestAnalyzeAdvertisedOnlyIsNotUse(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	capability := testCapability("advertised-only")
	event := testEvent(now.Add(-time.Hour), capability.Name, domain.EventAdvertised, "advertising-session")

	report, err := Analyze([]domain.Capability{capability}, []domain.UsageEvent{event}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	evidence := report.Capabilities[0]
	if evidence.EventCount(domain.EventAdvertised) != 1 || evidence.EventCount(domain.EventLoaded) != 0 || evidence.EventCount(domain.EventInvoked) != 0 {
		t.Fatalf("advertised-only event counts = %#v", evidence.EventCounts)
	}
	if evidence.InvocationCount != 0 || evidence.ActivityCount != 0 || evidence.DistinctSessionCount != 0 || evidence.HasFirstUsed || evidence.HasLastUsed {
		t.Fatalf("advertised-only usage evidence = %#v", evidence)
	}
	if evidence.Classification != REVIEW || !strings.Contains(evidence.Basis, "never observed") || !strings.Contains(evidence.Basis, "advertised evidence only") {
		t.Fatalf("advertised-only classification/basis = %q/%q", evidence.Classification, evidence.Basis)
	}
}

func TestAnalyzeLoadedOnlyDoesNotBecomeStaleFromLoadTime(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	capability := testCapability("loaded-old")
	loaded := testEvent(now.Add(-2*DefaultStaleAfter), capability.Name, domain.EventLoaded, "load-session")

	report, err := Analyze([]domain.Capability{capability}, []domain.UsageEvent{loaded}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	evidence := report.Capabilities[0]
	if evidence.Classification != REVIEW {
		t.Fatalf("loaded-only classification = %q, want REVIEW", evidence.Classification)
	}
	if evidence.HasLastUsed || evidence.LastUsedAge != 0 || evidence.DistinctSessionCount != 0 {
		t.Fatalf("loaded-only use evidence = %#v, want invocation-only fields empty", evidence)
	}
}

func TestAnalyzeCoverageConfidenceNeverClaimsCompleteCapture(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	capability := testCapability("hook-coverage")
	event := testEvent(now.Add(-time.Hour), capability.Name, domain.EventInvoked, "session")
	event.Provenance = domain.ProvenanceHook

	report, err := Analyze([]domain.Capability{capability}, []domain.UsageEvent{event}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	evidence := report.Capabilities[0]
	if evidence.EvidenceConfidence != domain.ConfidenceObserved {
		t.Fatalf("evidence confidence = %q, want observed facts", evidence.EvidenceConfidence)
	}
	if !strings.Contains(evidence.EvidenceCoverage, "coverage is unknown") || strings.Contains(evidence.EvidenceCoverage, "complete") {
		t.Fatalf("hook coverage = %q, want unknown lifetime coverage", evidence.EvidenceCoverage)
	}
}

func testEvent(timestamp time.Time, capabilityName string, eventType domain.EventType, session string) domain.UsageEvent {
	return domain.UsageEvent{
		ObservedAt:       timestamp,
		Runtime:          domain.RuntimeCodex,
		SessionID:        session,
		ProjectID:        "project",
		CapabilityType:   domain.CapabilitySkill,
		CapabilityName:   capabilityName,
		EventType:        eventType,
		Provenance:       domain.ProvenanceImport,
		InvocationOrigin: domain.InvocationOriginUnknown,
		SchemaVersion:    domain.CurrentUsageEventSchemaVersion,
	}
}

func testEventWithProvenance(timestamp time.Time, capabilityName string, eventType domain.EventType, session string, provenance domain.Provenance) domain.UsageEvent {
	event := testEvent(timestamp, capabilityName, eventType, session)
	event.Provenance = provenance
	return event
}

func testCapability(name string) domain.Capability {
	return domain.Capability{
		Runtime:        domain.RuntimeCodex,
		Type:           domain.CapabilitySkill,
		Name:           name,
		Scope:          domain.ScopeProject,
		Source:         "project",
		Enabled:        domain.EnabledStateEnabled,
		Advertisement:  domain.AdvertisementStateUnknown,
		MetadataTokens: domain.Measurement{Confidence: domain.ConfidenceUnknown},
		BodyTokens:     domain.Measurement{Confidence: domain.ConfidenceUnknown},
	}
}
