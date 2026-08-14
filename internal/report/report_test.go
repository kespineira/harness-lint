package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kespineira/harness-lint/internal/analysis"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
)

func TestBuildReportHistoryUsesAggregatesWithoutEventTableAndKeepsPrivacy(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	capability := reportTestCapability("installed")
	observed := now.Add(-2 * time.Hour)
	effective := now.Add(-3 * time.Hour)
	advertised := int64(2)
	coverageFirst := now.Add(-365 * 24 * time.Hour)
	coverageLast := now.Add(-24 * time.Hour)
	aggregates := []history.Aggregate{
		{
			Runtime:                    capability.Runtime,
			CapabilityType:             capability.Type,
			CapabilityName:             capability.Name,
			Uses:                       1,
			DistinctInvocationSessions: 1,
			FirstObservedAt:            &observed,
			LastObservedAt:             &observed,
			FirstEffectiveActivityAt:   &effective,
			LastEffectiveActivityAt:    &effective,
			InvocationEvidence:         map[domain.Provenance]int64{domain.ProvenanceHook: 1, domain.ProvenanceTranscript: 1},
			ObservedAdvertisedSessions: &advertised,
			Installed:                  true,
			InstalledScopes:            []domain.Scope{domain.ScopeProject, domain.ScopeUser},
			Coverage:                   &history.Coverage{FirstInventoryObservedAt: &coverageFirst, LastInventoryObservedAt: &coverageLast},
		},
		{
			Runtime:                    domain.RuntimeCodex,
			CapabilityType:             domain.CapabilityTool,
			CapabilityName:             "usage-only",
			Uses:                       2,
			DistinctInvocationSessions: 2,
			FirstObservedAt:            &observed,
			LastObservedAt:             &observed,
			FirstEffectiveActivityAt:   &effective,
			LastEffectiveActivityAt:    &effective,
			InvocationEvidence:         map[domain.Provenance]int64{domain.ProvenanceImport: 2},
		},
	}
	result, err := analysis.AnalyzeHistory([]domain.Capability{capability}, aggregates, analysis.DefaultConfig(), now)
	if err != nil {
		t.Fatalf("AnalyzeHistory() error = %v", err)
	}
	document, err := BuildReport(result, aggregates, now, 60)
	if err != nil {
		t.Fatalf("BuildReport() error = %v", err)
	}
	if len(document.Capabilities) != 1 || len(document.UsageOnly) != 1 || len(document.Runtimes) != 2 {
		t.Fatalf("document sizes = capabilities=%d usage-only=%d runtimes=%d", len(document.Capabilities), len(document.UsageOnly), len(document.Runtimes))
	}
	capabilityDTO := document.Capabilities[0]
	if !capabilityDTO.Installed || capabilityDTO.Loaded != 0 || capabilityDTO.InvocationCount != 1 || capabilityDTO.DistinctSessionCount != 1 {
		t.Fatalf("aggregate capability DTO = %#v", capabilityDTO)
	}
	if capabilityDTO.ObservedAdvertisedSessions == nil || *capabilityDTO.ObservedAdvertisedSessions != 2 || capabilityDTO.Advertised != 2 {
		t.Fatalf("aggregate advertisement DTO = %#v", capabilityDTO)
	}
	if len(capabilityDTO.InstalledScopes) != 2 || capabilityDTO.Coverage == nil || capabilityDTO.FirstObservedAt == nil || capabilityDTO.FirstEffectiveActivityAt == nil {
		t.Fatalf("aggregate scope/time/coverage DTO = %#v", capabilityDTO)
	}
	if got, want := document.UsageOnly[0].InvocationCount, 2; got != want {
		t.Fatalf("usage-only invocation count = %d, want %d", got, want)
	}
	if document.UsageOnly[0].Loaded != 0 || document.UsageOnly[0].ObservedAdvertisedSessions != nil {
		t.Fatalf("usage-only independently loaded/advertised DTO = %#v", document.UsageOnly[0])
	}

	var output bytes.Buffer
	if err := WriteJSON(&output, document); err != nil {
		t.Fatalf("WriteJSON() error = %v", err)
	}
	if strings.Contains(output.String(), "/private/secret") || strings.Contains(output.String(), "session-secret") || strings.Contains(output.String(), strings.Repeat("f", 64)) || strings.Contains(output.String(), "never used") {
		t.Fatalf("aggregate report JSON contains private/misleading sentinel: %s", output.String())
	}
	var decoded ReportDocument
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("decode report JSON: %v", err)
	}
}

func TestBuildStaleHistoryKeepsStrictClassificationBasisCoverageLimited(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	capability := reportTestCapability("stale")
	last := now.Add(-analysis.DefaultStaleAfter - time.Nanosecond)
	first := last
	aggregate := history.Aggregate{
		Runtime:                    capability.Runtime,
		CapabilityType:             capability.Type,
		CapabilityName:             capability.Name,
		Uses:                       1,
		DistinctInvocationSessions: 1,
		FirstObservedAt:            &first,
		LastObservedAt:             &last,
		FirstEffectiveActivityAt:   &first,
		LastEffectiveActivityAt:    &last,
		InvocationEvidence:         map[domain.Provenance]int64{domain.ProvenanceHook: 1},
		Coverage:                   &history.Coverage{FirstInventoryObservedAt: &first, LastInventoryObservedAt: &last},
	}
	result, err := analysis.AnalyzeHistory([]domain.Capability{capability}, []history.Aggregate{aggregate}, analysis.DefaultConfig(), now)
	if err != nil {
		t.Fatalf("AnalyzeHistory() error = %v", err)
	}
	if result.Capabilities[0].Classification != analysis.STALE {
		t.Fatalf("classification = %q, want STALE", result.Capabilities[0].Classification)
	}
	document, err := BuildStale(result, []history.Aggregate{aggregate}, now, 60)
	if err != nil {
		t.Fatalf("BuildStale() error = %v", err)
	}
	if len(document.Capabilities) != 1 || !strings.Contains(document.Capabilities[0].Basis, "coverage") || strings.Contains(strings.ToLower(document.Capabilities[0].Basis), "complete") {
		t.Fatalf("stale DTO basis = %#v, want strict stale basis limited by observation coverage", document.Capabilities[0])
	}
}

func reportTestCapability(name string) domain.Capability {
	return domain.Capability{
		Runtime:        domain.RuntimeCodex,
		Type:           domain.CapabilitySkill,
		Name:           name,
		Scope:          domain.ScopeProject,
		Source:         "/private/secret/definition.md",
		Enabled:        domain.EnabledStateEnabled,
		Advertisement:  domain.AdvertisementStateFullyAdvertised,
		MetadataTokens: domain.Measurement{Confidence: domain.ConfidenceUnknown},
		BodyTokens:     domain.Measurement{Confidence: domain.ConfidenceUnknown},
	}
}
