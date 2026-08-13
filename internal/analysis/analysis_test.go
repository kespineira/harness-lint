package analysis

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

func TestAnalyzeNeverUsedCapabilityIsDead(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	capability := testCapability("never-used")

	report, err := Analyze([]domain.Capability{capability}, nil, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if len(report.Capabilities) != 1 {
		t.Fatalf("capability evidence count = %d, want 1", len(report.Capabilities))
	}
	evidence := report.Capabilities[0]
	if evidence.Classification != DEAD {
		t.Fatalf("classification = %q, want %q", evidence.Classification, DEAD)
	}
	if evidence.EventCounts[domain.EventAdvertised] != 0 || evidence.EventCounts[domain.EventLoaded] != 0 || evidence.EventCounts[domain.EventInvoked] != 0 {
		t.Fatalf("event counts = %#v, want all zero", evidence.EventCounts)
	}
	if evidence.InvocationCount != 0 || evidence.DistinctSessionCount != 0 || evidence.HasLastUsed {
		t.Fatalf("unused evidence = %#v", evidence)
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
	if evidence.InvocationCount != 2 || evidence.UseCount != 2 || evidence.ActivityCount != 3 {
		t.Fatalf("use/activity counts = %d/%d/%d, want 2/2/3", evidence.InvocationCount, evidence.UseCount, evidence.ActivityCount)
	}
	if evidence.DistinctSessionCount != 3 {
		t.Fatalf("distinct session count = %d, want 3", evidence.DistinctSessionCount)
	}
	if !evidence.LastUsedAt.Equal(now.Add(-30 * time.Minute)) {
		t.Fatalf("last-used timestamp = %s, want %s", evidence.LastUsedAt, now.Add(-30*time.Minute))
	}
}

func TestAnalyzeLoadedWithoutInvocationIsNotDead(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	capability := testCapability("loaded-only")
	report, err := Analyze([]domain.Capability{capability}, []domain.UsageEvent{testEvent(now.Add(-time.Hour), capability.Name, domain.EventLoaded, "session")}, DefaultConfig(), now)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	evidence := report.Capabilities[0]
	if evidence.Classification != KEEP || evidence.InvocationCount != 0 || evidence.EventCounts[domain.EventLoaded] != 1 {
		t.Fatalf("loaded-only evidence = %#v", evidence)
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
	if !strings.Contains(group.MetadataTokens.Basis, "estimated") || !strings.Contains(group.MetadataTokens.Basis, "unknown") {
		t.Fatalf("metadata basis = %q, want estimate and unknown labels", group.MetadataTokens.Basis)
	}
	if group.BodyTokens.Value != 12 || group.BodyTokens.KnownCount != 2 || group.BodyTokens.UnknownCount != 1 {
		t.Fatalf("body aggregate = %#v, want known sum 12 and one unknown", group.BodyTokens)
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

func testEvent(timestamp time.Time, capabilityName string, eventType domain.EventType, session string) domain.UsageEvent {
	return domain.UsageEvent{
		Timestamp:      timestamp,
		Runtime:        domain.RuntimeCodex,
		SessionID:      session,
		ProjectID:      "project",
		CapabilityType: domain.CapabilitySkill,
		CapabilityName: capabilityName,
		EventType:      eventType,
	}
}

func testCapability(name string) domain.Capability {
	return domain.Capability{
		Runtime:        domain.RuntimeCodex,
		Type:           domain.CapabilitySkill,
		Name:           name,
		Scope:          domain.ScopeProject,
		Source:         "project",
		Enabled:        domain.EnabledStateEnabled,
		MetadataTokens: domain.Measurement{Confidence: domain.ConfidenceUnknown},
		BodyTokens:     domain.Measurement{Confidence: domain.ConfidenceUnknown},
	}
}
