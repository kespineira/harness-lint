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
		{name: "capability type known", valid: func() bool { return CapabilityTool.Valid() }, want: true},
		{name: "capability type unknown", valid: func() bool { return CapabilityUnknown.Valid() }, want: false},
		{name: "scope known", valid: func() bool { return ScopeProject.Valid() }, want: true},
		{name: "scope unknown", valid: func() bool { return ScopeUnknown.Valid() }, want: false},
		{name: "event type known", valid: func() bool { return EventInvoked.Valid() }, want: true},
		{name: "event type unknown", valid: func() bool { return EventType("installed").Valid() }, want: false},
		{name: "confidence known", valid: func() bool { return ConfidenceEstimated.Valid() }, want: true},
		{name: "confidence unknown", valid: func() bool { return Confidence("guess").Valid() }, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.valid(); got != test.want {
				t.Fatalf("Valid() = %v, want %v", got, test.want)
			}
		})
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
		Runtime:      RuntimeCodex,
		Type:         CapabilitySkill,
		Name:         "lint",
		Scope:        ScopeProject,
		Context:      Measurement{Value: 100, Confidence: ConfidenceObserved, Basis: "session metadata"},
		InputTokens:  Measurement{Value: 20, Confidence: ConfidenceExact, Basis: "request metadata"},
		OutputTokens: Measurement{Value: 30, Confidence: ConfidenceEstimated, Basis: "tokenizer estimate"},
		FirstSeen:    firstSeen,
		LastSeen:     firstSeen.Add(time.Minute),
	}
	if err := capability.Validate(); err != nil {
		t.Fatalf("valid capability rejected: %v", err)
	}

	capability.LastSeen = capability.FirstSeen.Add(-time.Second)
	if err := capability.Validate(); err == nil || !strings.Contains(err.Error(), "precedes") {
		t.Fatalf("invalid seen range error = %v", err)
	}
}

func TestFindingAndDiscoveryValidation(t *testing.T) {
	t.Parallel()

	discovery := Discovery{
		Capabilities: []Capability{{
			Runtime:      RuntimeClaude,
			Type:         CapabilityMCP,
			Name:         "filesystem",
			Scope:        ScopeUser,
			Context:      Measurement{Confidence: ConfidenceUnknown},
			InputTokens:  Measurement{Confidence: ConfidenceUnknown},
			OutputTokens: Measurement{Confidence: ConfidenceUnknown},
		}},
		Findings: []Finding{{
			Runtime:        RuntimeClaude,
			Code:           "config-unreadable",
			Message:        "configuration could not be read",
			Severity:       SeverityWarning,
			CapabilityType: CapabilityMCP,
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
