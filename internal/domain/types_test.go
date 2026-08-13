package domain

import (
	"strings"
	"testing"
	"time"
)

func TestEnumValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		valid func() bool
		want  bool
	}{
		{name: "runtime known", valid: func() bool { return RuntimeCodex.Valid() }, want: true},
		{name: "runtime unknown", valid: func() bool { return RuntimeUnknown.Valid() }, want: false},
		{name: "runtime arbitrary", valid: func() bool { return Runtime("other").Valid() }, want: false},
		{name: "capability type skill", valid: func() bool { return CapabilitySkill.Valid() }, want: true},
		{name: "capability type mcp server", valid: func() bool { return CapabilityMCPServer.Valid() }, want: true},
		{name: "capability type mcp tool", valid: func() bool { return CapabilityMCPTool.Valid() }, want: true},
		{name: "capability type agent", valid: func() bool { return CapabilityAgent.Valid() }, want: true},
		{name: "capability type tool", valid: func() bool { return CapabilityTool.Valid() }, want: true},
		{name: "capability type hook", valid: func() bool { return CapabilityHook.Valid() }, want: true},
		{name: "capability type instruction file", valid: func() bool { return CapabilityInstructionFile.Valid() }, want: true},
		{name: "capability type legacy mcp value", valid: func() bool { return CapabilityType("mcp").Valid() }, want: false},
		{name: "capability type unknown", valid: func() bool { return CapabilityUnknown.Valid() }, want: false},
		{name: "scope known", valid: func() bool { return ScopeProject.Valid() }, want: true},
		{name: "scope unknown", valid: func() bool { return ScopeUnknown.Valid() }, want: false},
		{name: "event type known", valid: func() bool { return EventInvoked.Valid() }, want: true},
		{name: "event type unknown", valid: func() bool { return EventType("installed").Valid() }, want: false},
		{name: "confidence known", valid: func() bool { return ConfidenceEstimated.Valid() }, want: true},
		{name: "confidence unknown", valid: func() bool { return Confidence("guess").Valid() }, want: false},
		{name: "enabled state enabled", valid: func() bool { return EnabledStateEnabled.Valid() }, want: true},
		{name: "enabled state disabled", valid: func() bool { return EnabledStateDisabled.Valid() }, want: true},
		{name: "enabled state unknown", valid: func() bool { return EnabledStateUnknown.Valid() }, want: true},
		{name: "enabled state arbitrary", valid: func() bool { return EnabledState("maybe").Valid() }, want: false},
		{name: "advertisement state unknown", valid: func() bool { return AdvertisementStateUnknown.Valid() }, want: true},
		{name: "advertisement state fully advertised", valid: func() bool { return AdvertisementStateFullyAdvertised.Valid() }, want: true},
		{name: "advertisement state name only", valid: func() bool { return AdvertisementStateNameOnly.Valid() }, want: true},
		{name: "advertisement state not advertised", valid: func() bool { return AdvertisementStateNotAdvertised.Valid() }, want: true},
		{name: "advertisement state arbitrary", valid: func() bool { return AdvertisementState("partial").Valid() }, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.valid(); got != test.want {
				t.Fatalf("Valid() = %v, want %v", got, test.want)
			}
		})
	}
	if CapabilityMCPServer == CapabilityMCPTool {
		t.Fatal("MCP server and MCP tool capability types must remain distinct")
	}
}

func TestMeasurementValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		measurement Measurement
		wantErr     bool
	}{
		{
			name:        "exact",
			measurement: Measurement{Value: 12, Confidence: ConfidenceExact, Basis: "runtime metadata"},
		},
		{
			name:        "observed",
			measurement: Measurement{Value: 12, Confidence: ConfidenceObserved, Basis: "runtime log"},
		},
		{
			name:        "estimated",
			measurement: Measurement{Value: 12, Confidence: ConfidenceEstimated, Basis: "tokenizer estimate"},
		},
		{
			name:        "unknown zero",
			measurement: Measurement{Confidence: ConfidenceUnknown},
		},
		{
			name:        "known without basis",
			measurement: Measurement{Value: 12, Confidence: ConfidenceExact},
			wantErr:     true,
		},
		{
			name:        "unknown nonzero",
			measurement: Measurement{Value: 12, Confidence: ConfidenceUnknown, Basis: "not available"},
			wantErr:     true,
		},
		{
			name:        "negative",
			measurement: Measurement{Value: -1, Confidence: ConfidenceExact, Basis: "runtime metadata"},
			wantErr:     true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.measurement.Validate(); (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestCapabilityValidation(t *testing.T) {
	t.Parallel()

	firstSeen := time.Date(2026, 8, 13, 12, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	capability := Capability{
		Runtime:        RuntimeCodex,
		Type:           CapabilitySkill,
		Name:           "lint",
		Scope:          ScopeProject,
		Enabled:        EnabledStateUnknown,
		Advertisement:  AdvertisementStateFullyAdvertised,
		MetadataTokens: Measurement{Value: 20, Confidence: ConfidenceObserved, Basis: "advertised metadata"},
		BodyTokens:     Measurement{Value: 30, Confidence: ConfidenceEstimated, Basis: "advertised body estimate"},
		FirstSeen:      firstSeen,
		LastSeen:       firstSeen.Add(time.Minute),
	}
	if err := capability.Validate(); err != nil {
		t.Fatalf("valid capability rejected: %v", err)
	}

	capability.LastSeen = capability.FirstSeen.Add(-time.Second)
	if err := capability.Validate(); err == nil || !strings.Contains(err.Error(), "precedes") {
		t.Fatalf("invalid seen range error = %v", err)
	}

	capability.LastSeen = capability.FirstSeen.Add(time.Minute)
	capability.Advertisement = AdvertisementState("partial")
	if err := capability.Validate(); err == nil || !strings.Contains(err.Error(), "advertisement state") {
		t.Fatalf("invalid advertisement state error = %v", err)
	}
}

func TestAdvertisedMeasurementsCanBePopulatedIndependently(t *testing.T) {
	t.Parallel()
	base := Capability{
		Runtime:       RuntimeCodex,
		Type:          CapabilitySkill,
		Name:          "lint",
		Scope:         ScopeProject,
		Enabled:       EnabledStateUnknown,
		Advertisement: AdvertisementStateUnknown,
	}
	metadataOnly := base
	metadataOnly.MetadataTokens = Measurement{Value: 12, Confidence: ConfidenceObserved, Basis: "advertised metadata"}
	metadataOnly.BodyTokens = Measurement{Confidence: ConfidenceUnknown}
	if err := metadataOnly.Validate(); err != nil {
		t.Fatalf("metadata-only advertised measurement rejected: %v", err)
	}
	bodyOnly := base
	bodyOnly.MetadataTokens = Measurement{Confidence: ConfidenceUnknown}
	bodyOnly.BodyTokens = Measurement{Value: 34, Confidence: ConfidenceExact, Basis: "advertised body"}
	if err := bodyOnly.Validate(); err != nil {
		t.Fatalf("body-only advertised measurement rejected: %v", err)
	}
}

func TestFindingAndDiscoveryValidation(t *testing.T) {
	t.Parallel()

	discovery := Discovery{
		Capabilities: []Capability{{
			Runtime:        RuntimeClaude,
			Type:           CapabilityMCPServer,
			Name:           "filesystem",
			Scope:          ScopeUser,
			Enabled:        EnabledStateUnknown,
			Advertisement:  AdvertisementStateUnknown,
			MetadataTokens: Measurement{Confidence: ConfidenceUnknown},
			BodyTokens:     Measurement{Confidence: ConfidenceUnknown},
		}},
		Findings: []Finding{{
			Runtime:        RuntimeClaude,
			Code:           "config-unreadable",
			Message:        "configuration could not be read",
			Severity:       SeverityWarning,
			CapabilityType: CapabilityMCPServer,
			CapabilityName: "filesystem",
			Confidence:     ConfidenceObserved,
		}},
	}
	if err := discovery.Validate(); err != nil {
		t.Fatalf("valid discovery rejected: %v", err)
	}

	discovery.Findings[0].Severity = Severity("critical")
	if err := discovery.Validate(); err == nil {
		t.Fatal("invalid finding severity accepted")
	}
}

func TestFingerprintForUsageEventIsStableAcrossTimeZones(t *testing.T) {
	t.Parallel()

	timestamp := time.Date(2026, 8, 13, 14, 30, 0, 123, time.FixedZone("CEST", 2*60*60))
	event := UsageEvent{
		Timestamp:      timestamp,
		Runtime:        RuntimeCodex,
		SessionID:      "session-hash",
		ProjectID:      "project-hash",
		CapabilityType: CapabilityTool,
		CapabilityName: "terminal",
		EventType:      EventInvoked,
	}
	first, err := FingerprintForUsageEvent(event)
	if err != nil {
		t.Fatalf("first fingerprint: %v", err)
	}
	event.Timestamp = timestamp.UTC()
	second, err := FingerprintForUsageEvent(event)
	if err != nil {
		t.Fatalf("second fingerprint: %v", err)
	}
	if first != second {
		t.Fatalf("fingerprints differ across equivalent timestamps: %q != %q", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("fingerprint length = %d, want SHA-256 hex length 64", len(first))
	}
}
