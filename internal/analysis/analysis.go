// Package analysis turns runtime-neutral inventory and usage observations into
// deterministic evidence. It deliberately knows nothing about runtime
// configuration, persistence, files, or presentation.
package analysis

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

const (
	// DefaultStaleAfter is the default period after which a previously active
	// capability is considered stale. Callers still pass the resulting config
	// to Analyze so the policy is explicit at the boundary.
	DefaultStaleAfter = 60 * 24 * time.Hour

	// DefaultReviewFootprintTokens is the known metadata/body footprint at which
	// a low-use capability merits review. The threshold is intentionally coarse:
	// it is a triage aid, not a cost score.
	DefaultReviewFootprintTokens int64 = 1000

	// DefaultReviewUseCount is the maximum observed invocation count that is
	// considered low use for the review rule.
	DefaultReviewUseCount = 1
)

// Classification is a plain policy label. It is not an opaque score.
type Classification string

const (
	KEEP   Classification = "KEEP"
	REVIEW Classification = "REVIEW"
	STALE  Classification = "STALE"
	DEAD   Classification = "DEAD"
)

// Config controls policy boundaries used by Analyze. A zero Config is
// normalized to DefaultConfig. Once a caller supplies any field, durations
// and thresholds must be valid; this catches accidental partial configuration
// while keeping the default easy to discover.
type Config struct {
	// StaleAfter is strict: an age exactly equal to StaleAfter is not stale;
	// only an age older than the threshold is stale.
	StaleAfter time.Duration

	// ReviewFootprintTokens is compared with the sum of known metadata and body
	// values. Unknown values are excluded; context labels their semantics by
	// capability type.
	ReviewFootprintTokens int64

	// ReviewMaxUseCount is the inclusive upper bound for low invocation use.
	// Loaded events remain separate evidence and do not become invocations.
	ReviewMaxUseCount int
}

// DefaultConfig returns the documented policy defaults.
func DefaultConfig() Config {
	return Config{
		StaleAfter:            DefaultStaleAfter,
		ReviewFootprintTokens: DefaultReviewFootprintTokens,
		ReviewMaxUseCount:     DefaultReviewUseCount,
	}
}

// Validate checks policy values. A zero Config is valid because Analyze fills
// it from DefaultConfig; negative values and unusable thresholds are errors.
func (c Config) Validate() error {
	if c.StaleAfter < 0 {
		return errors.New("stale threshold cannot be negative")
	}
	if c.ReviewFootprintTokens < 0 {
		return errors.New("review footprint threshold cannot be negative")
	}
	if c.ReviewMaxUseCount < 0 {
		return errors.New("review use threshold cannot be negative")
	}
	return nil
}

func (c Config) withDefaults() (Config, error) {
	if c == (Config{}) {
		c = DefaultConfig()
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	if c.StaleAfter == 0 {
		return Config{}, errors.New("stale threshold must be greater than zero")
	}
	if c.ReviewFootprintTokens == 0 {
		return Config{}, errors.New("review footprint threshold must be greater than zero")
	}
	return c, nil
}

// CapabilityEvidence is the evidence for one installed definition. Events
// have no source or scope fields, so a matching event is shared by all
// definitions with the same runtime, category, and name; this is explicit in
// the duplicate report rather than pretending the source can be inferred.
type CapabilityEvidence struct {
	// Capability is the installed definition represented by this row.
	Capability domain.Capability

	// EventCounts keeps advertised, loaded, and invoked observations separate.
	// All three keys are present, including zero-valued keys.
	EventCounts map[domain.EventType]int

	// InvocationCount counts only invoked events. ActivityCount counts loaded
	// plus invoked events and is used to distinguish a loaded-but-never-invoked
	// definition from a dead one.
	InvocationCount      int
	ActivityCount        int
	DistinctSessionCount int

	// Last-used means the latest loaded or invoked observation. Advertised
	// observations do not establish use. LastUsedAge is clamped to zero for a
	// future timestamp so callers never receive a misleading negative age.
	HasLastUsed      bool
	LastUsedAt       time.Time
	LastUsedAge      time.Duration
	LastUsedInFuture bool
	// MetadataTokens and BodyTokens retain adapter measurements independently;
	// context summaries label their semantics by capability type. Neither
	// measurement implies a loaded or invoked event.
	MetadataTokens domain.Measurement
	BodyTokens     domain.Measurement
	Classification Classification
	Confidence     domain.Confidence
	Basis          string
}

// EventCount returns one event type's count without exposing map lookup
// details to formatting or doctor callers.
func (e CapabilityEvidence) EventCount(eventType domain.EventType) int {
	return e.EventCounts[eventType]
}

// Report is the complete deterministic analysis result.
type Report struct {
	Capabilities []CapabilityEvidence
	Context      ContextSummary
	Duplicates   []DuplicateName
}

// MeasurementSummary is a grouped subtotal of one compatible measurement kind.
// Context groups label metadata and body semantics by capability type. Value
// is only the sum of known measurements; unknown values are excluded and
// counted explicitly.
type MeasurementSummary struct {
	Value          int64
	Confidence     domain.Confidence
	Basis          string
	KnownCount     int
	UnknownCount   int
	ExactCount     int
	ObservedCount  int
	EstimatedCount int
	Estimated      bool
	Complete       bool
}

// IsKnown reports whether at least one compatible value contributed to Value.
func (m MeasurementSummary) IsKnown() bool { return m.KnownCount > 0 }

// IsEstimate reports whether any contributing known value was estimated.
func (m MeasurementSummary) IsEstimate() bool { return m.Estimated }

// ContextGroup is one runtime/category bucket. Metadata and body remain
// separate; their semantic labels are selected from capability type and
// exposure evidence (for example, skills can have configured baseline
// metadata, while instruction-file bodies are configured baseline content).
type ContextGroup struct {
	Runtime         domain.Runtime
	CapabilityType  domain.CapabilityType
	CapabilityCount int
	MetadataTokens  MeasurementSummary
	BodyTokens      MeasurementSummary
}

// ContextSummary groups compatible metadata and body measurements by runtime
// and capability category in deterministic order; basis labels explain whether
// each dimension is configured baseline content, on-load content, or has
// runtime-dependent loading semantics.
type ContextSummary struct {
	Groups []ContextGroup
}

// DuplicateName describes installed definitions sharing a runtime/category/
// name while differing by source or scope. The full definitions are retained
// for doctor consumers to explain the conflict.
type DuplicateName struct {
	Runtime        domain.Runtime
	CapabilityType domain.CapabilityType
	Name           string
	Definitions    []domain.Capability
}

type capabilityKey struct {
	runtime domain.Runtime
	typ     domain.CapabilityType
	name    string
}

// Analyze validates inputs and returns deterministic per-definition evidence,
// context totals, and duplicate-name findings. now is explicit so repeated
// runs at the same instant produce identical ages and classifications.
func Analyze(capabilities []domain.Capability, events []domain.UsageEvent, config Config, now time.Time) (Report, error) {
	config, err := config.withDefaults()
	if err != nil {
		return Report{}, fmt.Errorf("invalid analysis config: %w", err)
	}
	if now.IsZero() {
		return Report{}, errors.New("analysis time is required")
	}
	now = now.UTC()

	orderedCapabilities := append([]domain.Capability(nil), capabilities...)
	for index, capability := range orderedCapabilities {
		if err := capability.Validate(); err != nil {
			return Report{}, fmt.Errorf("invalid capability at index %d (%q): %w", index, capability.Name, err)
		}
	}
	orderedEvents := append([]domain.UsageEvent(nil), events...)
	for index, event := range orderedEvents {
		if err := event.Validate(); err != nil {
			return Report{}, fmt.Errorf("invalid usage event at index %d: %w", index, err)
		}
	}
	sort.SliceStable(orderedCapabilities, func(i, j int) bool {
		return capabilityLess(orderedCapabilities[i], orderedCapabilities[j])
	})
	sort.SliceStable(orderedEvents, func(i, j int) bool {
		return eventLess(orderedEvents[i], orderedEvents[j])
	})

	duplicates, err := DetectDuplicateNames(orderedCapabilities)
	if err != nil {
		return Report{}, err
	}
	context, err := SummarizeContext(orderedCapabilities)
	if err != nil {
		return Report{}, err
	}

	byKey := make(map[capabilityKey][]domain.UsageEvent)
	for _, event := range orderedEvents {
		key := capabilityKey{runtime: event.Runtime, typ: event.CapabilityType, name: event.CapabilityName}
		byKey[key] = append(byKey[key], event)
	}

	result := Report{
		Capabilities: make([]CapabilityEvidence, 0, len(orderedCapabilities)),
		Context:      context,
		Duplicates:   duplicates,
	}
	for _, capability := range orderedCapabilities {
		key := capabilityKey{runtime: capability.Runtime, typ: capability.Type, name: capability.Name}
		evidence, err := analyzeCapability(capability, byKey[key], config, now)
		if err != nil {
			return Report{}, fmt.Errorf("analyze capability %q: %w", capability.Name, err)
		}
		result.Capabilities = append(result.Capabilities, evidence)
	}
	return result, nil
}

func analyzeCapability(capability domain.Capability, events []domain.UsageEvent, config Config, now time.Time) (CapabilityEvidence, error) {
	eventCounts := map[domain.EventType]int{
		domain.EventAdvertised: 0,
		domain.EventLoaded:     0,
		domain.EventInvoked:    0,
	}
	sessions := make(map[string]struct{})
	var lastUsed time.Time
	for _, event := range events {
		eventCounts[event.EventType]++
		if event.EventType != domain.EventLoaded && event.EventType != domain.EventInvoked {
			continue
		}
		sessions[event.SessionID] = struct{}{}
		if lastUsed.IsZero() || event.Timestamp.After(lastUsed) {
			lastUsed = event.Timestamp.UTC()
		}
	}

	invocationCount := eventCounts[domain.EventInvoked]
	activityCount := eventCounts[domain.EventLoaded] + invocationCount
	evidence := CapabilityEvidence{
		Capability:           capability,
		EventCounts:          eventCounts,
		InvocationCount:      invocationCount,
		ActivityCount:        activityCount,
		DistinctSessionCount: len(sessions),
		MetadataTokens:       capability.MetadataTokens,
		BodyTokens:           capability.BodyTokens,
		Confidence:           domain.ConfidenceObserved,
	}
	if !lastUsed.IsZero() {
		evidence.HasLastUsed = true
		evidence.LastUsedAt = lastUsed
		if lastUsed.After(now) {
			evidence.LastUsedInFuture = true
			evidence.LastUsedAge = 0
		} else {
			evidence.LastUsedAge = now.Sub(lastUsed)
		}
	}

	classification, confidence, basis, err := classify(evidence, config)
	if err != nil {
		return CapabilityEvidence{}, err
	}
	evidence.Classification = classification
	evidence.Confidence = confidence
	evidence.Basis = basis
	return evidence, nil
}

func classify(evidence CapabilityEvidence, config Config) (Classification, domain.Confidence, string, error) {
	if evidence.ActivityCount == 0 {
		return DEAD, domain.ConfidenceObserved, "installed definition with no observed loaded or invoked events", nil
	}
	if !evidence.LastUsedInFuture && evidence.LastUsedAge > config.StaleAfter {
		return STALE, domain.ConfidenceObserved, fmt.Sprintf("last loaded/invoked observation is %s older than the %s stale threshold", evidence.LastUsedAge, config.StaleAfter), nil
	}
	footprint, err := knownFootprint(evidence.MetadataTokens, evidence.BodyTokens)
	if err != nil {
		return Classification(""), domain.ConfidenceUnknown, "", err
	}
	if footprint.KnownCount > 0 && footprint.Value >= config.ReviewFootprintTokens && evidence.InvocationCount <= config.ReviewMaxUseCount {
		return REVIEW, footprint.Confidence, fmt.Sprintf("%s with %d observed invocation(s)", footprint.Basis(), evidence.InvocationCount), nil
	}
	if evidence.LastUsedInFuture {
		return KEEP, domain.ConfidenceObserved, "observed loaded/invoked activity has a future timestamp; age clamped to zero", nil
	}
	return KEEP, domain.ConfidenceObserved, "observed loaded/invoked activity is within the stale threshold", nil
}

type footprintEvidence struct {
	Value        int64
	Confidence   domain.Confidence
	KnownCount   int
	UnknownCount int
	Bases        []string
}

func knownFootprint(metadata, body domain.Measurement) (footprintEvidence, error) {
	result := footprintEvidence{Confidence: domain.ConfidenceUnknown}
	measurements := []struct {
		label       string
		measurement domain.Measurement
	}{
		{label: "metadata footprint", measurement: metadata},
		{label: "body/content footprint", measurement: body},
	}
	for _, item := range measurements {
		measurement := item.measurement
		switch measurement.Confidence {
		case domain.ConfidenceUnknown:
			result.UnknownCount++
			continue
		case domain.ConfidenceExact, domain.ConfidenceObserved, domain.ConfidenceEstimated:
			// Known measurement; confidence is reduced to the weakest known
			// contributor below.
		default:
			return footprintEvidence{}, fmt.Errorf("invalid measurement confidence %q", measurement.Confidence)
		}
		if measurement.Value > maxInt64-result.Value {
			return footprintEvidence{}, errors.New("known footprint overflows int64")
		}
		result.Value += measurement.Value
		result.KnownCount++
		if result.KnownCount == 1 || weakerConfidence(measurement.Confidence, result.Confidence) == measurement.Confidence {
			result.Confidence = measurement.Confidence
		}
		if strings.TrimSpace(measurement.Basis) != "" {
			result.Bases = append(result.Bases, item.label+": "+measurement.Basis)
		}
	}
	sort.Strings(result.Bases)
	return result, nil
}

func weakerConfidence(left, right domain.Confidence) domain.Confidence {
	rank := func(confidence domain.Confidence) int {
		switch confidence {
		case domain.ConfidenceExact:
			return 0
		case domain.ConfidenceObserved:
			return 1
		case domain.ConfidenceEstimated:
			return 2
		default:
			return 3
		}
	}
	if rank(left) >= rank(right) {
		return left
	}
	return right
}

func (f footprintEvidence) Basis() string {
	parts := []string{fmt.Sprintf("metadata plus body/content footprint is %d known token(s)", f.Value)}
	if f.KnownCount > 0 {
		parts = append(parts, fmt.Sprintf("weakest known confidence: %s", f.Confidence))
	}
	if f.UnknownCount > 0 {
		parts = append(parts, fmt.Sprintf("%d unknown measurement(s) excluded", f.UnknownCount))
	}
	if len(f.Bases) > 0 {
		parts = append(parts, "bases: "+strings.Join(f.Bases, ", "))
	}
	return strings.Join(parts, "; ")
}

// SummarizeContext aggregates installed configured baseline and on-load
// measurements. It is intentionally independent of event observations:
// neither exposure measurement is evidence of loaded or invoked use.
func SummarizeContext(capabilities []domain.Capability) (ContextSummary, error) {
	ordered := append([]domain.Capability(nil), capabilities...)
	for index, capability := range ordered {
		if err := capability.Validate(); err != nil {
			return ContextSummary{}, fmt.Errorf("invalid capability at index %d (%q): %w", index, capability.Name, err)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return capabilityLess(ordered[i], ordered[j])
	})

	type accumulator struct {
		key      capabilityKey
		count    int
		metadata []domain.Measurement
		body     []domain.Measurement
	}
	groups := make(map[capabilityKey]*accumulator)
	for _, capability := range ordered {
		key := capabilityKey{runtime: capability.Runtime, typ: capability.Type}
		group := groups[key]
		if group == nil {
			group = &accumulator{key: key}
			groups[key] = group
		}
		group.count++
		group.metadata = append(group.metadata, capability.MetadataTokens)
		group.body = append(group.body, capability.BodyTokens)
	}

	keys := make([]capabilityKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].runtime != keys[j].runtime {
			return keys[i].runtime < keys[j].runtime
		}
		return keys[i].typ < keys[j].typ
	})

	result := ContextSummary{Groups: make([]ContextGroup, 0, len(keys))}
	for _, key := range keys {
		group := groups[key]
		metadataLabel := "metadata footprint"
		if key.typ == domain.CapabilitySkill {
			metadataLabel = "configured baseline exposure for Skill metadata"
		}
		metadata, err := aggregateMeasurements(group.metadata, metadataLabel)
		if err != nil {
			return ContextSummary{}, fmt.Errorf("aggregate %s/%s metadata measurements: %w", key.runtime, key.typ, err)
		}
		body, err := aggregateMeasurements(group.body, bodyContextLabel(key.typ))
		if err != nil {
			return ContextSummary{}, fmt.Errorf("aggregate %s/%s body measurements: %w", key.runtime, key.typ, err)
		}
		result.Groups = append(result.Groups, ContextGroup{
			Runtime:         key.runtime,
			CapabilityType:  key.typ,
			CapabilityCount: group.count,
			MetadataTokens:  metadata,
			BodyTokens:      body,
		})
	}
	return result, nil
}

func bodyContextLabel(capabilityType domain.CapabilityType) string {
	switch capabilityType {
	case domain.CapabilityInstructionFile:
		return "configured baseline instruction content"
	case domain.CapabilitySkill:
		return "on-load body footprint"
	default:
		return "body/content footprint (loading semantics runtime-dependent)"
	}
}

func aggregateMeasurements(measurements []domain.Measurement, label string) (MeasurementSummary, error) {
	result := MeasurementSummary{Complete: true}
	bases := make(map[string]struct{})
	for _, measurement := range measurements {
		switch measurement.Confidence {
		case domain.ConfidenceUnknown:
			result.UnknownCount++
			result.Complete = false
			continue
		case domain.ConfidenceExact:
			result.ExactCount++
		case domain.ConfidenceObserved:
			result.ObservedCount++
		case domain.ConfidenceEstimated:
			result.EstimatedCount++
			result.Estimated = true
		default:
			return MeasurementSummary{}, fmt.Errorf("invalid measurement confidence %q", measurement.Confidence)
		}
		if measurement.Value > maxInt64-result.Value {
			return MeasurementSummary{}, errors.New("measurement total overflows int64")
		}
		result.Value += measurement.Value
		result.KnownCount++
		if strings.TrimSpace(measurement.Basis) != "" {
			bases[measurement.Basis] = struct{}{}
		}
	}

	switch {
	case result.KnownCount == 0:
		result.Confidence = domain.ConfidenceUnknown
	case result.UnknownCount > 0:
		// The known subtotal remains useful, but the complete total cannot
		// honestly be called exact or estimated while values are missing.
		result.Confidence = domain.ConfidenceUnknown
	case result.EstimatedCount > 0:
		result.Confidence = domain.ConfidenceEstimated
	case result.ObservedCount > 0:
		result.Confidence = domain.ConfidenceObserved
	default:
		result.Confidence = domain.ConfidenceExact
	}

	if result.KnownCount == 0 {
		result.Basis = fmt.Sprintf("%s: no known measurements; %d unknown measurement(s) excluded", label, result.UnknownCount)
		return result, nil
	}
	basisParts := []string{fmt.Sprintf("%s: sum of %d compatible known measurement(s)", label, result.KnownCount)}
	if result.EstimatedCount > 0 {
		basisParts = append(basisParts, "includes estimated measurements")
	}
	if result.UnknownCount > 0 {
		basisParts = append(basisParts, fmt.Sprintf("%d unknown measurement(s) excluded", result.UnknownCount))
	}
	if len(bases) > 0 {
		orderedBases := make([]string, 0, len(bases))
		for basis := range bases {
			orderedBases = append(orderedBases, basis)
		}
		sort.Strings(orderedBases)
		basisParts = append(basisParts, "bases: "+strings.Join(orderedBases, ", "))
	}
	result.Basis = strings.Join(basisParts, "; ")
	return result, nil
}

// DetectDuplicateNames finds definitions that have the same runtime,
// capability category, and name but differ in source or scope.
func DetectDuplicateNames(capabilities []domain.Capability) ([]DuplicateName, error) {
	ordered := append([]domain.Capability(nil), capabilities...)
	for index, capability := range ordered {
		if err := capability.Validate(); err != nil {
			return nil, fmt.Errorf("invalid capability at index %d (%q): %w", index, capability.Name, err)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return capabilityLess(ordered[i], ordered[j])
	})

	type definitions struct {
		key  capabilityKey
		defs map[string]domain.Capability
	}
	groups := make(map[capabilityKey]*definitions)
	for _, capability := range ordered {
		key := capabilityKey{runtime: capability.Runtime, typ: capability.Type, name: capability.Name}
		group := groups[key]
		if group == nil {
			group = &definitions{key: key, defs: make(map[string]domain.Capability)}
			groups[key] = group
		}
		group.defs[definitionKey(capability)] = capability
	}

	keys := make([]capabilityKey, 0, len(groups))
	for key, group := range groups {
		if len(group.defs) > 1 {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].runtime != keys[j].runtime {
			return keys[i].runtime < keys[j].runtime
		}
		if keys[i].typ != keys[j].typ {
			return keys[i].typ < keys[j].typ
		}
		return keys[i].name < keys[j].name
	})

	result := make([]DuplicateName, 0, len(keys))
	for _, key := range keys {
		group := groups[key]
		definitions := make([]domain.Capability, 0, len(group.defs))
		for _, capability := range group.defs {
			definitions = append(definitions, capability)
		}
		sort.SliceStable(definitions, func(i, j int) bool {
			return capabilityLess(definitions[i], definitions[j])
		})
		result = append(result, DuplicateName{
			Runtime:        key.runtime,
			CapabilityType: key.typ,
			Name:           key.name,
			Definitions:    definitions,
		})
	}
	return result, nil
}

func definitionKey(capability domain.Capability) string {
	return string(capability.Scope) + "\x00" + capability.Source
}

func capabilityLess(left, right domain.Capability) bool {
	if left.Runtime != right.Runtime {
		return left.Runtime < right.Runtime
	}
	if left.Type != right.Type {
		return left.Type < right.Type
	}
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	if left.Scope != right.Scope {
		return left.Scope < right.Scope
	}
	if left.Source != right.Source {
		return left.Source < right.Source
	}
	if left.Enabled != right.Enabled {
		return left.Enabled < right.Enabled
	}
	if left.Advertisement != right.Advertisement {
		return left.Advertisement < right.Advertisement
	}
	if left.Hash != right.Hash {
		return left.Hash < right.Hash
	}
	if left.MetadataTokens.Value != right.MetadataTokens.Value {
		return left.MetadataTokens.Value < right.MetadataTokens.Value
	}
	if left.MetadataTokens.Confidence != right.MetadataTokens.Confidence {
		return left.MetadataTokens.Confidence < right.MetadataTokens.Confidence
	}
	if left.MetadataTokens.Basis != right.MetadataTokens.Basis {
		return left.MetadataTokens.Basis < right.MetadataTokens.Basis
	}
	if left.BodyTokens.Value != right.BodyTokens.Value {
		return left.BodyTokens.Value < right.BodyTokens.Value
	}
	if left.BodyTokens.Confidence != right.BodyTokens.Confidence {
		return left.BodyTokens.Confidence < right.BodyTokens.Confidence
	}
	if left.BodyTokens.Basis != right.BodyTokens.Basis {
		return left.BodyTokens.Basis < right.BodyTokens.Basis
	}
	if !left.FirstSeen.Equal(right.FirstSeen) {
		return left.FirstSeen.Before(right.FirstSeen)
	}
	return left.LastSeen.Before(right.LastSeen)
}

func eventLess(left, right domain.UsageEvent) bool {
	leftTimestamp, rightTimestamp := left.Timestamp.UTC(), right.Timestamp.UTC()
	if !leftTimestamp.Equal(rightTimestamp) {
		return leftTimestamp.Before(rightTimestamp)
	}
	if left.Runtime != right.Runtime {
		return left.Runtime < right.Runtime
	}
	if left.CapabilityType != right.CapabilityType {
		return left.CapabilityType < right.CapabilityType
	}
	if left.CapabilityName != right.CapabilityName {
		return left.CapabilityName < right.CapabilityName
	}
	if left.EventType != right.EventType {
		return left.EventType < right.EventType
	}
	if left.SessionID != right.SessionID {
		return left.SessionID < right.SessionID
	}
	if left.ProjectID != right.ProjectID {
		return left.ProjectID < right.ProjectID
	}
	return left.Fingerprint < right.Fingerprint
}

const maxInt64 = int64(^uint64(0) >> 1)
