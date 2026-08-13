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
		usedLast30, neverUsed := 0, 0
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
				neverUsed++
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
		fmt.Fprintf(out, "runtime=%s installed=%d advertised=%d loaded=%d invoked=%d configured-advertised=%d used-last-30d=%d never-used=%d usage-events=%d\n", runtimeName, installed, advertisedEvents, loadedEvents, invokedEvents, configuredAdvertised, usedLast30, neverUsed, usageEvents)
	}
}

func printCapabilityEvidence(out io.Writer, result analysis.Report, now time.Time) {
	if len(result.Capabilities) == 0 {
		fmt.Fprintln(out, "no current capabilities")
		return
	}
	fmt.Fprintln(out, "capabilities:")
	for _, evidence := range result.Capabilities {
		lastUsed := "never"
		usedLast30 := "no"
		if evidence.HasLastUsed {
			lastUsed = evidence.LastUsedAt.UTC().Format(time.RFC3339)
			if !evidence.LastUsedAt.Before(now.Add(-lastUsedWindow)) && !evidence.LastUsedAt.After(now) {
				usedLast30 = "yes"
			}
		}
		fmt.Fprintf(out, "  runtime=%s type=%s name=%s status=%s advertised=%d loaded=%d invoked=%d exposure=%s used-last-30d=%s last-used=%s evidence=%s source=%s\n", evidence.Capability.Runtime, evidence.Capability.Type, cleanText(evidence.Capability.Name), evidence.Classification, evidence.EventCount(domain.EventAdvertised), evidence.EventCount(domain.EventLoaded), evidence.EventCount(domain.EventInvoked), evidence.Capability.Advertisement, usedLast30, lastUsed, cleanText(evidence.Basis), cleanText(evidence.Capability.Source))
	}
}

type usageKey struct {
	runtime domain.Runtime
	typ     domain.CapabilityType
	name    string
}

func printUsageOnly(out io.Writer, installed []analysis.CapabilityEvidence, events []domain.UsageEvent) {
	installedKeys := make(map[usageKey]struct{}, len(installed))
	for _, evidence := range installed {
		installedKeys[usageKey{runtime: evidence.Capability.Runtime, typ: evidence.Capability.Type, name: evidence.Capability.Name}] = struct{}{}
	}
	type usageCounts struct {
		advertised int
		loaded     int
		invoked    int
	}
	counts := make(map[usageKey]*usageCounts)
	for _, event := range events {
		key := usageKey{runtime: event.Runtime, typ: event.CapabilityType, name: event.CapabilityName}
		if _, ok := installedKeys[key]; ok {
			continue
		}
		count := counts[key]
		if count == nil {
			count = &usageCounts{}
			counts[key] = count
		}
		switch event.EventType {
		case domain.EventAdvertised:
			count.advertised++
		case domain.EventLoaded:
			count.loaded++
		case domain.EventInvoked:
			count.invoked++
		}
	}
	keys := make([]usageKey, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
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
	if len(keys) == 0 {
		return
	}
	fmt.Fprintln(out, "usage-only (observed usage is not installed inventory):")
	for _, key := range keys {
		count := counts[key]
		fmt.Fprintf(out, "  runtime=%s type=%s name=%s advertised=%d loaded=%d invoked=%d status=usage-only\n", key.runtime, key.typ, cleanText(key.name), count.advertised, count.loaded, count.invoked)
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
