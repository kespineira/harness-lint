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
		ObservedAt:       timestamp,
		Runtime:          RuntimeCodex,
		SessionID:        "session-hash",
		ProjectID:        "project-hash",
		CapabilityType:   CapabilityTool,
		CapabilityName:   "terminal",
		EventType:        EventInvoked,
		Provenance:       ProvenanceImport,
		InvocationOrigin: InvocationOriginUnknown,
		SchemaVersion:    CurrentUsageEventSchemaVersion,
	}
	first, err := FingerprintForUsageEvent(event)
	if err != nil {
		t.Fatalf("first fingerprint: %v", err)
	}
	event.ObservedAt = timestamp.UTC()
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

func TestUsageEventObservedAndSourceTimesRemainDistinct(t *testing.T) {
	t.Parallel()

	observed := time.Date(2026, 8, 13, 15, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	source := observed.Add(-time.Hour)
	event := validUsageEvent(observed)
	event.SourceTimestamp = &source
	if err := event.Validate(); err != nil {
		t.Fatalf("valid source timestamp rejected: %v", err)
	}
	normalized, err := NormalizeUsageEvent(event)
	if err != nil {
		t.Fatalf("NormalizeUsageEvent() error = %v", err)
	}
	if !normalized.ObservedAt.Equal(observed.UTC()) {
		t.Fatalf("observed time changed during normalization: %s", normalized.ObservedAt)
	}
	if got := normalized.EffectiveActivityTime(); !got.Equal(source.UTC()) {
		t.Fatalf("effective activity time = %s, want source time %s", got, source.UTC())
	}

	event.SourceTimestamp = nil
	if got := event.EffectiveActivityTime(); !got.Equal(observed.UTC()) {
		t.Fatalf("fallback effective activity time = %s, want observed time %s", got, observed.UTC())
	}
}

func TestUsageEventValidationRequiresProvenanceAndPositiveSchema(t *testing.T) {
	t.Parallel()

	event := validUsageEvent(time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC))
	tests := []struct {
		name   string
		mutate func(*UsageEvent)
		want   string
	}{
		{name: "observed at", mutate: func(e *UsageEvent) { e.ObservedAt = time.Time{} }, want: "observed-at"},
		{name: "provenance", mutate: func(e *UsageEvent) { e.Provenance = Provenance("unknown") }, want: "provenance"},
		{name: "invocation origin", mutate: func(e *UsageEvent) { e.InvocationOrigin = InvocationOrigin("automated") }, want: "invocation origin"},
		{name: "schema version", mutate: func(e *UsageEvent) { e.SchemaVersion = 0 }, want: "schema version"},
		{name: "source timestamp", mutate: func(e *UsageEvent) { zero := time.Time{}; e.SourceTimestamp = &zero }, want: "source timestamp"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := event
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestInvocationOriginValidation(t *testing.T) {
	t.Parallel()

	for _, origin := range []InvocationOrigin{InvocationOriginUnknown, InvocationOriginModelSelected, InvocationOriginUserExplicit} {
		if !origin.Valid() {
			t.Fatalf("InvocationOrigin(%q).Valid() = false, want true", origin)
		}
	}
	if InvocationOrigin("other").Valid() {
		t.Fatal("unknown invocation origin accepted")
	}
}

func TestStableSourceIdentityDrivesFingerprintAndFallbackIsConservative(t *testing.T) {
	t.Parallel()

	first := validUsageEvent(time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC))
	rawIdentity := " tool-use-1 "
	first.SourceIdentity = rawIdentity
	normalizedFirst := first
	normalizedFirst.SourceIdentity = NormalizeSourceIdentity(rawIdentity)
	if got := NormalizeSourceIdentity(normalizedFirst.SourceIdentity); got != normalizedFirst.SourceIdentity {
		t.Fatalf("source identity normalization is not idempotent: %q -> %q", normalizedFirst.SourceIdentity, got)
	}
	normalizedFingerprint, err := FingerprintForUsageEvent(normalizedFirst)
	if err != nil {
		t.Fatalf("normalized source fingerprint: %v", err)
	}
	firstFingerprint, err := FingerprintForUsageEvent(first)
	if err != nil {
		t.Fatalf("first fingerprint: %v", err)
	}
	if firstFingerprint != normalizedFingerprint {
		t.Fatalf("source normalization changed fingerprint: raw=%q normalized=%q", firstFingerprint, normalizedFingerprint)
	}
	second := first
	second.ObservedAt = first.ObservedAt.Add(time.Minute)
	secondFingerprint, err := FingerprintForUsageEvent(second)
	if err != nil {
		t.Fatalf("retry fingerprint: %v", err)
	}
	if firstFingerprint != secondFingerprint {
		t.Fatalf("same stable source identity fingerprints differ: %q != %q", firstFingerprint, secondFingerprint)
	}

	second.SourceIdentity = "tool-use-2"
	secondFingerprint, err = FingerprintForUsageEvent(second)
	if err != nil {
		t.Fatalf("second delivery fingerprint: %v", err)
	}
	if firstFingerprint == secondFingerprint {
		t.Fatal("different stable source identities collapsed into one fingerprint")
	}

	rescan := first
	rescan.ObservedAt = first.ObservedAt.Add(10 * time.Minute)
	rescanFingerprint, err := FingerprintForUsageEvent(rescan)
	if err != nil {
		t.Fatalf("rescan fingerprint: %v", err)
	}
	if firstFingerprint != rescanFingerprint {
		t.Fatalf("stable source identity fingerprints differ across rescans: %q != %q", firstFingerprint, rescanFingerprint)
	}

	withoutIdentity := validUsageEvent(first.ObservedAt)
	withoutIdentity.SourceTimestamp = func() *time.Time {
		value := first.ObservedAt.Add(-time.Second)
		return &value
	}()
	withDifferentSourceTime := withoutIdentity
	otherSourceTime := withoutIdentity.SourceTimestamp.Add(time.Second)
	withDifferentSourceTime.SourceTimestamp = &otherSourceTime
	firstFallback, err := FingerprintForUsageEvent(withoutIdentity)
	if err != nil {
		t.Fatalf("fallback fingerprint: %v", err)
	}
	secondFallback, err := FingerprintForUsageEvent(withDifferentSourceTime)
	if err != nil {
		t.Fatalf("different fallback fingerprint: %v", err)
	}
	if firstFallback == secondFallback {
		t.Fatal("fallback fingerprints collapsed events with different source times")
	}
	rescanFallback := withoutIdentity
	rescanFallback.ObservedAt = withoutIdentity.ObservedAt.Add(time.Hour)
	rescanFallbackFingerprint, err := FingerprintForUsageEvent(rescanFallback)
	if err != nil {
		t.Fatalf("rescan fallback fingerprint: %v", err)
	}
	if firstFallback != rescanFallbackFingerprint {
		t.Fatal("fallback fingerprint used observation time despite trustworthy source timestamp")
	}
}

func TestFingerprintNeverContainsPayloadLikeMetadata(t *testing.T) {
	t.Parallel()

	event := validUsageEvent(time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC))
	event.SessionID = "prompt secret"
	event.ProjectID = "/private/raw/path/tool output"
	event.SourceIdentity = "tool-use-1"
	fingerprint, err := FingerprintForUsageEvent(event)
	if err != nil {
		t.Fatalf("FingerprintForUsageEvent() error = %v", err)
	}
	for _, forbidden := range []string{"prompt secret", "/private/raw/path", "tool output"} {
		if strings.Contains(fingerprint, forbidden) {
			t.Fatalf("fingerprint contains unsafe metadata %q: %s", forbidden, fingerprint)
		}
	}
}

func validUsageEvent(observedAt time.Time) UsageEvent {
	return UsageEvent{
		ObservedAt:       observedAt,
		Runtime:          RuntimeCodex,
		SessionID:        "session-hash",
		ProjectID:        "project-hash",
		CapabilityType:   CapabilityTool,
		CapabilityName:   "terminal",
		EventType:        EventInvoked,
		Provenance:       ProvenanceImport,
		InvocationOrigin: InvocationOriginUnknown,
		SchemaVersion:    CurrentUsageEventSchemaVersion,
	}
}
