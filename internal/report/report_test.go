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
			AdvertisedObservations:     2,
			LoadedObservations:         1,
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
	document, err := BuildReportHistory(result, aggregates, now, 60)
	if err != nil {
		t.Fatalf("BuildReportHistory() error = %v", err)
	}
	if len(document.Capabilities) != 1 || len(document.UsageOnly) != 1 || len(document.Runtimes) != 2 {
		t.Fatalf("document sizes = capabilities=%d usage-only=%d runtimes=%d", len(document.Capabilities), len(document.UsageOnly), len(document.Runtimes))
	}
	capabilityDTO := document.Capabilities[0]
	if !capabilityDTO.Installed || capabilityDTO.Loaded != 1 || capabilityDTO.InvocationCount != 1 || capabilityDTO.DistinctSessionCount != 1 {
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
	var codexRuntime *Runtime
	for index := range document.Runtimes {
		if document.Runtimes[index].Runtime == string(domain.RuntimeCodex) {
			codexRuntime = &document.Runtimes[index]
			break
		}
	}
	if codexRuntime == nil || codexRuntime.Advertised != 2 || codexRuntime.Loaded != 1 || codexRuntime.Invoked != 3 || codexRuntime.UsageEvents != 6 {
		t.Fatalf("runtime history counts = %#v, want advertised=2 loaded=1 invoked=3 usage_events=6", codexRuntime)
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

func TestHistoryReportRejectsDuplicateAggregateKeysAndKeepsTypedAPIs(t *testing.T) {
	var _ func(analysis.Report, []domain.UsageEvent, time.Time, int) (ReportDocument, error) = BuildReport
	var _ func(analysis.Report, []history.Aggregate, time.Time, int) (ReportDocument, error) = BuildReportHistory
	var _ func(analysis.Report, []domain.UsageEvent, time.Time, int) (StaleDocument, error) = BuildStale
	var _ func(analysis.Report, []history.Aggregate, time.Time, int) (StaleDocument, error) = BuildStaleHistory

	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	base := history.Aggregate{
		Runtime:        domain.RuntimeCodex,
		CapabilityType: domain.CapabilitySkill,
		CapabilityName: "duplicate",
	}
	if _, err := BuildReportHistory(analysis.Report{}, []history.Aggregate{base, base}, now, 0); err == nil || !strings.Contains(err.Error(), "duplicate history aggregate key") {
		t.Fatalf("BuildReportHistory() duplicate error = %v, want explicit duplicate-key rejection", err)
	}
}

func TestHistoryReportsRejectImpossibleAdvertisedSessionEvidence(t *testing.T) {
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
			aggregate: history.Aggregate{Runtime: base.Runtime, CapabilityType: base.CapabilityType, CapabilityName: base.CapabilityName, ObservedAdvertisedSessions: &session},
			wantError: "invalid history aggregate at index 0: observed advertised sessions must be nil when advertised observations are zero",
		},
		{
			name:      "positive advertisements require sessions",
			aggregate: history.Aggregate{Runtime: base.Runtime, CapabilityType: base.CapabilityType, CapabilityName: base.CapabilityName, AdvertisedObservations: 1},
			wantError: "invalid history aggregate at index 0: observed advertised sessions are required when advertised observations are positive",
		},
		{
			name:      "sessions must be positive",
			aggregate: history.Aggregate{Runtime: base.Runtime, CapabilityType: base.CapabilityType, CapabilityName: base.CapabilityName, AdvertisedObservations: 1, ObservedAdvertisedSessions: &zero},
			wantError: "invalid history aggregate at index 0: observed advertised sessions must be positive",
		},
		{
			name:      "sessions cannot exceed advertisements",
			aggregate: history.Aggregate{Runtime: base.Runtime, CapabilityType: base.CapabilityType, CapabilityName: base.CapabilityName, AdvertisedObservations: 1, ObservedAdvertisedSessions: &tooMany},
			wantError: "invalid history aggregate at index 0: observed advertised sessions exceed advertised observations",
		},
	}
	builders := []struct {
		name  string
		build func([]history.Aggregate) error
	}{
		{
			name: "report",
			build: func(aggregates []history.Aggregate) error {
				_, err := BuildReportHistory(analysis.Report{}, aggregates, now, 0)
				return err
			},
		},
		{
			name: "stale",
			build: func(aggregates []history.Aggregate) error {
				_, err := BuildStaleHistory(analysis.Report{}, aggregates, now, 0)
				return err
			},
		},
	}
	for _, builder := range builders {
		for _, test := range tests {
			t.Run(builder.name+"/"+test.name, func(t *testing.T) {
				if err := builder.build([]history.Aggregate{test.aggregate}); err == nil || err.Error() != test.wantError {
					t.Fatalf("history validation error = %v, want %q", err, test.wantError)
				}
			})
		}
	}
}

func TestCapabilityMeasurementJSONKeepsIndependentSafeEvidence(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	known := reportTestCapability("known-footprint")
	known.MetadataTokens = domain.Measurement{
		Value:      42,
		Confidence: domain.ConfidenceEstimated,
		Basis:      "configured metadata /private/secret/definition.md PROMPT_SENTINEL",
	}
	known.BodyTokens = domain.Measurement{
		Value:      18,
		Confidence: domain.ConfidenceObserved,
		Basis:      "loaded body OUTPUT_SENTINEL",
	}
	unknown := reportTestCapability("unknown-footprint")
	aggregates := []history.Aggregate{
		{Runtime: known.Runtime, CapabilityType: known.Type, CapabilityName: known.Name, Installed: true},
		{Runtime: unknown.Runtime, CapabilityType: unknown.Type, CapabilityName: unknown.Name, Installed: true},
	}
	result, err := analysis.AnalyzeHistory([]domain.Capability{known, unknown}, aggregates, analysis.DefaultConfig(), now)
	if err != nil {
		t.Fatalf("AnalyzeHistory() error = %v", err)
	}
	document, err := BuildReportHistory(result, aggregates, now, 0)
	if err != nil {
		t.Fatalf("BuildReportHistory() error = %v", err)
	}
	rows := make(map[string]Capability, len(document.Capabilities))
	for _, row := range document.Capabilities {
		rows[row.Name] = row
	}
	knownRow := rows[known.Name]
	if knownRow.MetadataExposure.Tokens == nil || *knownRow.MetadataExposure.Tokens != 42 || knownRow.MetadataExposure.Confidence != string(domain.ConfidenceEstimated) || knownRow.MetadataExposure.Basis == "" {
		t.Fatalf("metadata exposure DTO = %#v", knownRow.MetadataExposure)
	}
	if knownRow.LoadedBodyFootprint.Tokens == nil || *knownRow.LoadedBodyFootprint.Tokens != 18 || knownRow.LoadedBodyFootprint.Confidence != string(domain.ConfidenceObserved) || knownRow.LoadedBodyFootprint.Basis == "" {
		t.Fatalf("loaded body footprint DTO = %#v", knownRow.LoadedBodyFootprint)
	}
	if *knownRow.MetadataExposure.Tokens != 42 || *knownRow.LoadedBodyFootprint.Tokens != 18 {
		t.Fatalf("measurement DTOs were combined or overwritten: %#v", knownRow)
	}
	unknownRow := rows[unknown.Name]
	if unknownRow.MetadataExposure.Tokens != nil || unknownRow.MetadataExposure.Confidence != string(domain.ConfidenceUnknown) || unknownRow.LoadedBodyFootprint.Tokens != nil || unknownRow.LoadedBodyFootprint.Confidence != string(domain.ConfidenceUnknown) {
		t.Fatalf("unknown measurement DTOs = %#v", unknownRow)
	}

	var raw map[string]interface{}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal report JSON: %v", err)
	}
	if strings.Contains(string(encoded), "/private/secret/definition.md") || strings.Contains(string(encoded), "PROMPT_SENTINEL") || strings.Contains(string(encoded), "OUTPUT_SENTINEL") {
		t.Fatalf("report JSON contains private measurement basis sentinel: %s", encoded)
	}
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("decode report JSON: %v", err)
	}
	capabilities, ok := raw["capabilities"].([]interface{})
	if !ok || len(capabilities) != 2 {
		t.Fatalf("decoded capabilities = %#v", raw["capabilities"])
	}
	for _, capability := range capabilities {
		row, ok := capability.(map[string]interface{})
		if !ok {
			t.Fatalf("decoded capability row = %#v", capability)
		}
		if row["name"] != unknown.Name {
			continue
		}
		metadata, ok := row["metadata_exposure"].(map[string]interface{})
		if !ok || metadata["tokens"] != nil || metadata["confidence"] != string(domain.ConfidenceUnknown) {
			t.Fatalf("unknown metadata JSON = %#v", row["metadata_exposure"])
		}
		body, ok := row["loaded_body_footprint"].(map[string]interface{})
		if !ok || body["tokens"] != nil || body["confidence"] != string(domain.ConfidenceUnknown) {
			t.Fatalf("unknown body JSON = %#v", row["loaded_body_footprint"])
		}
	}
}

func TestHistoryReportKeepsLoadedSeparateFromInvocationTimes(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	capability := reportTestCapability("loaded-only")
	aggregate := history.Aggregate{
		Runtime:            capability.Runtime,
		CapabilityType:     capability.Type,
		CapabilityName:     capability.Name,
		LoadedObservations: 1,
		Installed:          true,
		InstalledScopes:    []domain.Scope{domain.ScopeProject},
	}
	result, err := analysis.AnalyzeHistory([]domain.Capability{capability}, []history.Aggregate{aggregate}, analysis.DefaultConfig(), now)
	if err != nil {
		t.Fatalf("AnalyzeHistory() error = %v", err)
	}
	evidence := result.Capabilities[0]
	if evidence.EventCount(domain.EventLoaded) != 1 || evidence.EventCount(domain.EventInvoked) != 0 || evidence.ActivityCount != 1 || evidence.HasLastUsed {
		t.Fatalf("loaded-only analysis = %#v, want one loaded event and no invocation", evidence)
	}
	document, err := BuildReportHistory(result, []history.Aggregate{aggregate}, now, 0)
	if err != nil {
		t.Fatalf("BuildReportHistory() error = %v", err)
	}
	row := document.Capabilities[0]
	if row.Loaded != 1 || row.InvocationCount != 0 || row.FirstInvocationObservedAt != nil || row.LastInvocationObservedAt != nil {
		t.Fatalf("loaded-only report row = %#v, want loaded-only counts/times", row)
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
	document, err := BuildStaleHistory(result, []history.Aggregate{aggregate}, now, 60)
	if err != nil {
		t.Fatalf("BuildStaleHistory() error = %v", err)
	}
	if len(document.Capabilities) != 1 || !strings.Contains(document.Capabilities[0].Basis, "coverage") || strings.Contains(strings.ToLower(document.Capabilities[0].Basis), "complete") {
		t.Fatalf("stale DTO basis = %#v, want strict stale basis limited by observation coverage", document.Capabilities[0])
	}
	if document.Capabilities[0].MetadataExposure.Tokens != nil || document.Capabilities[0].LoadedBodyFootprint.Tokens != nil {
		t.Fatalf("stale unknown measurements = %#v, want nullable unknown values", document.Capabilities[0])
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
