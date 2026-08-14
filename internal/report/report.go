// Package report owns the stable, privacy-preserving DTOs used by the report
// and stale commands.  These types deliberately do not mirror domain or
// analysis structs so persistence and implementation details cannot become a
// command-line JSON contract by accident.
package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/kespineira/harness-lint/internal/analysis"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
)

const SchemaVersion = 2

// ReportDocument is the versioned JSON contract for report --json.
type ReportDocument struct {
	SchemaVersion  int          `json:"schema_version"`
	GeneratedAt    string       `json:"generated_at"`
	StaleAfterDays int          `json:"stale_after_days"`
	Runtimes       []Runtime    `json:"runtimes"`
	Capabilities   []Capability `json:"capabilities"`
	UsageOnly      []UsageOnly  `json:"usage_only"`
	Findings       []Finding    `json:"findings"`
}

// StaleDocument is the versioned JSON contract for stale --json.  It omits
// usage-only rows because stale evaluates installed definitions only.
type StaleDocument struct {
	SchemaVersion  int          `json:"schema_version"`
	GeneratedAt    string       `json:"generated_at"`
	StaleAfterDays int          `json:"stale_after_days"`
	Runtimes       []Runtime    `json:"runtimes"`
	Capabilities   []Capability `json:"capabilities"`
	Findings       []Finding    `json:"findings"`
}

// Runtime contains aggregate evidence while retaining separate installed,
// advertised, loaded, and invoked concepts.
type Runtime struct {
	Runtime              string `json:"runtime"`
	Installed            int    `json:"installed"`
	Advertised           int    `json:"advertised"`
	Loaded               int    `json:"loaded"`
	Invoked              int    `json:"invoked"`
	ConfiguredAdvertised int    `json:"configured_advertised"`
	InvokedLast30Days    int    `json:"invoked_last_30d"`
	// NoActivityObserved counts installed definitions with no loaded or
	// invoked evidence; it does not prove lifetime non-use.
	NoActivityObserved int `json:"no_activity_observed"`
	UsageEvents        int `json:"usage_events"`
}

// Measurement is the privacy-safe DTO for one installed definition
// measurement. Tokens is nil when the source measurement is unknown; the
// confidence and basis remain explicit without serializing domain.Measurement
// or implying an exact runtime context cost.
type Measurement struct {
	Tokens     *int64 `json:"tokens"`
	Confidence string `json:"confidence"`
	Basis      string `json:"basis"`
}

// Capability contains one installed definition's analysis and safe activity
// history.  It intentionally excludes source paths, hashes, session IDs,
// source identities, and fingerprints.
type Capability struct {
	Runtime         string   `json:"runtime"`
	Type            string   `json:"type"`
	Name            string   `json:"name"`
	Installed       bool     `json:"installed"`
	Scope           string   `json:"scope"`
	InstalledScopes []string `json:"installed_scopes,omitempty"`
	Enabled         string   `json:"enabled"`
	Advertisement   string   `json:"advertisement"`
	// MetadataExposure describes name/description/metadata exposure only.
	MetadataExposure Measurement `json:"metadata_exposure"`
	// LoadedBodyFootprint describes loaded or on-load body content only.
	LoadedBodyFootprint         Measurement        `json:"loaded_body_footprint"`
	Status                      string             `json:"status"`
	Confidence                  string             `json:"confidence"`
	CoverageConfidence          string             `json:"coverage_confidence"`
	Basis                       string             `json:"basis"`
	Evidence                    string             `json:"evidence"`
	EvidenceSources             []string           `json:"evidence_sources"`
	Advertised                  int                `json:"advertised"`
	ObservedAdvertisedSessions  *int               `json:"observed_advertised_sessions"`
	InvokedInAdvertisedSessions *int               `json:"invoked_in_advertised_sessions"`
	Loaded                      int                `json:"loaded"`
	InvocationCount             int                `json:"invocation_count"`
	DistinctSessionCount        int                `json:"distinct_sessions"`
	FirstObservedAt             *string            `json:"first_observed_at"`
	LastObservedAt              *string            `json:"last_observed_at"`
	FirstEffectiveActivityAt    *string            `json:"first_effective_activity_at"`
	LastEffectiveActivityAt     *string            `json:"last_effective_activity_at"`
	FirstInvocationObservedAt   *string            `json:"first_invocation_observed_at"`
	LastInvocationObservedAt    *string            `json:"last_invocation_observed_at"`
	FirstInvocationEffectiveAt  *string            `json:"first_invocation_effective_at"`
	LastInvocationEffectiveAt   *string            `json:"last_invocation_effective_at"`
	LastInvocationAge           *string            `json:"last_invocation_age"`
	LastInvocationInFuture      bool               `json:"last_invocation_in_future"`
	Coverage                    *Coverage          `json:"coverage,omitempty"`
	FirstSeen                   *string            `json:"first_seen"`
	LastSeen                    *string            `json:"last_seen"`
	EffectiveCoverage           *EffectiveCoverage `json:"effective_coverage"`
}

// EffectiveCoverage summarizes only confirmed capture/presence intersection.
// Unknown coverage has a null duration; a string zero is never emitted.
type EffectiveCoverage struct {
	Status          string  `json:"status"`
	CoveredDuration *string `json:"covered_duration"`
}

// Coverage contains only nullable observation windows. It intentionally has
// no paths, session identifiers, source identities, or fingerprints, and it
// must never be interpreted as continuity or lifetime completeness.
type Coverage struct {
	FirstInventoryObservedAt  *string `json:"first_inventory_observed_at,omitempty"`
	LastInventoryObservedAt   *string `json:"last_inventory_observed_at,omitempty"`
	FirstUsageObservedAt      *string `json:"first_usage_observed_at,omitempty"`
	LastUsageObservedAt       *string `json:"last_usage_observed_at,omitempty"`
	FirstDirectHookObservedAt *string `json:"first_direct_hook_observed_at,omitempty"`
	LastDirectHookObservedAt  *string `json:"last_direct_hook_observed_at,omitempty"`
}

// UsageOnly represents observed usage with no matching current inventory.
// It keeps the same event distinctions as Capability without exposing event
// identifiers.
type UsageOnly struct {
	Runtime                     string             `json:"runtime"`
	Type                        string             `json:"type"`
	Name                        string             `json:"name"`
	Advertised                  int                `json:"advertised"`
	ObservedAdvertisedSessions  *int               `json:"observed_advertised_sessions"`
	InvokedInAdvertisedSessions *int               `json:"invoked_in_advertised_sessions"`
	Loaded                      int                `json:"loaded"`
	InvocationCount             int                `json:"invocation_count"`
	DistinctSessionCount        int                `json:"distinct_sessions"`
	EvidenceSources             []string           `json:"evidence_sources"`
	FirstObservedAt             *string            `json:"first_observed_at"`
	LastObservedAt              *string            `json:"last_observed_at"`
	FirstEffectiveActivityAt    *string            `json:"first_effective_activity_at"`
	LastEffectiveActivityAt     *string            `json:"last_effective_activity_at"`
	FirstInvocationObservedAt   *string            `json:"first_invocation_observed_at"`
	LastInvocationObservedAt    *string            `json:"last_invocation_observed_at"`
	FirstInvocationEffectiveAt  *string            `json:"first_invocation_effective_at"`
	LastInvocationEffectiveAt   *string            `json:"last_invocation_effective_at"`
	Coverage                    *Coverage          `json:"coverage,omitempty"`
	EffectiveCoverage           *EffectiveCoverage `json:"effective_coverage"`
}

// Finding is a safe, deterministic diagnostic.  Definitions is a count, not
// a list of source-bearing domain objects.
type Finding struct {
	Runtime     string `json:"runtime"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Code        string `json:"code"`
	Severity    string `json:"severity"`
	Confidence  string `json:"confidence"`
	Definitions int    `json:"definitions"`
	Message     string `json:"message"`
}

type capabilityKey struct {
	runtime domain.Runtime
	typ     domain.CapabilityType
	name    string
}

type observationSummary struct {
	counts   map[domain.EventType]int
	source   map[domain.Provenance]struct{}
	sessions map[string]struct{}
	// Aggregate history has no session identifiers. distinctSessions carries
	// the already-safe subtotal without manufacturing identities.
	distinctSessions            int
	observedAdvertisedSessions  *int64
	invokedInAdvertisedSessions *int64
	coverage                    *history.Coverage
	effectiveCoverage           *history.EffectiveCoverage
	installed                   bool
	installedScopes             []domain.Scope
	aggregate                   bool

	hasObserved         bool
	firstObserved       time.Time
	lastObserved        time.Time
	hasActivity         bool
	firstActivity       time.Time
	lastActivity        time.Time
	hasInvocation       bool
	firstInvocationSeen time.Time
	lastInvocationSeen  time.Time
	firstInvocation     time.Time
	lastInvocation      time.Time
}

func newObservationSummary() *observationSummary {
	return &observationSummary{
		counts:   map[domain.EventType]int{domain.EventAdvertised: 0, domain.EventLoaded: 0, domain.EventInvoked: 0},
		source:   make(map[domain.Provenance]struct{}),
		sessions: make(map[string]struct{}),
	}
}

func (s *observationSummary) add(event domain.UsageEvent) {
	s.counts[event.EventType]++
	s.source[event.Provenance] = struct{}{}
	observed := event.ObservedAt.UTC()
	if !s.hasObserved || observed.Before(s.firstObserved) {
		s.firstObserved = observed
	}
	if !s.hasObserved || observed.After(s.lastObserved) {
		s.lastObserved = observed
	}
	s.hasObserved = true

	if event.EventType != domain.EventAdvertised {
		effective := event.EffectiveActivityTime().UTC()
		if !s.hasActivity || effective.Before(s.firstActivity) {
			s.firstActivity = effective
		}
		if !s.hasActivity || effective.After(s.lastActivity) {
			s.lastActivity = effective
		}
		s.hasActivity = true
	}
	if event.EventType != domain.EventInvoked {
		return
	}
	s.sessions[event.SessionID] = struct{}{}
	effective := event.EffectiveActivityTime().UTC()
	if !s.hasInvocation || observed.Before(s.firstInvocationSeen) {
		s.firstInvocationSeen = observed
	}
	if !s.hasInvocation || observed.After(s.lastInvocationSeen) {
		s.lastInvocationSeen = observed
	}
	if !s.hasInvocation || effective.Before(s.firstInvocation) {
		s.firstInvocation = effective
	}
	if !s.hasInvocation || effective.After(s.lastInvocation) {
		s.lastInvocation = effective
	}
	s.hasInvocation = true
}

func (s *observationSummary) setAggregate(aggregate history.Aggregate) {
	s.aggregate = true
	s.installed = aggregate.Installed
	s.installedScopes = append([]domain.Scope(nil), aggregate.InstalledScopes...)
	sort.Slice(s.installedScopes, func(i, j int) bool { return s.installedScopes[i] < s.installedScopes[j] })
	s.observedAdvertisedSessions = aggregate.ObservedAdvertisedSessions
	s.invokedInAdvertisedSessions = aggregate.InvokedInAdvertisedSessions
	s.effectiveCoverage = aggregate.EffectiveCoverage
	s.counts[domain.EventAdvertised] = int(aggregate.AdvertisedObservations)
	s.counts[domain.EventLoaded] = int(aggregate.LoadedObservations)
	s.counts[domain.EventInvoked] = int(aggregate.Uses)
	s.distinctSessions = int(aggregate.DistinctInvocationSessions)
	for provenance, count := range aggregate.InvocationEvidence {
		if count > 0 {
			s.source[provenance] = struct{}{}
		}
	}
	if aggregate.Uses > 0 {
		s.hasInvocation = true
		s.hasActivity = true
		s.firstObserved = aggregate.FirstObservedAt.UTC()
		s.lastObserved = aggregate.LastObservedAt.UTC()
		s.firstActivity = aggregate.FirstEffectiveActivityAt.UTC()
		s.lastActivity = aggregate.LastEffectiveActivityAt.UTC()
		s.firstInvocationSeen = s.firstObserved
		s.lastInvocationSeen = s.lastObserved
		s.firstInvocation = s.firstActivity
		s.lastInvocation = s.lastActivity
	} else if aggregate.LoadedObservations > 0 {
		s.hasActivity = true
	}
	s.coverage = aggregate.Coverage
}

func (s *observationSummary) sourceNames() []string {
	result := make([]string, 0, len(s.source))
	for source := range s.source {
		result = append(result, string(source))
	}
	sort.Strings(result)
	return result
}

func buildSummaries(events []domain.UsageEvent) map[capabilityKey]*observationSummary {
	result := make(map[capabilityKey]*observationSummary)
	for _, event := range events {
		key := capabilityKey{runtime: event.Runtime, typ: event.CapabilityType, name: event.CapabilityName}
		summary := result[key]
		if summary == nil {
			summary = newObservationSummary()
			result[key] = summary
		}
		summary.add(event)
	}
	return result
}

func buildAggregateSummaries(aggregates []history.Aggregate) map[capabilityKey]*observationSummary {
	result := make(map[capabilityKey]*observationSummary, len(aggregates))
	for _, aggregate := range aggregates {
		key := capabilityKey{runtime: aggregate.Runtime, typ: aggregate.CapabilityType, name: aggregate.CapabilityName}
		summary := newObservationSummary()
		summary.setAggregate(aggregate)
		result[key] = summary
	}
	return result
}

type reportRuntimeTotals struct {
	advertised int64
	loaded     int64
	invoked    int64
	usage      int64
}

// validateReportAggregates protects the typed history report API when called
// independently of AnalyzeHistory. QueryInvocationHistory supplies one row
// per key, so duplicate rows are rejected rather than merged: session totals
// cannot safely be added across duplicate buckets.
func validateReportAggregates(input []history.Aggregate) error {
	ordered := append([]history.Aggregate(nil), input...)
	totals := make(map[domain.Runtime]reportRuntimeTotals)
	for index, aggregate := range ordered {
		if err := validateReportAggregate(aggregate); err != nil {
			return fmt.Errorf("invalid history aggregate at index %d: %w", index, err)
		}
		runtimeTotals := totals[aggregate.Runtime]
		var err error
		if runtimeTotals.advertised, err = addReportCount(runtimeTotals.advertised, aggregate.AdvertisedObservations, "advertised runtime total"); err != nil {
			return err
		}
		if runtimeTotals.loaded, err = addReportCount(runtimeTotals.loaded, aggregate.LoadedObservations, "loaded runtime total"); err != nil {
			return err
		}
		if runtimeTotals.invoked, err = addReportCount(runtimeTotals.invoked, aggregate.Uses, "invocation runtime total"); err != nil {
			return err
		}
		usageEvents, err := addReportCount(aggregate.AdvertisedObservations, aggregate.LoadedObservations, "aggregate usage event total")
		if err != nil {
			return err
		}
		if usageEvents, err = addReportCount(usageEvents, aggregate.Uses, "aggregate usage event total"); err != nil {
			return err
		}
		if runtimeTotals.usage, err = addReportCount(runtimeTotals.usage, usageEvents, "usage event runtime total"); err != nil {
			return err
		}
		totals[aggregate.Runtime] = runtimeTotals
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return reportAggregateLess(ordered[i], ordered[j])
	})
	for index := 1; index < len(ordered); index++ {
		if reportAggregateKeyEqual(ordered[index-1], ordered[index]) {
			return fmt.Errorf("duplicate history aggregate key %s/%s/%s", ordered[index].Runtime, ordered[index].CapabilityType, ordered[index].CapabilityName)
		}
	}
	return nil
}

func validateReportAggregate(aggregate history.Aggregate) error {
	if !aggregate.Runtime.Valid() {
		return fmt.Errorf("invalid runtime %q", aggregate.Runtime)
	}
	if !aggregate.CapabilityType.Valid() {
		return fmt.Errorf("invalid capability type %q", aggregate.CapabilityType)
	}
	if strings.TrimSpace(aggregate.CapabilityName) == "" {
		return errors.New("capability name is required")
	}
	counts := []struct {
		name  string
		value int64
	}{
		{name: "invocation use count", value: aggregate.Uses},
		{name: "distinct invocation sessions", value: aggregate.DistinctInvocationSessions},
		{name: "advertised observation count", value: aggregate.AdvertisedObservations},
		{name: "loaded observation count", value: aggregate.LoadedObservations},
	}
	for _, count := range counts {
		if err := validateReportCount(count.name, count.value); err != nil {
			return err
		}
	}
	if aggregate.DistinctInvocationSessions > aggregate.Uses {
		return errors.New("distinct invocation session count exceeds invocation use count")
	}
	if aggregate.Uses == 0 && aggregate.DistinctInvocationSessions != 0 {
		return errors.New("zero-use aggregate cannot have invocation sessions")
	}
	if aggregate.Uses == 0 && (aggregate.FirstObservedAt != nil || aggregate.LastObservedAt != nil || aggregate.FirstEffectiveActivityAt != nil || aggregate.LastEffectiveActivityAt != nil) {
		return errors.New("zero-use aggregate cannot have invocation timestamps")
	}
	if err := validateReportTimePair("invocation observed", aggregate.FirstObservedAt, aggregate.LastObservedAt, aggregate.Uses > 0); err != nil {
		return err
	}
	if err := validateReportTimePair("invocation effective", aggregate.FirstEffectiveActivityAt, aggregate.LastEffectiveActivityAt, aggregate.Uses > 0); err != nil {
		return err
	}
	if err := validateReportCoverage(aggregate.Coverage); err != nil {
		return err
	}
	if aggregate.ObservedAdvertisedSessions != nil {
		if err := validateReportCount("observed advertised session count", *aggregate.ObservedAdvertisedSessions); err != nil {
			return err
		}
	}
	if aggregate.InvokedInAdvertisedSessions != nil {
		if err := validateReportCount("invoked-in-advertised session count", *aggregate.InvokedInAdvertisedSessions); err != nil {
			return err
		}
		if aggregate.ObservedAdvertisedSessions == nil {
			return errors.New("invoked-in-advertised sessions require advertised session evidence")
		}
		if *aggregate.InvokedInAdvertisedSessions > *aggregate.ObservedAdvertisedSessions {
			return errors.New("invoked-in-advertised sessions exceed advertised sessions")
		}
	}
	if (aggregate.ObservedAdvertisedSessions == nil) != (aggregate.InvokedInAdvertisedSessions == nil) {
		return errors.New("advertised session evidence must include both observed and invoked session counts")
	}
	if aggregate.EffectiveCoverage != nil {
		key := history.CoverageKey{Runtime: aggregate.Runtime, CapabilityType: aggregate.CapabilityType, CapabilityName: aggregate.CapabilityName}
		if aggregate.EffectiveCoverage.Key != key {
			return errors.New("effective coverage key does not match aggregate key")
		}
		if err := aggregate.EffectiveCoverage.Validate(); err != nil {
			return fmt.Errorf("effective coverage: %w", err)
		}
	}
	if aggregate.AdvertisedObservations == 0 {
		if aggregate.ObservedAdvertisedSessions != nil {
			return errors.New("observed advertised sessions must be nil when advertised observations are zero")
		}
	} else {
		if aggregate.ObservedAdvertisedSessions == nil {
			return errors.New("observed advertised sessions are required when advertised observations are positive")
		}
		if *aggregate.ObservedAdvertisedSessions <= 0 {
			return errors.New("observed advertised sessions must be positive")
		}
		if *aggregate.ObservedAdvertisedSessions > aggregate.AdvertisedObservations {
			return errors.New("observed advertised sessions exceed advertised observations")
		}
	}
	seenScopes := make(map[domain.Scope]struct{}, len(aggregate.InstalledScopes))
	for _, scope := range aggregate.InstalledScopes {
		if !scope.Valid() {
			return fmt.Errorf("invalid installed scope %q", scope)
		}
		if _, found := seenScopes[scope]; found {
			return fmt.Errorf("duplicate installed scope %q", scope)
		}
		seenScopes[scope] = struct{}{}
	}
	for provenance, count := range aggregate.InvocationEvidence {
		if !provenance.Valid() {
			return fmt.Errorf("invalid invocation evidence provenance %q", provenance)
		}
		if err := validateReportCount("invocation evidence count", count); err != nil {
			return err
		}
		if count > aggregate.Uses {
			return fmt.Errorf("invocation evidence count for %q exceeds invocation uses", provenance)
		}
	}
	return nil
}

func validateReportCount(name string, value int64) error {
	if value < 0 {
		return fmt.Errorf("%s cannot be negative", name)
	}
	if value > maxReportInt64() {
		return fmt.Errorf("%s does not fit int", name)
	}
	return nil
}

func addReportCount(current, value int64, name string) (int64, error) {
	if err := validateReportCount(name, value); err != nil {
		return 0, err
	}
	if current > maxReportInt64()-value {
		return 0, fmt.Errorf("%s overflows int", name)
	}
	return current + value, nil
}

func maxReportInt64() int64 {
	return int64(^uint(0) >> 1)
}

func validateReportCoverage(coverage *history.Coverage) error {
	if coverage == nil {
		return nil
	}
	pairs := []struct {
		name        string
		first, last *time.Time
	}{
		{name: "inventory coverage", first: coverage.FirstInventoryObservedAt, last: coverage.LastInventoryObservedAt},
		{name: "usage coverage", first: coverage.FirstUsageObservedAt, last: coverage.LastUsageObservedAt},
		{name: "direct-hook coverage", first: coverage.FirstDirectHookObservedAt, last: coverage.LastDirectHookObservedAt},
	}
	for _, pair := range pairs {
		if err := validateReportTimePair(pair.name, pair.first, pair.last, false); err != nil {
			return err
		}
	}
	return nil
}

func validateReportTimePair(name string, first, last *time.Time, required bool) error {
	if (first == nil) != (last == nil) {
		return fmt.Errorf("%s first and last timestamps must both be set or both be nil", name)
	}
	if required && first == nil {
		return fmt.Errorf("%s timestamps are required for invocation use", name)
	}
	if first == nil {
		return nil
	}
	if first.IsZero() || last.IsZero() {
		return fmt.Errorf("%s timestamps cannot be zero", name)
	}
	if last.Before(*first) {
		return fmt.Errorf("%s last timestamp precedes first timestamp", name)
	}
	return nil
}

func reportAggregateLess(left, right history.Aggregate) bool {
	if left.Runtime != right.Runtime {
		return left.Runtime < right.Runtime
	}
	if left.CapabilityType != right.CapabilityType {
		return left.CapabilityType < right.CapabilityType
	}
	return left.CapabilityName < right.CapabilityName
}

func reportAggregateKeyEqual(left, right history.Aggregate) bool {
	return left.Runtime == right.Runtime && left.CapabilityType == right.CapabilityType && left.CapabilityName == right.CapabilityName
}

func timestamp(value time.Time, ok bool) *string {
	if !ok || value.IsZero() {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}

func safeText(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if unicode.IsControl(character) {
			builder.WriteByte(' ')
			continue
		}
		builder.WriteRune(character)
	}
	return strings.TrimSpace(builder.String())
}

func looksSensitiveToken(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"sentinel", "secret", "payload", "prompt", "response", "session", "fingerprint", "source_identity", "sourceidentity"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if len(value) == 64 {
		for _, character := range value {
			if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
				return false
			}
		}
		return true
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || strings.Contains(value, ":\\") {
		return true
	}
	for _, marker := range []string{"=/", "=~/", "=./", "=../", "=\\", "file://"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

// safeBasis preserves the useful semantic basis while avoiding accidental
// leakage when a producer includes a path or opaque identifier in free text.
func safeBasis(value string) string {
	fields := strings.Fields(safeText(value))
	for index, field := range fields {
		trimmed := strings.Trim(field, ",;()[]{}")
		if looksSensitiveToken(trimmed) {
			fields[index] = strings.Replace(field, trimmed, "[redacted]", 1)
		}
	}
	return strings.Join(fields, " ")
}

func measurementDTO(measurement domain.Measurement) Measurement {
	result := Measurement{
		Confidence: string(measurement.Confidence),
		Basis:      safeBasis(measurement.Basis),
	}
	if measurement.Confidence != domain.ConfidenceUnknown {
		value := measurement.Value
		result.Tokens = &value
	}
	return result
}

func capabilityDTO(evidence analysis.CapabilityEvidence, summary *observationSummary, asOf time.Time) Capability {
	if summary == nil {
		summary = newObservationSummary()
	}
	installed := evidence.Installed
	if !installed {
		// Every CapabilityEvidence row represents current inventory. The
		// explicit field is still useful to aggregate callers, while this
		// fallback keeps the legacy event wrapper's DTO compatible.
		installed = true
	}
	installedScopes := evidence.InstalledScopes
	if len(installedScopes) == 0 {
		installedScopes = summary.installedScopes
	}
	if len(installedScopes) == 0 && evidence.Capability.Scope.Valid() {
		installedScopes = []domain.Scope{evidence.Capability.Scope}
	}
	observedAdvertised := evidence.ObservedAdvertisedSessions
	if observedAdvertised == nil {
		observedAdvertised = summary.observedAdvertisedSessions
	}
	advertised := summary.counts[domain.EventAdvertised]
	loaded := summary.counts[domain.EventLoaded]
	invocationCount := evidence.InvocationCount
	distinctSessions := evidence.DistinctSessionCount
	firstObserved := summary.firstObserved
	lastObserved := summary.lastObserved
	firstActivity := summary.firstActivity
	lastActivity := summary.lastActivity
	hasObserved := summary.hasObserved
	hasActivity := summary.hasActivity
	firstInvocationSeen := summary.firstInvocationSeen
	lastInvocationSeen := summary.lastInvocationSeen
	firstInvocation := summary.firstInvocation
	lastInvocation := summary.lastInvocation
	hasInvocation := summary.hasInvocation
	coverage := summary.coverage
	if coverage == nil {
		coverage = evidence.Coverage
	}
	if evidence.FirstObservedAt != nil {
		firstObserved = evidence.FirstObservedAt.UTC()
		hasObserved = true
	}
	if evidence.LastObservedAt != nil {
		lastObserved = evidence.LastObservedAt.UTC()
		hasObserved = true
	}
	if evidence.FirstEffectiveActivityAt != nil {
		firstActivity = evidence.FirstEffectiveActivityAt.UTC()
		hasActivity = true
		firstInvocation = firstActivity
		firstInvocationSeen = firstObserved
		hasInvocation = true
	}
	if evidence.LastEffectiveActivityAt != nil {
		lastActivity = evidence.LastEffectiveActivityAt.UTC()
		hasActivity = true
		lastInvocation = lastActivity
		lastInvocationSeen = lastObserved
		hasInvocation = true
	}
	if summary.aggregate {
		advertised = summary.counts[domain.EventAdvertised]
		loaded = summary.counts[domain.EventLoaded]
		invocationCount = summary.counts[domain.EventInvoked]
		distinctSessions = summary.distinctSessions
		coverage = summary.coverage
	}
	invokedInAdvertised := evidence.InvokedInAdvertisedSessions
	if invokedInAdvertised == nil {
		invokedInAdvertised = summary.invokedInAdvertisedSessions
	}
	effectiveCoverage := evidence.EffectiveCoverage
	if effectiveCoverage == nil {
		effectiveCoverage = summary.effectiveCoverage
	}
	firstSeen := timestampValue(evidence.Capability.FirstSeen)
	lastSeen := timestampValue(evidence.Capability.LastSeen)
	scopes := make([]string, 0, len(installedScopes))
	for _, scope := range installedScopes {
		if scope.Valid() {
			scopes = append(scopes, string(scope))
		}
	}
	sort.Strings(scopes)
	return Capability{
		Runtime:                     string(evidence.Capability.Runtime),
		Type:                        string(evidence.Capability.Type),
		Name:                        safeText(evidence.Capability.Name),
		Installed:                   installed,
		Scope:                       string(evidence.Capability.Scope),
		InstalledScopes:             scopes,
		Enabled:                     string(evidence.Capability.Enabled),
		Advertisement:               string(evidence.Capability.Advertisement),
		MetadataExposure:            measurementDTO(evidence.MetadataTokens),
		LoadedBodyFootprint:         measurementDTO(evidence.BodyTokens),
		Status:                      string(evidence.Classification),
		Confidence:                  string(evidence.Confidence),
		CoverageConfidence:          string(evidence.CoverageConfidence),
		Basis:                       safeBasis(evidence.Basis),
		Evidence:                    safeBasis(evidence.EvidenceCoverage),
		EvidenceSources:             summary.sourceNames(),
		Advertised:                  advertised,
		ObservedAdvertisedSessions:  intPointer(observedAdvertised),
		InvokedInAdvertisedSessions: intPointer(invokedInAdvertised),
		Loaded:                      loaded,
		InvocationCount:             invocationCount,
		DistinctSessionCount:        distinctSessions,
		FirstObservedAt:             timestamp(firstObserved, hasObserved),
		LastObservedAt:              timestamp(lastObserved, hasObserved),
		FirstEffectiveActivityAt:    timestamp(firstActivity, hasActivity),
		LastEffectiveActivityAt:     timestamp(lastActivity, hasActivity),
		FirstInvocationObservedAt:   timestamp(firstInvocationSeen, hasInvocation),
		LastInvocationObservedAt:    timestamp(lastInvocationSeen, hasInvocation),
		FirstInvocationEffectiveAt:  timestamp(firstInvocation, hasInvocation),
		LastInvocationEffectiveAt:   timestamp(lastInvocation, hasInvocation),
		LastInvocationAge:           durationPointer(evidence.LastUsedAge, evidence.HasLastUsed),
		LastInvocationInFuture:      evidence.LastUsedInFuture,
		Coverage:                    coverageDTO(coverage),
		FirstSeen:                   timestampPointer(firstSeen),
		LastSeen:                    timestampPointer(lastSeen),
		EffectiveCoverage:           effectiveCoverageDTO(effectiveCoverage, asOf),
	}
}

func intPointer(value *int64) *int {
	if value == nil || *value < 0 || *value > int64(^uint(0)>>1) {
		return nil
	}
	converted := int(*value)
	return &converted
}

func coverageDTO(coverage *history.Coverage) *Coverage {
	if coverage == nil {
		return nil
	}
	return &Coverage{
		FirstInventoryObservedAt:  timestampPointer(coverage.FirstInventoryObservedAt),
		LastInventoryObservedAt:   timestampPointer(coverage.LastInventoryObservedAt),
		FirstUsageObservedAt:      timestampPointer(coverage.FirstUsageObservedAt),
		LastUsageObservedAt:       timestampPointer(coverage.LastUsageObservedAt),
		FirstDirectHookObservedAt: timestampPointer(coverage.FirstDirectHookObservedAt),
		LastDirectHookObservedAt:  timestampPointer(coverage.LastDirectHookObservedAt),
	}
}

func timestampPointer(value *time.Time) *string {
	if value == nil || value.IsZero() {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}

func timestampValue(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func durationPointer(value time.Duration, ok bool) *string {
	if !ok {
		return nil
	}
	formatted := value.String()
	return &formatted
}

func effectiveCoverageDTO(coverage *history.EffectiveCoverage, asOf time.Time) *EffectiveCoverage {
	if coverage == nil {
		return &EffectiveCoverage{Status: string(history.CoverageUnknown), CoveredDuration: nil}
	}
	status := coverage.Status
	if status == history.CoverageComplete {
		status = history.CoverageUnknown
	}
	if status != history.CoveragePartial {
		return &EffectiveCoverage{Status: string(history.CoverageUnknown), CoveredDuration: nil}
	}
	var duration time.Duration
	for _, interval := range coverage.Intervals {
		end := interval.End
		if end.IsZero() || end.After(asOf) {
			end = asOf
		}
		if end.After(interval.Start) {
			duration += end.Sub(interval.Start)
		}
	}
	if duration <= 0 {
		return &EffectiveCoverage{Status: string(history.CoverageUnknown), CoveredDuration: nil}
	}
	value := duration.String()
	return &EffectiveCoverage{Status: string(status), CoveredDuration: &value}
}

func runtimeEventsDTO(runtimeName domain.Runtime, result analysis.Report, events []domain.UsageEvent, now time.Time) Runtime {
	dto := Runtime{Runtime: string(runtimeName)}
	for _, event := range events {
		if event.Runtime != runtimeName {
			continue
		}
		dto.UsageEvents++
		switch event.EventType {
		case domain.EventAdvertised:
			dto.Advertised++
		case domain.EventLoaded:
			dto.Loaded++
		case domain.EventInvoked:
			dto.Invoked++
		}
	}
	for _, evidence := range result.Capabilities {
		if evidence.Capability.Runtime != runtimeName {
			continue
		}
		dto.Installed++
		if evidence.Capability.Advertisement == domain.AdvertisementStateFullyAdvertised || evidence.Capability.Advertisement == domain.AdvertisementStateNameOnly {
			dto.ConfiguredAdvertised++
		}
		if evidence.ActivityCount == 0 {
			dto.NoActivityObserved++
		}
		if evidence.HasLastUsed && !evidence.LastUsedInFuture && !evidence.LastUsedAt.Before(now.Add(-30*24*time.Hour)) && !evidence.LastUsedAt.After(now) {
			dto.InvokedLast30Days++
		}
	}
	return dto
}

func runtimeHistoryDTO(runtimeName domain.Runtime, result analysis.Report, aggregates []history.Aggregate, now time.Time) Runtime {
	dto := Runtime{Runtime: string(runtimeName)}
	for _, aggregate := range aggregates {
		if aggregate.Runtime != runtimeName {
			continue
		}
		dto.Advertised += int(aggregate.AdvertisedObservations)
		dto.Loaded += int(aggregate.LoadedObservations)
		dto.Invoked += int(aggregate.Uses)
		dto.UsageEvents += int(aggregate.AdvertisedObservations + aggregate.LoadedObservations + aggregate.Uses)
	}
	for _, evidence := range result.Capabilities {
		if evidence.Capability.Runtime != runtimeName {
			continue
		}
		dto.Installed++
		if evidence.Capability.Advertisement == domain.AdvertisementStateFullyAdvertised || evidence.Capability.Advertisement == domain.AdvertisementStateNameOnly {
			dto.ConfiguredAdvertised++
		}
		if evidence.ActivityCount == 0 {
			dto.NoActivityObserved++
		}
		if evidence.HasLastUsed && !evidence.LastUsedInFuture && !evidence.LastUsedAt.Before(now.Add(-30*24*time.Hour)) && !evidence.LastUsedAt.After(now) {
			dto.InvokedLast30Days++
		}
	}
	return dto
}

func findingDTO(duplicate analysis.DuplicateName) Finding {
	return Finding{
		Runtime:     string(duplicate.Runtime),
		Type:        string(duplicate.CapabilityType),
		Name:        safeText(duplicate.Name),
		Code:        "duplicate-capability",
		Severity:    string(domain.SeverityWarning),
		Confidence:  string(domain.ConfidenceObserved),
		Definitions: len(duplicate.Definitions),
		Message:     fmt.Sprintf("%d installed definitions share this capability name", len(duplicate.Definitions)),
	}
}

func buildFindings(result analysis.Report) []Finding {
	findings := make([]Finding, 0, len(result.Duplicates))
	for _, duplicate := range result.Duplicates {
		findings = append(findings, findingDTO(duplicate))
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Runtime != findings[j].Runtime {
			return findings[i].Runtime < findings[j].Runtime
		}
		if findings[i].Type != findings[j].Type {
			return findings[i].Type < findings[j].Type
		}
		return findings[i].Name < findings[j].Name
	})
	return findings
}

func buildUsageOnly(installed []analysis.CapabilityEvidence, summaries map[capabilityKey]*observationSummary, asOf time.Time) []UsageOnly {
	installedKeys := make(map[capabilityKey]struct{}, len(installed))
	for _, evidence := range installed {
		installedKeys[capabilityKey{runtime: evidence.Capability.Runtime, typ: evidence.Capability.Type, name: evidence.Capability.Name}] = struct{}{}
	}
	keys := make([]capabilityKey, 0)
	for key := range summaries {
		if _, found := installedKeys[key]; !found && !summaries[key].installed {
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
	result := make([]UsageOnly, 0, len(keys))
	for _, key := range keys {
		summary := summaries[key]
		advertised := summary.counts[domain.EventAdvertised]
		loaded := summary.counts[domain.EventLoaded]
		invocationCount := summary.counts[domain.EventInvoked]
		distinctSessions := len(summary.sessions)
		if summary.aggregate {
			distinctSessions = summary.distinctSessions
		}
		result = append(result, UsageOnly{
			Runtime:                     string(key.runtime),
			Type:                        string(key.typ),
			Name:                        safeText(key.name),
			Advertised:                  advertised,
			ObservedAdvertisedSessions:  intPointer(summary.observedAdvertisedSessions),
			InvokedInAdvertisedSessions: intPointer(summary.invokedInAdvertisedSessions),
			Loaded:                      loaded,
			InvocationCount:             invocationCount,
			DistinctSessionCount:        distinctSessions,
			EvidenceSources:             summary.sourceNames(),
			FirstObservedAt:             timestamp(summary.firstObserved, summary.hasObserved),
			LastObservedAt:              timestamp(summary.lastObserved, summary.hasObserved),
			FirstEffectiveActivityAt:    timestamp(summary.firstActivity, summary.hasActivity),
			LastEffectiveActivityAt:     timestamp(summary.lastActivity, summary.hasActivity),
			FirstInvocationObservedAt:   timestamp(summary.firstInvocationSeen, summary.hasInvocation),
			LastInvocationObservedAt:    timestamp(summary.lastInvocationSeen, summary.hasInvocation),
			FirstInvocationEffectiveAt:  timestamp(summary.firstInvocation, summary.hasInvocation),
			LastInvocationEffectiveAt:   timestamp(summary.lastInvocation, summary.hasInvocation),
			Coverage:                    coverageDTO(summary.coverage),
			EffectiveCoverage:           effectiveCoverageDTO(summary.effectiveCoverage, asOf),
		})
	}
	return result
}

type reportData struct {
	generatedAt  string
	runtimes     []Runtime
	capabilities []Capability
	findings     []Finding
	summaries    map[capabilityKey]*observationSummary
}

func buildReportData(result analysis.Report, summaries map[capabilityKey]*observationSummary, runtimes []Runtime, now time.Time) reportData {
	capabilities := make([]Capability, 0, len(result.Capabilities))
	for _, evidence := range result.Capabilities {
		key := capabilityKey{runtime: evidence.Capability.Runtime, typ: evidence.Capability.Type, name: evidence.Capability.Name}
		capabilities = append(capabilities, capabilityDTO(evidence, summaries[key], now))
	}
	return reportData{
		generatedAt:  now.Format(time.RFC3339Nano),
		runtimes:     runtimes,
		capabilities: capabilities,
		findings:     buildFindings(result),
		summaries:    summaries,
	}
}

func normalizedReportNow(now time.Time) (time.Time, error) {
	if now.IsZero() {
		return time.Time{}, errors.New("report generation time is required")
	}
	return now.UTC(), nil
}

func buildReportEvents(result analysis.Report, events []domain.UsageEvent, now time.Time) (reportData, error) {
	now, err := normalizedReportNow(now)
	if err != nil {
		return reportData{}, err
	}
	summaries := buildSummaries(events)
	runtimes := make([]Runtime, 0, 2)
	for _, runtimeName := range []domain.Runtime{domain.RuntimeClaudeCode, domain.RuntimeCodex} {
		runtimes = append(runtimes, runtimeEventsDTO(runtimeName, result, events, now))
	}
	return buildReportData(result, summaries, runtimes, now), nil
}

func buildReportHistory(result analysis.Report, aggregates []history.Aggregate, now time.Time) (reportData, error) {
	now, err := normalizedReportNow(now)
	if err != nil {
		return reportData{}, err
	}
	if err := validateReportAggregates(aggregates); err != nil {
		return reportData{}, err
	}
	summaries := buildAggregateSummaries(aggregates)
	runtimes := make([]Runtime, 0, 2)
	for _, runtimeName := range []domain.Runtime{domain.RuntimeClaudeCode, domain.RuntimeCodex} {
		runtimes = append(runtimes, runtimeHistoryDTO(runtimeName, result, aggregates, now))
	}
	return buildReportData(result, summaries, runtimes, now), nil
}

// BuildReport maps analysis and persisted events to the report JSON contract.
// The typed event signature remains the compatibility API; bounded history
// callers should use BuildReportHistory.
func BuildReport(result analysis.Report, events []domain.UsageEvent, now time.Time, staleDays int) (ReportDocument, error) {
	data, err := buildReportEvents(result, events, now)
	if err != nil {
		return ReportDocument{}, err
	}
	usageOnly := buildUsageOnly(result.Capabilities, data.summaries, now)
	if usageOnly == nil {
		usageOnly = []UsageOnly{}
	}
	return ReportDocument{
		SchemaVersion:  SchemaVersion,
		GeneratedAt:    data.generatedAt,
		StaleAfterDays: staleDays,
		Runtimes:       data.runtimes,
		Capabilities:   data.capabilities,
		UsageOnly:      usageOnly,
		Findings:       data.findings,
	}, nil
}

// BuildReportHistory maps analysis and bounded history aggregates to the
// report JSON contract. It never needs the complete usage event table.
func BuildReportHistory(result analysis.Report, aggregates []history.Aggregate, now time.Time, staleDays int) (ReportDocument, error) {
	data, err := buildReportHistory(result, aggregates, now)
	if err != nil {
		return ReportDocument{}, err
	}
	usageOnly := buildUsageOnly(result.Capabilities, data.summaries, now)
	if usageOnly == nil {
		usageOnly = []UsageOnly{}
	}
	return ReportDocument{
		SchemaVersion:  SchemaVersion,
		GeneratedAt:    data.generatedAt,
		StaleAfterDays: staleDays,
		Runtimes:       data.runtimes,
		Capabilities:   data.capabilities,
		UsageOnly:      usageOnly,
		Findings:       data.findings,
	}, nil
}

// BuildStale maps analysis and persisted events to the stale JSON contract.
// The typed event signature remains the compatibility API; bounded history
// callers should use BuildStaleHistory.
func BuildStale(result analysis.Report, events []domain.UsageEvent, now time.Time, staleDays int) (StaleDocument, error) {
	data, err := buildReportEvents(result, events, now)
	if err != nil {
		return StaleDocument{}, err
	}
	return StaleDocument{
		SchemaVersion:  SchemaVersion,
		GeneratedAt:    data.generatedAt,
		StaleAfterDays: staleDays,
		Runtimes:       data.runtimes,
		Capabilities:   data.capabilities,
		Findings:       data.findings,
	}, nil
}

// BuildStaleHistory maps analysis and bounded history aggregates to the stale
// JSON contract. It never needs the complete usage event table.
func BuildStaleHistory(result analysis.Report, aggregates []history.Aggregate, now time.Time, staleDays int) (StaleDocument, error) {
	data, err := buildReportHistory(result, aggregates, now)
	if err != nil {
		return StaleDocument{}, err
	}
	return StaleDocument{
		SchemaVersion:  SchemaVersion,
		GeneratedAt:    data.generatedAt,
		StaleAfterDays: staleDays,
		Runtimes:       data.runtimes,
		Capabilities:   data.capabilities,
		Findings:       data.findings,
	}, nil
}

// WriteJSON writes a DTO using the same deterministic encoder convention as
// hooks status JSON.
func WriteJSON(out io.Writer, value interface{}) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
