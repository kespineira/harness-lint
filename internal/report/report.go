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
)

const SchemaVersion = 1

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

// Capability contains one installed definition's analysis and safe activity
// history.  It intentionally excludes source paths, hashes, session IDs,
// source identities, and fingerprints.
type Capability struct {
	Runtime                    string   `json:"runtime"`
	Type                       string   `json:"type"`
	Name                       string   `json:"name"`
	Scope                      string   `json:"scope"`
	Enabled                    string   `json:"enabled"`
	Advertisement              string   `json:"advertisement"`
	Status                     string   `json:"status"`
	Confidence                 string   `json:"confidence"`
	CoverageConfidence         string   `json:"coverage_confidence"`
	Basis                      string   `json:"basis"`
	Evidence                   string   `json:"evidence"`
	EvidenceSources            []string `json:"evidence_sources"`
	Advertised                 int      `json:"advertised"`
	Loaded                     int      `json:"loaded"`
	InvocationCount            int      `json:"invocation_count"`
	DistinctSessionCount       int      `json:"distinct_sessions"`
	FirstObservedAt            *string  `json:"first_observed_at"`
	LastObservedAt             *string  `json:"last_observed_at"`
	FirstEffectiveActivityAt   *string  `json:"first_effective_activity_at"`
	LastEffectiveActivityAt    *string  `json:"last_effective_activity_at"`
	FirstInvocationObservedAt  *string  `json:"first_invocation_observed_at"`
	LastInvocationObservedAt   *string  `json:"last_invocation_observed_at"`
	FirstInvocationEffectiveAt *string  `json:"first_invocation_effective_at"`
	LastInvocationEffectiveAt  *string  `json:"last_invocation_effective_at"`
	LastInvocationAge          *string  `json:"last_invocation_age"`
	LastInvocationInFuture     bool     `json:"last_invocation_in_future"`
}

// UsageOnly represents observed usage with no matching current inventory.
// It keeps the same event distinctions as Capability without exposing event
// identifiers.
type UsageOnly struct {
	Runtime                    string   `json:"runtime"`
	Type                       string   `json:"type"`
	Name                       string   `json:"name"`
	Advertised                 int      `json:"advertised"`
	Loaded                     int      `json:"loaded"`
	InvocationCount            int      `json:"invocation_count"`
	DistinctSessionCount       int      `json:"distinct_sessions"`
	EvidenceSources            []string `json:"evidence_sources"`
	FirstObservedAt            *string  `json:"first_observed_at"`
	LastObservedAt             *string  `json:"last_observed_at"`
	FirstEffectiveActivityAt   *string  `json:"first_effective_activity_at"`
	LastEffectiveActivityAt    *string  `json:"last_effective_activity_at"`
	FirstInvocationObservedAt  *string  `json:"first_invocation_observed_at"`
	LastInvocationObservedAt   *string  `json:"last_invocation_observed_at"`
	FirstInvocationEffectiveAt *string  `json:"first_invocation_effective_at"`
	LastInvocationEffectiveAt  *string  `json:"last_invocation_effective_at"`
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

func capabilityDTO(evidence analysis.CapabilityEvidence, summary *observationSummary) Capability {
	if summary == nil {
		summary = newObservationSummary()
	}
	return Capability{
		Runtime:                    string(evidence.Capability.Runtime),
		Type:                       string(evidence.Capability.Type),
		Name:                       safeText(evidence.Capability.Name),
		Scope:                      string(evidence.Capability.Scope),
		Enabled:                    string(evidence.Capability.Enabled),
		Advertisement:              string(evidence.Capability.Advertisement),
		Status:                     string(evidence.Classification),
		Confidence:                 string(evidence.Confidence),
		CoverageConfidence:         string(evidence.CoverageConfidence),
		Basis:                      safeBasis(evidence.Basis),
		Evidence:                   safeBasis(evidence.EvidenceCoverage),
		EvidenceSources:            summary.sourceNames(),
		Advertised:                 summary.counts[domain.EventAdvertised],
		Loaded:                     summary.counts[domain.EventLoaded],
		InvocationCount:            evidence.InvocationCount,
		DistinctSessionCount:       evidence.DistinctSessionCount,
		FirstObservedAt:            timestamp(summary.firstObserved, summary.hasObserved),
		LastObservedAt:             timestamp(summary.lastObserved, summary.hasObserved),
		FirstEffectiveActivityAt:   timestamp(summary.firstActivity, summary.hasActivity),
		LastEffectiveActivityAt:    timestamp(summary.lastActivity, summary.hasActivity),
		FirstInvocationObservedAt:  timestamp(summary.firstInvocationSeen, summary.hasInvocation),
		LastInvocationObservedAt:   timestamp(summary.lastInvocationSeen, summary.hasInvocation),
		FirstInvocationEffectiveAt: timestamp(summary.firstInvocation, summary.hasInvocation),
		LastInvocationEffectiveAt:  timestamp(summary.lastInvocation, summary.hasInvocation),
		LastInvocationAge:          durationPointer(evidence.LastUsedAge, evidence.HasLastUsed),
		LastInvocationInFuture:     evidence.LastUsedInFuture,
	}
}

func durationPointer(value time.Duration, ok bool) *string {
	if !ok {
		return nil
	}
	formatted := value.String()
	return &formatted
}

func runtimeDTO(runtimeName domain.Runtime, result analysis.Report, events []domain.UsageEvent, now time.Time) Runtime {
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

func buildUsageOnly(installed []analysis.CapabilityEvidence, summaries map[capabilityKey]*observationSummary) []UsageOnly {
	installedKeys := make(map[capabilityKey]struct{}, len(installed))
	for _, evidence := range installed {
		installedKeys[capabilityKey{runtime: evidence.Capability.Runtime, typ: evidence.Capability.Type, name: evidence.Capability.Name}] = struct{}{}
	}
	keys := make([]capabilityKey, 0)
	for key := range summaries {
		if _, found := installedKeys[key]; !found {
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
		result = append(result, UsageOnly{
			Runtime:                    string(key.runtime),
			Type:                       string(key.typ),
			Name:                       safeText(key.name),
			Advertised:                 summary.counts[domain.EventAdvertised],
			Loaded:                     summary.counts[domain.EventLoaded],
			InvocationCount:            summary.counts[domain.EventInvoked],
			DistinctSessionCount:       len(summary.sessions),
			EvidenceSources:            summary.sourceNames(),
			FirstObservedAt:            timestamp(summary.firstObserved, summary.hasObserved),
			LastObservedAt:             timestamp(summary.lastObserved, summary.hasObserved),
			FirstEffectiveActivityAt:   timestamp(summary.firstActivity, summary.hasActivity),
			LastEffectiveActivityAt:    timestamp(summary.lastActivity, summary.hasActivity),
			FirstInvocationObservedAt:  timestamp(summary.firstInvocationSeen, summary.hasInvocation),
			LastInvocationObservedAt:   timestamp(summary.lastInvocationSeen, summary.hasInvocation),
			FirstInvocationEffectiveAt: timestamp(summary.firstInvocation, summary.hasInvocation),
			LastInvocationEffectiveAt:  timestamp(summary.lastInvocation, summary.hasInvocation),
		})
	}
	return result
}

func buildCommon(result analysis.Report, events []domain.UsageEvent, now time.Time) (string, []Runtime, []Capability, []Finding, map[capabilityKey]*observationSummary, error) {
	if now.IsZero() {
		return "", nil, nil, nil, nil, errors.New("report generation time is required")
	}
	now = now.UTC()
	summaries := buildSummaries(events)
	runtimes := make([]Runtime, 0, 2)
	for _, runtimeName := range []domain.Runtime{domain.RuntimeClaudeCode, domain.RuntimeCodex} {
		runtimes = append(runtimes, runtimeDTO(runtimeName, result, events, now))
	}
	capabilities := make([]Capability, 0, len(result.Capabilities))
	for _, evidence := range result.Capabilities {
		key := capabilityKey{runtime: evidence.Capability.Runtime, typ: evidence.Capability.Type, name: evidence.Capability.Name}
		capabilities = append(capabilities, capabilityDTO(evidence, summaries[key]))
	}
	findings := buildFindings(result)
	return now.Format(time.RFC3339Nano), runtimes, capabilities, findings, summaries, nil
}

// BuildReport maps analysis and persisted events to the report JSON contract.
func BuildReport(result analysis.Report, events []domain.UsageEvent, now time.Time, staleDays int) (ReportDocument, error) {
	generatedAt, runtimes, capabilities, findings, summaries, err := buildCommon(result, events, now)
	if err != nil {
		return ReportDocument{}, err
	}
	usageOnly := buildUsageOnly(result.Capabilities, summaries)
	if usageOnly == nil {
		usageOnly = []UsageOnly{}
	}
	return ReportDocument{
		SchemaVersion:  SchemaVersion,
		GeneratedAt:    generatedAt,
		StaleAfterDays: staleDays,
		Runtimes:       runtimes,
		Capabilities:   capabilities,
		UsageOnly:      usageOnly,
		Findings:       findings,
	}, nil
}

// BuildStale maps analysis and persisted events to the stale JSON contract.
func BuildStale(result analysis.Report, events []domain.UsageEvent, now time.Time, staleDays int) (StaleDocument, error) {
	generatedAt, runtimes, capabilities, findings, _, err := buildCommon(result, events, now)
	if err != nil {
		return StaleDocument{}, err
	}
	return StaleDocument{
		SchemaVersion:  SchemaVersion,
		GeneratedAt:    generatedAt,
		StaleAfterDays: staleDays,
		Runtimes:       runtimes,
		Capabilities:   capabilities,
		Findings:       findings,
	}, nil
}

// WriteJSON writes a DTO using the same deterministic encoder convention as
// hooks status JSON.
func WriteJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
