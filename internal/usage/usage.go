// Package usage owns the stable, privacy-preserving JSON contract for the
// usage command. It deliberately accepts history aggregates rather than
// exposing store or domain structs at the command boundary.
package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
)

const SchemaVersion = 2

// Period describes the closed UTC interval used for a usage query.
type Period struct {
	Days      int    `json:"days"`
	Start     string `json:"start"`
	End       string `json:"end"`
	Inclusive bool   `json:"inclusive"`
}

// Filters are the normalized filters applied to a usage query. A nil value
// means that no filter was supplied. The mcp filter remains mcp in this DTO
// even though its query expands to mcp_server and mcp_tool internally.
type Filters struct {
	Runtime *string `json:"runtime"`
	Type    *string `json:"type"`
}

// Coverage records observation windows without claiming continuity or
// complete lifetime coverage. The fields are nullable because any source can
// be absent from a local store.
type Coverage struct {
	FirstInventoryObservedAt  *string `json:"first_inventory_observed_at"`
	LastInventoryObservedAt   *string `json:"last_inventory_observed_at"`
	FirstUsageObservedAt      *string `json:"first_usage_observed_at"`
	LastUsageObservedAt       *string `json:"last_usage_observed_at"`
	FirstDirectHookObservedAt *string `json:"first_direct_hook_observed_at"`
	LastDirectHookObservedAt  *string `json:"last_direct_hook_observed_at"`
}

// EffectiveCoverage summarizes only confirmed capture/presence intersection.
// Unknown coverage has a null duration; a string zero is never evidence.
type EffectiveCoverage struct {
	Status          string  `json:"status"`
	CoveredDuration *string `json:"covered_duration"`
}

// Provenance contains per-source invocation subtotals and the deterministic
// set of sources that contributed invocation evidence. A duplicated
// hook/transcript identity is represented once in Uses but once in each
// applicable source subtotal.
type Provenance struct {
	Hook       int64    `json:"hook"`
	Transcript int64    `json:"transcript"`
	Import     int64    `json:"import"`
	Sources    []string `json:"sources"`
}

// Monthly is one UTC calendar-month invocation subtotal. Month is the first
// instant of the month in UTC and zero-use buckets are intentional evidence,
// not forecasts.
type Monthly struct {
	Month            string `json:"month"`
	Uses             int64  `json:"uses"`
	DistinctSessions int64  `json:"distinct_sessions"`
}

// Capability is one returned runtime/type/name history bucket. It contains
// only normalized metadata and aggregate counts; paths, hashes, identifiers,
// source identities, fingerprints, payloads, and raw errors are excluded.
type Capability struct {
	Runtime                     string             `json:"runtime"`
	Type                        string             `json:"type"`
	Name                        string             `json:"name"`
	Installed                   bool               `json:"installed"`
	InstalledScopes             []string           `json:"installed_scopes"`
	Uses                        int64              `json:"uses"`
	DistinctSessions            int64              `json:"distinct_sessions"`
	FirstObservedAt             *string            `json:"first_observed_at"`
	LastObservedAt              *string            `json:"last_observed_at"`
	FirstEffectiveActivityAt    *string            `json:"first_effective_activity_at"`
	LastEffectiveActivityAt     *string            `json:"last_effective_activity_at"`
	Provenance                  Provenance         `json:"provenance"`
	AdvertisedObservations      int64              `json:"advertised_observations"`
	AdvertisedSessions          *int64             `json:"advertised_sessions"`
	InvokedInAdvertisedSessions *int64             `json:"invoked_in_advertised_sessions"`
	LoadedObservations          int64              `json:"loaded_observations"`
	EffectiveCoverage           *EffectiveCoverage `json:"effective_coverage"`
	ObservationOnlyCoverage     *Coverage          `json:"observation_only_coverage"`
	Monthly                     []Monthly          `json:"monthly,omitempty"`
}

// UsageDocument is the versioned JSON contract emitted by usage --json.
type UsageDocument struct {
	SchemaVersion int          `json:"schema_version"`
	GeneratedAt   string       `json:"generated_at"`
	Period        Period       `json:"period"`
	Filters       Filters      `json:"filters"`
	Capabilities  []Capability `json:"capabilities"`
}

// BuildInput is the safe builder input. Aggregates and monthly rows are
// already privacy-safe store DTOs; the builder copies only their allow-listed
// fields into UsageDocument.
type BuildInput struct {
	GeneratedAt    time.Time
	Days           int
	RuntimeFilter  *string
	TypeFilter     *string
	Aggregates     []history.Aggregate
	Monthly        []history.MonthlyAggregate
	IncludeMonthly bool
}

// Build converts history aggregates to the explicit usage JSON DTO. It does
// not query a store and never serializes the input structs directly.
func Build(input BuildInput) (UsageDocument, error) {
	generatedAt := input.GeneratedAt.UTC()
	if input.GeneratedAt.IsZero() {
		return UsageDocument{}, errors.New("usage generated-at time is required")
	}
	if input.Days <= 0 {
		return UsageDocument{}, errors.New("usage days must be greater than zero")
	}
	if int64(input.Days) > maxPeriodDays {
		return UsageDocument{}, errors.New("usage days exceed supported UTC period range")
	}
	duration, err := periodDuration(input.Days)
	if err != nil {
		return UsageDocument{}, err
	}
	start := generatedAt.Add(-duration)
	period := Period{
		Days:      input.Days,
		Start:     start.Format(time.RFC3339Nano),
		End:       generatedAt.Format(time.RFC3339Nano),
		Inclusive: true,
	}
	runtimeFilter, err := normalizeRuntimeFilter(input.RuntimeFilter)
	if err != nil {
		return UsageDocument{}, err
	}
	typeFilter, err := normalizeTypeFilter(input.TypeFilter)
	if err != nil {
		return UsageDocument{}, err
	}
	filters := Filters{Runtime: runtimeFilter, Type: typeFilter}

	ordered := append([]history.Aggregate(nil), input.Aggregates...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return aggregateLess(ordered[i], ordered[j])
	})
	for index, aggregate := range ordered {
		if err := validateAggregate(aggregate); err != nil {
			return UsageDocument{}, fmt.Errorf("invalid usage aggregate at index %d: %w", index, err)
		}
	}
	for index := 1; index < len(ordered); index++ {
		if sameAggregateKey(ordered[index-1], ordered[index]) {
			return UsageDocument{}, fmt.Errorf("duplicate usage aggregate key %s/%s/%s", ordered[index].Runtime, ordered[index].CapabilityType, ordered[index].CapabilityName)
		}
	}

	monthlyByKey := make(map[aggregateKey]map[time.Time]history.MonthlyAggregate)
	for index, monthly := range input.Monthly {
		if err := validateMonthly(monthly); err != nil {
			return UsageDocument{}, fmt.Errorf("invalid monthly aggregate at index %d: %w", index, err)
		}
		key := aggregateKey{runtime: monthly.Runtime, typ: monthly.CapabilityType, name: monthly.CapabilityName}
		months := monthlyByKey[key]
		if months == nil {
			months = make(map[time.Time]history.MonthlyAggregate)
			monthlyByKey[key] = months
		}
		month := monthStart(monthly.Month)
		if _, found := months[month]; found {
			return UsageDocument{}, fmt.Errorf("duplicate monthly aggregate key %s/%s/%s/%s", monthly.Runtime, monthly.CapabilityType, monthly.CapabilityName, month.Format("2006-01"))
		}
		months[month] = monthly
	}

	rows := make([]Capability, 0, len(ordered))
	for _, aggregate := range ordered {
		row := capabilityDTO(aggregate, generatedAt)
		if input.IncludeMonthly {
			row.Monthly = fillMonthly(monthlyByKey[aggregateKey{runtime: aggregate.Runtime, typ: aggregate.CapabilityType, name: aggregate.CapabilityName}], start, generatedAt)
		}
		rows = append(rows, row)
	}
	if rows == nil {
		rows = []Capability{}
	}
	return UsageDocument{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   generatedAt.Format(time.RFC3339Nano),
		Period:        period,
		Filters:       filters,
		Capabilities:  rows,
	}, nil
}

// WriteJSON writes the DTO with stable encoder settings used by CLI JSON
// commands.
func WriteJSON(out io.Writer, document UsageDocument) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(document)
}

type aggregateKey struct {
	runtime domain.Runtime
	typ     domain.CapabilityType
	name    string
}

func aggregateLess(left, right history.Aggregate) bool {
	if left.Runtime != right.Runtime {
		return left.Runtime < right.Runtime
	}
	if left.CapabilityType != right.CapabilityType {
		return left.CapabilityType < right.CapabilityType
	}
	return left.CapabilityName < right.CapabilityName
}

func sameAggregateKey(left, right history.Aggregate) bool {
	return left.Runtime == right.Runtime && left.CapabilityType == right.CapabilityType && left.CapabilityName == right.CapabilityName
}

func validateAggregate(aggregate history.Aggregate) error {
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
		{"uses", aggregate.Uses},
		{"distinct sessions", aggregate.DistinctInvocationSessions},
		{"advertised observations", aggregate.AdvertisedObservations},
		{"loaded observations", aggregate.LoadedObservations},
	}
	for _, count := range counts {
		if count.value < 0 {
			return fmt.Errorf("%s cannot be negative", count.name)
		}
	}
	if aggregate.DistinctInvocationSessions > aggregate.Uses {
		return errors.New("distinct sessions exceed uses")
	}
	if aggregate.Uses == 0 && aggregate.DistinctInvocationSessions != 0 {
		return errors.New("zero-use aggregate cannot have sessions")
	}
	if aggregate.Uses == 0 && (aggregate.FirstObservedAt != nil || aggregate.LastObservedAt != nil || aggregate.FirstEffectiveActivityAt != nil || aggregate.LastEffectiveActivityAt != nil) {
		return errors.New("zero-use aggregate cannot have invocation timestamps")
	}
	if err := validateTimePair(aggregate.FirstObservedAt, aggregate.LastObservedAt); err != nil {
		return fmt.Errorf("observed timestamps: %w", err)
	}
	if err := validateTimePair(aggregate.FirstEffectiveActivityAt, aggregate.LastEffectiveActivityAt); err != nil {
		return fmt.Errorf("effective timestamps: %w", err)
	}
	if aggregate.Uses > 0 {
		if aggregate.FirstObservedAt == nil {
			return errors.New("invocation observed timestamps are required for invocation use")
		}
		if aggregate.FirstEffectiveActivityAt == nil {
			return errors.New("invocation effective timestamps are required for invocation use")
		}
	}
	if aggregate.ObservedAdvertisedSessions != nil {
		if *aggregate.ObservedAdvertisedSessions < 0 || *aggregate.ObservedAdvertisedSessions > aggregate.AdvertisedObservations {
			return errors.New("advertised sessions are outside observation count")
		}
	}
	if aggregate.InvokedInAdvertisedSessions != nil {
		if *aggregate.InvokedInAdvertisedSessions < 0 {
			return errors.New("invoked-in-advertised sessions cannot be negative")
		}
		if aggregate.ObservedAdvertisedSessions == nil {
			return errors.New("invoked-in-advertised sessions require advertised sessions")
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
			return errors.New("advertised sessions require advertised observations")
		}
	} else {
		if aggregate.ObservedAdvertisedSessions == nil {
			return errors.New("advertised sessions are required when advertised observations are positive")
		}
		if *aggregate.ObservedAdvertisedSessions <= 0 {
			return errors.New("advertised sessions must be positive")
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
			return fmt.Errorf("invalid provenance %q", provenance)
		}
		if count < 0 || count > aggregate.Uses {
			return fmt.Errorf("provenance count for %q is outside uses", provenance)
		}
	}
	return validateCoverage(aggregate.Coverage)
}

func validateMonthly(monthly history.MonthlyAggregate) error {
	if !monthly.Runtime.Valid() {
		return fmt.Errorf("invalid runtime %q", monthly.Runtime)
	}
	if !monthly.CapabilityType.Valid() {
		return fmt.Errorf("invalid capability type %q", monthly.CapabilityType)
	}
	if strings.TrimSpace(monthly.CapabilityName) == "" {
		return errors.New("capability name is required")
	}
	if monthly.Month.IsZero() {
		return errors.New("month is required")
	}
	if monthly.Uses < 0 || monthly.DistinctInvocationSessions < 0 {
		return errors.New("monthly counts cannot be negative")
	}
	if monthly.DistinctInvocationSessions > monthly.Uses {
		return errors.New("monthly distinct sessions exceed uses")
	}
	return nil
}

func validateTimePair(first, last *time.Time) error {
	if (first == nil) != (last == nil) {
		return errors.New("first and last must both be set or nil")
	}
	if first == nil {
		return nil
	}
	if first.IsZero() || last.IsZero() {
		return errors.New("timestamps cannot be zero")
	}
	if last.Before(*first) {
		return errors.New("last precedes first")
	}
	return nil
}

func validateCoverage(coverage *history.Coverage) error {
	if coverage == nil {
		return nil
	}
	pairs := []coveragePair{
		{name: "inventory", first: coverage.FirstInventoryObservedAt, last: coverage.LastInventoryObservedAt},
		{name: "usage", first: coverage.FirstUsageObservedAt, last: coverage.LastUsageObservedAt},
		{name: "direct hook", first: coverage.FirstDirectHookObservedAt, last: coverage.LastDirectHookObservedAt},
	}
	for _, pair := range pairs {
		if err := validateTimePair(pair.first, pair.last); err != nil {
			return fmt.Errorf("%s coverage: %w", pair.name, err)
		}
	}
	return nil
}

type coveragePair struct {
	name        string
	first, last *time.Time
}

const maxPeriodDays = int64(1<<63-1) / int64(24*time.Hour)

func periodDuration(days int) (time.Duration, error) {
	if days <= 0 {
		return 0, errors.New("usage days must be greater than zero")
	}
	if int64(days) > maxPeriodDays {
		return 0, errors.New("usage days exceed supported UTC period range")
	}
	return time.Duration(days) * 24 * time.Hour, nil
}

func capabilityDTO(aggregate history.Aggregate, asOf time.Time) Capability {
	scopes := make([]string, 0, len(aggregate.InstalledScopes))
	for _, scope := range aggregate.InstalledScopes {
		if scope.Valid() {
			scopes = append(scopes, string(scope))
		}
	}
	sort.Strings(scopes)
	sources := make([]string, 0, len(aggregate.InvocationEvidence))
	for source, count := range aggregate.InvocationEvidence {
		if count > 0 {
			sources = append(sources, string(source))
		}
	}
	sort.Strings(sources)
	return Capability{
		Runtime:                  string(aggregate.Runtime),
		Type:                     string(aggregate.CapabilityType),
		Name:                     safeName(aggregate.CapabilityName),
		Installed:                aggregate.Installed,
		InstalledScopes:          scopes,
		Uses:                     aggregate.Uses,
		DistinctSessions:         aggregate.DistinctInvocationSessions,
		FirstObservedAt:          timestamp(aggregate.FirstObservedAt),
		LastObservedAt:           timestamp(aggregate.LastObservedAt),
		FirstEffectiveActivityAt: timestamp(aggregate.FirstEffectiveActivityAt),
		LastEffectiveActivityAt:  timestamp(aggregate.LastEffectiveActivityAt),
		Provenance: Provenance{
			Hook:       aggregate.InvocationEvidence[domain.ProvenanceHook],
			Transcript: aggregate.InvocationEvidence[domain.ProvenanceTranscript],
			Import:     aggregate.InvocationEvidence[domain.ProvenanceImport],
			Sources:    sources,
		},
		AdvertisedObservations:      aggregate.AdvertisedObservations,
		AdvertisedSessions:          cloneInt64(aggregate.ObservedAdvertisedSessions),
		InvokedInAdvertisedSessions: cloneInt64(aggregate.InvokedInAdvertisedSessions),
		LoadedObservations:          aggregate.LoadedObservations,
		ObservationOnlyCoverage:     coverageDTO(aggregate.Coverage),
		EffectiveCoverage:           effectiveCoverageDTO(aggregate.EffectiveCoverage, asOf),
	}
}

func effectiveCoverageDTO(coverage *history.EffectiveCoverage, asOf time.Time) *EffectiveCoverage {
	if coverage == nil || coverage.Status != history.CoveragePartial {
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
	return &EffectiveCoverage{Status: string(history.CoveragePartial), CoveredDuration: &value}
}

func fillMonthly(existing map[time.Time]history.MonthlyAggregate, start, end time.Time) []Monthly {
	first := monthStart(start)
	last := monthStart(end)
	result := make([]Monthly, 0)
	for month := first; !month.After(last); month = month.AddDate(0, 1, 0) {
		value := existing[month]
		result = append(result, Monthly{
			Month:            month.Format(time.RFC3339Nano),
			Uses:             value.Uses,
			DistinctSessions: value.DistinctInvocationSessions,
		})
	}
	return result
}

func monthStart(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func timestamp(value *time.Time) *string {
	if value == nil || value.IsZero() {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}

func coverageDTO(coverage *history.Coverage) *Coverage {
	if coverage == nil {
		return nil
	}
	return &Coverage{
		FirstInventoryObservedAt:  timestamp(coverage.FirstInventoryObservedAt),
		LastInventoryObservedAt:   timestamp(coverage.LastInventoryObservedAt),
		FirstUsageObservedAt:      timestamp(coverage.FirstUsageObservedAt),
		LastUsageObservedAt:       timestamp(coverage.LastUsageObservedAt),
		FirstDirectHookObservedAt: timestamp(coverage.FirstDirectHookObservedAt),
		LastDirectHookObservedAt:  timestamp(coverage.LastDirectHookObservedAt),
	}
}

func safeName(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			builder.WriteByte(' ')
		} else {
			builder.WriteRune(character)
		}
	}
	return strings.TrimSpace(builder.String())
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func normalizeRuntimeFilter(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	normalized := strings.ToLower(strings.TrimSpace(*value))
	switch normalized {
	case "claude":
		normalized = string(domain.RuntimeClaudeCode)
	case "claude-code", "codex":
	default:
		return nil, fmt.Errorf("invalid usage runtime filter %q", normalized)
	}
	return &normalized, nil
}

func normalizeTypeFilter(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	normalized := strings.ToLower(strings.TrimSpace(*value))
	switch normalized {
	case "skill", "mcp", "tool", "agent":
	default:
		return nil, fmt.Errorf("invalid usage type filter %q", normalized)
	}
	return &normalized, nil
}
