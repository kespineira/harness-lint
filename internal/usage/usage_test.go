package usage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
)

func TestBuildUsageDocumentFillsClosedUTCMonthsAndKeepsNullablePrivacySafeFields(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	firstObserved := now.Add(-48 * time.Hour)
	firstEffective := now.Add(-47 * time.Hour)
	advertisedSessions := int64(1)
	invokedAdvertisedSessions := int64(1)
	aggregate := history.Aggregate{
		Runtime:                    domain.RuntimeCodex,
		CapabilityType:             domain.CapabilityTool,
		CapabilityName:             "terminal",
		Uses:                       1,
		DistinctInvocationSessions: 1,
		FirstObservedAt:            &firstObserved,
		LastObservedAt:             &firstObserved,
		FirstEffectiveActivityAt:   &firstEffective,
		LastEffectiveActivityAt:    &firstEffective,
		InvocationEvidence: map[domain.Provenance]int64{
			domain.ProvenanceHook:       1,
			domain.ProvenanceTranscript: 1,
		},
		AdvertisedObservations:      1,
		ObservedAdvertisedSessions:  &advertisedSessions,
		InvokedInAdvertisedSessions: &invokedAdvertisedSessions,
		Installed:                   true,
		InstalledScopes:             []domain.Scope{domain.ScopeUser},
	}
	monthly := []history.MonthlyAggregate{{
		Month:                      time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		Runtime:                    aggregate.Runtime,
		CapabilityType:             aggregate.CapabilityType,
		CapabilityName:             aggregate.CapabilityName,
		Uses:                       1,
		DistinctInvocationSessions: 1,
	}}
	document, err := Build(BuildInput{
		GeneratedAt:    now,
		Days:           60,
		TypeFilter:     stringPtr("tool"),
		Aggregates:     []history.Aggregate{aggregate},
		Monthly:        monthly,
		IncludeMonthly: true,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if document.SchemaVersion != SchemaVersion || document.GeneratedAt != now.Format(time.RFC3339Nano) || !document.Period.Inclusive {
		t.Fatalf("envelope = %#v", document)
	}
	if document.Period.Start != now.Add(-60*24*time.Hour).Format(time.RFC3339Nano) || document.Period.End != now.Format(time.RFC3339Nano) {
		t.Fatalf("period = %#v", document.Period)
	}
	if document.Filters.Type == nil || *document.Filters.Type != "tool" || document.Filters.Runtime != nil {
		t.Fatalf("filters = %#v", document.Filters)
	}
	if len(document.Capabilities) != 1 || len(document.Capabilities[0].Monthly) != 3 {
		t.Fatalf("capability/monthly rows = %#v", document.Capabilities)
	}
	if document.Capabilities[0].Monthly[0].Uses != 0 || document.Capabilities[0].Monthly[1].Uses != 0 || document.Capabilities[0].Monthly[2].Uses != 1 {
		t.Fatalf("monthly zero buckets = %#v", document.Capabilities[0].Monthly)
	}
	if document.Capabilities[0].AdvertisedSessions == nil || *document.Capabilities[0].AdvertisedSessions != 1 {
		t.Fatalf("advertised sessions = %#v", document.Capabilities[0].AdvertisedSessions)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	output := string(encoded)
	for _, forbidden := range []string{"/private/source", "PROMPT_SENTINEL", "ARGS_SENTINEL", "OUTPUT_SENTINEL", "session-secret", "fingerprint-secret", "source_identity"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("usage JSON leaked %q: %s", forbidden, output)
		}
	}
	if !strings.Contains(output, `"first_observed_at"`) || !strings.Contains(output, `"observation_only_coverage":null`) {
		t.Fatalf("nullable fields are not explicit: %s", output)
	}
}

func TestBuildUsageDocumentRejectsZeroDaysAndDuplicateRows(t *testing.T) {
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	aggregate := history.Aggregate{Runtime: domain.RuntimeCodex, CapabilityType: domain.CapabilitySkill, CapabilityName: "lint"}
	if _, err := Build(BuildInput{GeneratedAt: now, Days: 0, Aggregates: []history.Aggregate{aggregate}}); err == nil || !strings.Contains(err.Error(), "greater than zero") {
		t.Fatalf("zero days error = %v", err)
	}
	if _, err := Build(BuildInput{GeneratedAt: now, Days: 1, Aggregates: []history.Aggregate{aggregate, aggregate}}); err == nil || !strings.Contains(err.Error(), "duplicate usage aggregate key") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func stringPtr(value string) *string { return &value }
