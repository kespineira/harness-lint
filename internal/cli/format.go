package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/kespineira/harness-lint/internal/analysis"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
	reportdto "github.com/kespineira/harness-lint/internal/report"
	usagedto "github.com/kespineira/harness-lint/internal/usage"
)

func printFinding(out io.Writer, finding domain.Finding) {
	capability := "unknown"
	if finding.CapabilityType.Valid() {
		capability = string(finding.CapabilityType) + "/" + cleanText(finding.CapabilityName)
	}
	fmt.Fprintf(out, "finding runtime=%s code=%s severity=%s confidence=%s capability=%s message=%s\n", finding.Runtime, cleanText(finding.Code), finding.Severity, finding.Confidence, capability, cleanText(finding.Message))
}

func printRuntimeCounts(out io.Writer, result analysis.Report, aggregates []history.Aggregate, now time.Time) {
	for _, runtimeName := range knownRuntimes {
		installed := 0
		configuredAdvertised := 0
		advertisedEvents, loadedEvents, invokedEvents := 0, 0, 0
		usedLast30, noActivityObserved := 0, 0
		for _, aggregate := range aggregates {
			if aggregate.Runtime != runtimeName {
				continue
			}
			advertisedEvents += int(aggregate.AdvertisedObservations)
			loadedEvents += int(aggregate.LoadedObservations)
			invokedEvents += int(aggregate.Uses)
		}
		for _, evidence := range result.Capabilities {
			if evidence.Capability.Runtime != runtimeName {
				continue
			}
			installed++
			if evidence.Capability.Advertisement == domain.AdvertisementStateFullyAdvertised || evidence.Capability.Advertisement == domain.AdvertisementStateNameOnly {
				configuredAdvertised++
			}
			if evidence.ActivityCount == 0 {
				noActivityObserved++
			}
			if evidence.HasLastUsed && !evidence.LastUsedAt.Before(now.Add(-lastUsedWindow)) && !evidence.LastUsedAt.After(now) {
				usedLast30++
			}
		}
		usageEvents := advertisedEvents + loadedEvents + invokedEvents
		fmt.Fprintf(out, "runtime=%s installed=%d advertised=%d loaded=%d invoked=%d configured-advertised=%d used-last-30d=%d no-activity-observed=%d usage-events=%d\n", runtimeName, installed, advertisedEvents, loadedEvents, invokedEvents, configuredAdvertised, usedLast30, noActivityObserved, usageEvents)
	}
}

func printCapabilityEvidenceWithHistory(out io.Writer, result analysis.Report, aggregates historyAggregateIndex, now time.Time) {
	if len(result.Capabilities) == 0 {
		fmt.Fprintln(out, "no current capabilities")
		return
	}
	document, err := reportdto.BuildStaleHistory(result, aggregateValues(aggregates), now, 0)
	if err != nil {
		// Analyze already validates the clock. Keep this formatter defensive for
		// direct unit callers without turning a presentation error into a panic.
		fmt.Fprintf(out, "report evidence unavailable: %s\n", cleanText(err.Error()))
		return
	}
	fmt.Fprintln(out, "capabilities:")
	for _, capability := range document.Capabilities {
		usedLast30 := "no"
		if capability.LastInvocationAge != nil && !capability.LastInvocationInFuture {
			if age, parseErr := time.ParseDuration(*capability.LastInvocationAge); parseErr == nil && age <= lastUsedWindow {
				usedLast30 = "yes"
			}
		}
		aggregate, found := aggregates[historyAggregateKey{runtime: capability.Runtime, typ: capability.Type, name: capability.Name}]
		advertisedSessions := "unknown"
		invokedAdvertisedSessions := "unknown"
		provenance := "hook=0,transcript=0,import=0"
		coverage := "unknown"
		if found {
			if aggregate.ObservedAdvertisedSessions != nil {
				advertisedSessions = fmt.Sprintf("%d", *aggregate.ObservedAdvertisedSessions)
			}
			if aggregate.InvokedInAdvertisedSessions != nil {
				invokedAdvertisedSessions = formatAdvertisedSessionEvidence(aggregate.InvokedInAdvertisedSessions, aggregate.ObservedAdvertisedSessions)
			}
			provenance = formatProvenance(aggregate.InvocationEvidence)
			coverage = formatObservationCoverage(aggregate.Coverage)
		}
		evidence := cleanText(capability.Evidence)
		fmt.Fprintf(out, "  runtime=%s type=%s name=%s status=%s advertised=%d advertised-sessions=%s efficiency=%s loaded=%d invocation-uses=%d distinct-sessions=%d provenance=%s evidence-sources=%s exposure=%s used-last-30d=%s first-seen=%s last-seen=%s first-observed=%s last-observed=%s first-invocation-effective=%s last-invocation-effective=%s metadata-exposure=%s body-footprint=%s confidence=%s coverage-confidence=%s coverage=%s effective-coverage=%s basis=%s evidence=%s\n",
			capability.Runtime,
			capability.Type,
			cleanText(capability.Name),
			capability.Status,
			capability.Advertised,
			advertisedSessions,
			invokedAdvertisedSessions,
			capability.Loaded,
			capability.InvocationCount,
			capability.DistinctSessionCount,
			provenance,
			joinSources(capability.EvidenceSources),
			capability.Advertisement,
			usedLast30,
			humanInventoryTimestamp(capability.FirstSeen),
			humanInventoryTimestamp(capability.LastSeen),
			humanObservedTimestamp(capability.FirstObservedAt),
			humanObservedTimestamp(capability.LastObservedAt),
			humanInvocationTimestamp(capability.FirstInvocationEffectiveAt),
			humanInvocationTimestamp(capability.LastInvocationEffectiveAt),
			formatReportMeasurement(capability.MetadataExposure),
			formatReportMeasurement(capability.LoadedBodyFootprint),
			capability.Confidence,
			capability.CoverageConfidence,
			coverage,
			formatModeledCoverage(capability.EffectiveCoverage),
			cleanText(capability.Basis),
			evidence)
	}
}

func humanObservedTimestamp(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "never observed"
	}
	return *value
}

func humanInventoryTimestamp(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "unknown"
	}
	return *value
}

func formatModeledCoverage(coverage *reportdto.EffectiveCoverage) string {
	if coverage == nil || strings.TrimSpace(coverage.Status) == "" {
		return "unknown"
	}
	if coverage.CoveredDuration == nil {
		return cleanText(coverage.Status) + "/unknown"
	}
	return cleanText(coverage.Status) + "/" + cleanText(*coverage.CoveredDuration)
}

func formatAdvertisedSessionEvidence(invoked, advertised *int64) string {
	if invoked == nil || advertised == nil {
		return "unknown"
	}
	return fmt.Sprintf("invoked in %d / %d advertised sessions", *invoked, *advertised)
}

func humanInvocationTimestamp(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "no invocation observed"
	}
	return *value
}

func joinSources(sources []string) string {
	if len(sources) == 0 {
		return "none"
	}
	return strings.Join(sources, ",")
}

func printDuplicateFindings(out io.Writer, result analysis.Report) {
	if len(result.Duplicates) == 0 {
		return
	}
	fmt.Fprintln(out, "findings:")
	for _, duplicate := range result.Duplicates {
		fmt.Fprintf(out, "  finding runtime=%s code=duplicate-capability severity=warning confidence=observed capability=%s/%s definitions=%d\n", duplicate.Runtime, duplicate.CapabilityType, cleanText(duplicate.Name), len(duplicate.Definitions))
	}
}

func printUsageOnlyWithAnalysis(out io.Writer, result analysis.Report, aggregates historyAggregateIndex, now time.Time) {
	document, err := reportdto.BuildReportHistory(result, aggregateValues(aggregates), now, 0)
	if err != nil || len(document.UsageOnly) == 0 {
		return
	}
	fmt.Fprintln(out, "usage-only (observed usage is not installed inventory):")
	for _, usage := range document.UsageOnly {
		aggregate, found := aggregates[historyAggregateKey{runtime: usage.Runtime, typ: usage.Type, name: usage.Name}]
		advertisedSessions := "unknown"
		invokedAdvertisedSessions := "unknown"
		provenance := "hook=0,transcript=0,import=0"
		coverage := "unknown"
		if found {
			if aggregate.ObservedAdvertisedSessions != nil {
				advertisedSessions = fmt.Sprintf("%d", *aggregate.ObservedAdvertisedSessions)
			}
			if aggregate.InvokedInAdvertisedSessions != nil {
				invokedAdvertisedSessions = formatAdvertisedSessionEvidence(aggregate.InvokedInAdvertisedSessions, aggregate.ObservedAdvertisedSessions)
			}
			provenance = formatProvenance(aggregate.InvocationEvidence)
			coverage = formatObservationCoverage(aggregate.Coverage)
		}
		fmt.Fprintf(out, "  runtime=%s type=%s name=%s advertised=%d advertised-sessions=%s efficiency=%s loaded=%d invocation-uses=%d distinct-sessions=%d provenance=%s first-observed=%s last-observed=%s first-invocation-effective=%s last-invocation-effective=%s evidence-sources=%s coverage=%s effective-coverage=%s status=usage-only\n",
			usage.Runtime,
			usage.Type,
			cleanText(usage.Name),
			usage.Advertised,
			advertisedSessions,
			invokedAdvertisedSessions,
			usage.Loaded,
			usage.InvocationCount,
			usage.DistinctSessionCount,
			provenance,
			humanObservedTimestamp(usage.FirstObservedAt),
			humanObservedTimestamp(usage.LastObservedAt),
			humanInvocationTimestamp(usage.FirstInvocationEffectiveAt),
			humanInvocationTimestamp(usage.LastInvocationEffectiveAt),
			joinSources(usage.EvidenceSources),
			coverage,
			formatModeledCoverage(usage.EffectiveCoverage))
	}
}

func formatMeasurementSummary(summary analysis.MeasurementSummary) string {
	confidence := summary.Confidence
	if summary.KnownCount == 0 {
		return string(domain.ConfidenceUnknown)
	}
	value := fmt.Sprintf("%d tokens (%s", summary.Value, confidence)
	if summary.UnknownCount > 0 {
		value += "; partial"
	}
	return value + ")"
}

func cleanText(value string) string {
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

type historyAggregateKey struct {
	runtime string
	typ     string
	name    string
}

type historyAggregateIndex map[historyAggregateKey]history.Aggregate

func buildAggregateIndex(aggregates []history.Aggregate) historyAggregateIndex {
	result := make(historyAggregateIndex, len(aggregates))
	for _, aggregate := range aggregates {
		key := historyAggregateKey{runtime: string(aggregate.Runtime), typ: string(aggregate.CapabilityType), name: aggregate.CapabilityName}
		result[key] = aggregate
	}
	return result
}

func aggregateValues(index historyAggregateIndex) []history.Aggregate {
	result := make([]history.Aggregate, 0, len(index))
	for _, aggregate := range index {
		result = append(result, aggregate)
	}
	// The report DTO sorts and validates its input. This conversion is only
	// needed by the compatibility DTO builder; preserve deterministic ordering
	// here as well so the formatter never depends on map iteration.
	sort.Slice(result, func(left, right int) bool {
		if result[left].Runtime != result[right].Runtime {
			return result[left].Runtime < result[right].Runtime
		}
		if result[left].CapabilityType != result[right].CapabilityType {
			return result[left].CapabilityType < result[right].CapabilityType
		}
		return result[left].CapabilityName < result[right].CapabilityName
	})
	return result
}

func formatProvenance(counts map[domain.Provenance]int64) string {
	return fmt.Sprintf("hook=%d,transcript=%d,import=%d", counts[domain.ProvenanceHook], counts[domain.ProvenanceTranscript], counts[domain.ProvenanceImport])
}

func formatObservationCoverage(coverage *history.Coverage) string {
	if coverage == nil {
		return "unknown"
	}
	parts := make([]string, 0, 3)
	if coverage.FirstInventoryObservedAt != nil || coverage.LastInventoryObservedAt != nil {
		parts = append(parts, "inventory")
	}
	if coverage.FirstUsageObservedAt != nil || coverage.LastUsageObservedAt != nil {
		parts = append(parts, "usage")
	}
	if coverage.FirstDirectHookObservedAt != nil || coverage.LastDirectHookObservedAt != nil {
		parts = append(parts, "direct-hook")
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return "observation-only(" + strings.Join(parts, ",") + "); lifetime activity coverage unknown"
}

func formatReportMeasurement(measurement reportdto.Measurement) string {
	if measurement.Tokens == nil {
		return "unknown(" + cleanText(measurement.Confidence) + ")"
	}
	return fmt.Sprintf("%d tokens(%s)", *measurement.Tokens, cleanText(measurement.Confidence))
}

func printUsageDocument(out io.Writer, document usagedto.UsageDocument) {
	fmt.Fprintf(out, "usage generated-at=%s period-start=%s period-end=%s days=%d inclusive=%t\n", document.GeneratedAt, document.Period.Start, document.Period.End, document.Period.Days, document.Period.Inclusive)
	fmt.Fprintf(out, "filters runtime=%s type=%s\n", usageFilterText(document.Filters.Runtime), usageFilterText(document.Filters.Type))
	if len(document.Capabilities) == 0 {
		fmt.Fprintln(out, "no usage capabilities returned; coverage is observation-only and lifetime activity coverage is unknown")
		return
	}
	currentRuntime := ""
	for _, capability := range document.Capabilities {
		if capability.Runtime != currentRuntime {
			currentRuntime = capability.Runtime
			fmt.Fprintf(out, "runtime=%s\n", capability.Runtime)
		}
		observation := usageObservationLabel(capability)
		advertisedSessions := "unknown"
		invokedAdvertisedSessions := "unknown"
		if capability.AdvertisedSessions != nil {
			advertisedSessions = fmt.Sprintf("%d", *capability.AdvertisedSessions)
		}
		if capability.InvokedInAdvertisedSessions != nil {
			invokedAdvertisedSessions = formatAdvertisedSessionEvidence(capability.InvokedInAdvertisedSessions, capability.AdvertisedSessions)
		}
		fmt.Fprintf(out, "  type=%s name=%s installed=%t scopes=%s observation=%s uses=%d sessions=%d provenance=hook:%d,transcript:%d,import:%d sources=%s advertised=%d advertised-sessions=%s efficiency=%s loaded=%d coverage=%s effective-coverage=%s\n",
			capability.Type,
			cleanText(capability.Name),
			capability.Installed,
			joinSources(capability.InstalledScopes),
			observation,
			capability.Uses,
			capability.DistinctSessions,
			capability.Provenance.Hook,
			capability.Provenance.Transcript,
			capability.Provenance.Import,
			joinSources(capability.Provenance.Sources),
			capability.AdvertisedObservations,
			advertisedSessions,
			invokedAdvertisedSessions,
			capability.LoadedObservations,
			usageCoverageText(capability.ObservationOnlyCoverage),
			formatUsageModeledCoverage(capability.EffectiveCoverage))
		if len(capability.Monthly) > 0 {
			fmt.Fprint(out, "    monthly UTC:")
			for _, month := range capability.Monthly {
				fmt.Fprintf(out, " %s uses=%d sessions=%d", month.Month, month.Uses, month.DistinctSessions)
			}
			fmt.Fprintln(out)
		}
	}
}

func formatUsageModeledCoverage(coverage *usagedto.EffectiveCoverage) string {
	if coverage == nil || strings.TrimSpace(coverage.Status) == "" {
		return "unknown"
	}
	if coverage.CoveredDuration == nil {
		return cleanText(coverage.Status) + "/unknown"
	}
	return cleanText(coverage.Status) + "/" + cleanText(*coverage.CoveredDuration)
}

func usageFilterText(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "none"
	}
	return cleanText(*value)
}

func usageObservationLabel(capability usagedto.Capability) string {
	if capability.Uses > 0 && capability.LastObservedAt != nil {
		return "last observed " + *capability.LastObservedAt
	}
	if capability.AdvertisedObservations > 0 || capability.LoadedObservations > 0 || capability.ObservationOnlyCoverage != nil && (capability.ObservationOnlyCoverage.FirstUsageObservedAt != nil || capability.ObservationOnlyCoverage.LastUsageObservedAt != nil || capability.ObservationOnlyCoverage.FirstDirectHookObservedAt != nil || capability.ObservationOnlyCoverage.LastDirectHookObservedAt != nil) {
		return "not observed in this period"
	}
	return "never observed"
}

func usageCoverageText(coverage *usagedto.Coverage) string {
	if coverage == nil {
		return "observation-only coverage unknown"
	}
	parts := make([]string, 0, 3)
	if coverage.FirstInventoryObservedAt != nil || coverage.LastInventoryObservedAt != nil {
		parts = append(parts, "inventory")
	}
	if coverage.FirstUsageObservedAt != nil || coverage.LastUsageObservedAt != nil {
		parts = append(parts, "usage")
	}
	if coverage.FirstDirectHookObservedAt != nil || coverage.LastDirectHookObservedAt != nil {
		parts = append(parts, "direct-hook")
	}
	if len(parts) == 0 {
		return "observation-only coverage unknown"
	}
	return "observation-only(" + strings.Join(parts, ",") + "); lifetime activity coverage unknown"
}
