package cli

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/kespineira/harness-lint/internal/analysis"
	"github.com/kespineira/harness-lint/internal/domain"
	reportdto "github.com/kespineira/harness-lint/internal/report"
)

func printFinding(out io.Writer, finding domain.Finding) {
	capability := "unknown"
	if finding.CapabilityType.Valid() {
		capability = string(finding.CapabilityType) + "/" + cleanText(finding.CapabilityName)
	}
	fmt.Fprintf(out, "finding runtime=%s code=%s severity=%s confidence=%s capability=%s message=%s\n", finding.Runtime, cleanText(finding.Code), finding.Severity, finding.Confidence, capability, cleanText(finding.Message))
}

func printRuntimeCounts(out io.Writer, result analysis.Report, events []domain.UsageEvent, now time.Time) {
	for _, runtimeName := range knownRuntimes {
		installed := 0
		configuredAdvertised := 0
		advertisedEvents, loadedEvents, invokedEvents := 0, 0, 0
		usedLast30, noActivityObserved := 0, 0
		for _, event := range events {
			if event.Runtime != runtimeName {
				continue
			}
			switch event.EventType {
			case domain.EventAdvertised:
				advertisedEvents++
			case domain.EventLoaded:
				loadedEvents++
			case domain.EventInvoked:
				invokedEvents++
			}
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
		usageEvents := 0
		for _, event := range events {
			if event.Runtime == runtimeName {
				usageEvents++
			}
		}
		fmt.Fprintf(out, "runtime=%s installed=%d advertised=%d loaded=%d invoked=%d configured-advertised=%d used-last-30d=%d no-activity-observed=%d usage-events=%d\n", runtimeName, installed, advertisedEvents, loadedEvents, invokedEvents, configuredAdvertised, usedLast30, noActivityObserved, usageEvents)
	}
}

func printCapabilityEvidenceWithEvents(out io.Writer, result analysis.Report, events []domain.UsageEvent, now time.Time) {
	if len(result.Capabilities) == 0 {
		fmt.Fprintln(out, "no current capabilities")
		return
	}
	document, err := reportdto.BuildStale(result, events, now, 0)
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
		fmt.Fprintf(out, "  runtime=%s type=%s name=%s status=%s advertised=%d loaded=%d invocation-uses=%d distinct-sessions=%d exposure=%s used-last-30d=%s first-observed=%s last-observed=%s first-invocation-effective=%s last-invocation-effective=%s evidence-sources=%s confidence=%s coverage-confidence=%s basis=%s evidence=%s\n",
			capability.Runtime,
			capability.Type,
			cleanText(capability.Name),
			capability.Status,
			capability.Advertised,
			capability.Loaded,
			capability.InvocationCount,
			capability.DistinctSessionCount,
			capability.Advertisement,
			usedLast30,
			humanObservedTimestamp(capability.FirstObservedAt),
			humanObservedTimestamp(capability.LastObservedAt),
			humanInvocationTimestamp(capability.FirstInvocationEffectiveAt),
			humanInvocationTimestamp(capability.LastInvocationEffectiveAt),
			joinSources(capability.EvidenceSources),
			capability.Confidence,
			capability.CoverageConfidence,
			cleanText(capability.Basis),
			cleanText(capability.Evidence))
	}
}

func humanObservedTimestamp(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "never observed"
	}
	return *value
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

func printUsageOnlyWithAnalysis(out io.Writer, result analysis.Report, events []domain.UsageEvent, now time.Time) {
	document, err := reportdto.BuildReport(result, events, now, 0)
	if err != nil || len(document.UsageOnly) == 0 {
		return
	}
	fmt.Fprintln(out, "usage-only (observed usage is not installed inventory):")
	for _, usage := range document.UsageOnly {
		fmt.Fprintf(out, "  runtime=%s type=%s name=%s advertised=%d loaded=%d invocation-uses=%d distinct-sessions=%d first-observed=%s last-observed=%s first-invocation-effective=%s last-invocation-effective=%s evidence-sources=%s status=usage-only\n",
			usage.Runtime,
			usage.Type,
			cleanText(usage.Name),
			usage.Advertised,
			usage.Loaded,
			usage.InvocationCount,
			usage.DistinctSessionCount,
			humanObservedTimestamp(usage.FirstObservedAt),
			humanObservedTimestamp(usage.LastObservedAt),
			humanInvocationTimestamp(usage.FirstInvocationEffectiveAt),
			humanInvocationTimestamp(usage.LastInvocationEffectiveAt),
			joinSources(usage.EvidenceSources))
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
