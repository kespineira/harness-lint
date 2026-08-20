package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/kespineira/harness-lint/internal/analysis"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
	"github.com/kespineira/harness-lint/internal/presentation"
)

// reportCapability is the human-facing identity of one installed definition.
// Scope comes from the current definition, not from usage history: usage
// events deliberately do not identify a source or scope.
type reportCapability struct {
	evidence analysis.CapabilityEvidence
	scope    domain.Scope
}

func reportCapabilities(result analysis.Report) []reportCapability {
	items := make([]reportCapability, 0, len(result.Capabilities))
	for _, evidence := range result.Capabilities {
		items = append(items, reportCapability{evidence: evidence, scope: evidence.Capability.Scope})
	}
	sort.SliceStable(items, func(left, right int) bool {
		return reportCapabilityLess(items[left], items[right])
	})
	return items
}

func reportCapabilityLess(left, right reportCapability) bool {
	if left.evidence.Capability.Runtime != right.evidence.Capability.Runtime {
		return left.evidence.Capability.Runtime < right.evidence.Capability.Runtime
	}
	if left.evidence.Capability.Type != right.evidence.Capability.Type {
		return left.evidence.Capability.Type < right.evidence.Capability.Type
	}
	if left.evidence.Capability.Name != right.evidence.Capability.Name {
		return left.evidence.Capability.Name < right.evidence.Capability.Name
	}
	if left.scope != right.scope {
		return left.scope < right.scope
	}
	// Analysis already gives duplicate definitions a deterministic order. Use
	// source/hash only as a private tie-breaker; neither is rendered.
	if left.evidence.Capability.Source != right.evidence.Capability.Source {
		return left.evidence.Capability.Source < right.evidence.Capability.Source
	}
	return left.evidence.Capability.Hash < right.evidence.Capability.Hash
}

func reportCapabilityStatusLess(left, right reportCapability) bool {
	leftRank := reportStatusRank(left.evidence.Classification)
	rightRank := reportStatusRank(right.evidence.Classification)
	if leftRank != rightRank {
		return leftRank < rightRank
	}
	return reportCapabilityLess(left, right)
}

func reportStatusRank(value analysis.Classification) int {
	switch value {
	case analysis.STALE:
		return 0
	case analysis.REVIEW:
		return 1
	case analysis.KEEP:
		return 2
	default:
		return 3
	}
}

func reportTable(renderer presentation.HumanRenderer, headers []string, rows [][]string) string {
	return humanTable(renderer, headers, rows, 2)
}

func reportWrap(renderer presentation.HumanRenderer, value string, indent int) string {
	return humanWrap(renderer, value, indent)
}

func reportKeyValues(renderer presentation.HumanRenderer, values []presentation.KeyValue, indent int) string {
	return humanKeyValues(renderer, values, indent)
}

func writeReportTable(out io.Writer, renderer presentation.HumanRenderer, headers []string, rows [][]string) {
	if table := reportTable(renderer, headers, rows); table != "" {
		fmt.Fprintln(out, indentHumanBlock(table, 2))
	}
}

func renderReportView(out io.Writer, renderer presentation.HumanRenderer, all, verbose bool, result analysis.Report, aggregates []history.Aggregate, now time.Time, staleDays int) {
	items := reportCapabilities(result)
	displayed := items
	attention := reportAttention(items)
	if !all {
		displayed = attention
		if len(displayed) > 5 {
			displayed = displayed[:5]
		}
	}

	fmt.Fprintln(out, "Harness report")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Stale threshold: %s\n", humanDayCount(renderer, staleDays))
	if verbose {
		fmt.Fprintf(out, "  As of %s\n", renderer.Timestamp(now))
	}
	fmt.Fprintln(out)

	renderReportRuntimeOverview(out, renderer, result)
	fmt.Fprintln(out)
	renderReportObservationNote(out, renderer, items)
	fmt.Fprintln(out)

	if all {
		fmt.Fprintf(out, "Capabilities (%s)\n", renderer.Integer(int64(len(items))))
		if len(items) == 0 {
			fmt.Fprintln(out, "  No current capabilities were discovered.")
		} else {
			writeReportTable(out, renderer,
				[]string{"Status", "Runtime", "Type", "Name", "Scope", "Uses", "Last used"},
				reportCapabilityRows(renderer, items))
		}
	} else {
		fmt.Fprintln(out, "Needs attention")
		if len(displayed) == 0 {
			fmt.Fprintln(out, "  No stale or review capabilities need attention.")
		} else {
			writeReportTable(out, renderer,
				[]string{"Status", "Runtime", "Type", "Name", "Scope", "Last used"},
				reportAttentionRows(renderer, displayed))
			if len(attention) > len(displayed) {
				writeReportText(out, renderer, fmt.Sprintf("Showing %s of %s attention items. Use `harness-lint report --all` to explore all capabilities.", renderer.Integer(int64(len(displayed))), renderer.Integer(int64(len(attention)))), 2)
			}
		}
	}
	fmt.Fprintln(out)

	renderTopUsed(out, renderer, items)
	fmt.Fprintln(out)
	renderReportTotals(out, renderer, result, aggregates)
	fmt.Fprintln(out)
	renderReportExploreHints(out, renderer, result, aggregates, all)

	if verbose {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Details")
		if len(displayed) == 0 {
			fmt.Fprintln(out, "  No capability details to expand.")
		} else {
			for index, item := range displayed {
				if index > 0 {
					fmt.Fprintln(out)
				}
				renderCapabilityDetails(out, renderer, item, staleDays, now)
			}
		}
	}
}

func reportAttention(items []reportCapability) []reportCapability {
	result := make([]reportCapability, 0, len(items))
	for _, item := range items {
		if item.evidence.Classification == analysis.STALE || item.evidence.Classification == analysis.REVIEW {
			result = append(result, item)
		}
	}
	sort.SliceStable(result, func(left, right int) bool {
		return reportCapabilityStatusLess(result[left], result[right])
	})
	return result
}

func reportCapabilityRows(renderer presentation.HumanRenderer, items []reportCapability) [][]string {
	return reportRows(renderer, items, true)
}

func reportAttentionRows(renderer presentation.HumanRenderer, items []reportCapability) [][]string {
	return reportRows(renderer, items, false)
}

func reportRows(renderer presentation.HumanRenderer, items []reportCapability, includeUses bool) [][]string {
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		evidence := item.evidence
		row := []string{
			reportStatus(renderer, evidence.Classification),
			renderer.Runtime(string(evidence.Capability.Runtime)),
			renderer.Type(string(evidence.Capability.Type)),
			cleanText(evidence.Capability.Name),
			scopeLabel(item.scope),
		}
		if includeUses {
			row = append(row, renderer.Integer(int64(evidence.InvocationCount)))
		}
		row = append(row, reportLastUsed(renderer, evidence))
		rows = append(rows, row)
	}
	return rows
}

func reportStatus(renderer presentation.HumanRenderer, classification analysis.Classification) string {
	if classification == "" {
		return renderer.Status("unknown")
	}
	return renderer.Status(string(classification))
}

func reportLastUsed(renderer presentation.HumanRenderer, evidence analysis.CapabilityEvidence) string {
	if !evidence.HasLastUsed {
		return "not observed"
	}
	return renderer.RelativeTime(evidence.LastUsedAt)
}

func scopeLabel(scope domain.Scope) string {
	if scope == "" {
		return "unknown"
	}
	return string(scope)
}

type reportRuntimeOverview struct {
	installed int
	used      int
	review    int
	stale     int
}

func renderReportRuntimeOverview(out io.Writer, renderer presentation.HumanRenderer, result analysis.Report) {
	totals := make(map[domain.Runtime]reportRuntimeOverview)
	for _, runtimeName := range knownRuntimes {
		totals[runtimeName] = reportRuntimeOverview{}
	}
	for _, item := range result.Capabilities {
		row := totals[item.Capability.Runtime]
		row.installed++
		if item.InvocationCount > 0 {
			row.used++
		}
		switch item.Classification {
		case analysis.REVIEW:
			row.review++
		case analysis.STALE:
			row.stale++
		}
		totals[item.Capability.Runtime] = row
	}
	runtimes := make([]domain.Runtime, 0, len(totals))
	for runtimeName := range totals {
		runtimes = append(runtimes, runtimeName)
	}
	sort.SliceStable(runtimes, func(left, right int) bool {
		leftRank, rightRank := runtimeOrder(runtimes[left]), runtimeOrder(runtimes[right])
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return runtimes[left] < runtimes[right]
	})

	fmt.Fprintln(out, "Overview")
	rows := make([][]string, 0, len(runtimes))
	for _, runtimeName := range runtimes {
		row := totals[runtimeName]
		rows = append(rows, []string{
			renderer.Runtime(string(runtimeName)),
			renderer.Integer(int64(row.installed)),
			renderer.Integer(int64(row.used)),
			renderer.Integer(int64(row.review)),
			renderer.Integer(int64(row.stale)),
		})
	}
	writeReportTable(out, renderer, []string{"Runtime", "Installed", "Used", "Review", "Stale"}, rows)
}

func runtimeOrder(value domain.Runtime) int {
	for index, known := range knownRuntimes {
		if value == known {
			return index
		}
	}
	return len(knownRuntimes)
}

func renderReportObservationNote(out io.Writer, renderer presentation.HumanRenderer, items []reportCapability) {
	fmt.Fprintln(out, "Observation")
	note := "Only local observations are shown. Missing observations do not prove non-use. Lifetime coverage remains unknown unless a positive capture/presence intersection is shown."
	fmt.Fprintln(out, indentHumanBlock(reportWrap(renderer, note, 2), 2))
	unknownAdvertisement := 0
	unknownSessionEvidence := 0
	for _, item := range items {
		if item.evidence.Capability.Advertisement == domain.AdvertisementStateUnknown {
			unknownAdvertisement++
		}
		if item.evidence.ObservedAdvertisedSessions == nil {
			unknownSessionEvidence++
		}
	}
	if unknownAdvertisement > 0 {
		writeReportText(out, renderer, fmt.Sprintf("Advertisement is unknown for %s; no exposure is inferred.", humanCount(renderer, unknownAdvertisement, "capability", "capabilities")), 2)
	}
	if unknownSessionEvidence > 0 {
		writeReportText(out, renderer, "Per-session advertisement evidence is not available for every capability.", 2)
	}
}

func renderTopUsed(out io.Writer, renderer presentation.HumanRenderer, items []reportCapability) {
	used := topUsedCapabilities(items)
	if len(used) > 3 {
		used = used[:3]
	}
	fmt.Fprintln(out, "Most used")
	if len(used) == 0 {
		fmt.Fprintln(out, "  No invocations were observed for current capabilities.")
		return
	}
	rows := make([][]string, 0, len(used))
	for _, item := range used {
		evidence := item.evidence
		rows = append(rows, []string{
			renderer.Runtime(string(evidence.Capability.Runtime)),
			renderer.Type(string(evidence.Capability.Type)),
			cleanText(evidence.Capability.Name),
			scopeLabel(item.scope),
			renderer.Integer(int64(evidence.InvocationCount)),
			reportLastUsed(renderer, evidence),
		})
	}
	writeReportTable(out, renderer, []string{"Runtime", "Type", "Name", "Scope", "Uses", "Last used"}, rows)
}

func topUsedCapabilities(items []reportCapability) []reportCapability {
	used := make([]reportCapability, 0, len(items))
	for _, item := range items {
		if item.evidence.InvocationCount > 0 {
			used = append(used, item)
		}
	}
	sort.SliceStable(used, func(left, right int) bool {
		if used[left].evidence.InvocationCount != used[right].evidence.InvocationCount {
			return used[left].evidence.InvocationCount > used[right].evidence.InvocationCount
		}
		return reportCapabilityLess(used[left], used[right])
	})
	return used
}

func renderReportTotals(out io.Writer, renderer presentation.HumanRenderer, result analysis.Report, aggregates []history.Aggregate) {
	var advertised, loaded, invoked int64
	for _, aggregate := range aggregates {
		advertised += aggregate.AdvertisedObservations
		loaded += aggregate.LoadedObservations
		invoked += aggregate.Uses
	}
	statusCounts := map[analysis.Classification]int{}
	used := 0
	for _, item := range result.Capabilities {
		statusCounts[item.Classification]++
		if item.InvocationCount > 0 {
			used++
		}
	}
	fmt.Fprintln(out, "Totals")
	fmt.Fprintf(out, "  %s installed · %s used · %s total observations\n",
		renderer.Integer(int64(len(result.Capabilities))),
		renderer.Integer(int64(used)),
		renderer.Integer(advertised+loaded+invoked))
	fmt.Fprintf(out, "  %s advertised · %s loaded · %s invoked\n",
		renderer.Integer(advertised), renderer.Integer(loaded), renderer.Integer(invoked))
	fmt.Fprintf(out, "  %s stale · %s review · %s keep\n",
		renderer.Integer(int64(statusCounts[analysis.STALE])),
		renderer.Integer(int64(statusCounts[analysis.REVIEW])),
		renderer.Integer(int64(statusCounts[analysis.KEEP])))
}

func renderReportExploreHints(out io.Writer, renderer presentation.HumanRenderer, result analysis.Report, aggregates []history.Aggregate, all bool) {
	fmt.Fprintln(out, "Explore")
	if !all && len(result.Capabilities) > len(reportAttention(reportCapabilities(result))) {
		writeReportText(out, renderer, "Use `harness-lint report --all` to inspect every current capability.", 2)
	}
	if len(result.Capabilities) > 0 {
		writeReportText(out, renderer, "Use `harness-lint explain <name>` for evidence and rationale for one capability.", 2)
	}
	usageOnly := 0
	for _, aggregate := range aggregates {
		if !aggregate.Installed {
			usageOnly++
		}
	}
	if usageOnly > 0 {
		verb := "are"
		if usageOnly == 1 {
			verb = "is"
		}
		writeReportText(out, renderer, fmt.Sprintf("%s %s not in current inventory; this does not prove installation or removal.", humanCount(renderer, usageOnly, "observed usage-only name", "observed usage-only names"), verb), 2)
		writeReportText(out, renderer, "Use `harness-lint usage` or `harness-lint usage --json` to inspect that evidence.", 2)
	}
	if len(result.Capabilities) == 0 && usageOnly == 0 {
		writeReportText(out, renderer, "Run `harness-lint scan` to refresh current inventory and observations.", 2)
	}
}

func writeReportText(out io.Writer, renderer presentation.HumanRenderer, value string, indent int) {
	writeHumanText(out, renderer, value, indent)
}

func renderCapabilityDetails(out io.Writer, renderer presentation.HumanRenderer, item reportCapability, staleDays int, now time.Time) {
	evidence := item.evidence
	writeReportText(out, renderer, fmt.Sprintf("%s · %s · %s · %s", renderer.Runtime(string(evidence.Capability.Runtime)), renderer.Type(string(evidence.Capability.Type)), cleanText(evidence.Capability.Name), scopeLabel(item.scope)), 2)
	fmt.Fprintf(out, "    Decision       %s\n", reportStatus(renderer, evidence.Classification))
	writeReportText(out, renderer, "Basis          "+reportClassificationBasis(evidence, staleDays), 4)
	writeReportText(out, renderer, "Interpretation  "+reportInterpretation(evidence), 4)
	fmt.Fprintln(out, "    Evidence")
	details := []presentation.KeyValue{
		{Key: "Advertisement", Value: advertisementLabel(evidence.Capability.Advertisement)},
		{Key: "Enabled", Value: string(evidence.Capability.Enabled)},
		{Key: "Uses", Value: renderer.Integer(int64(evidence.InvocationCount))},
		{Key: "Loaded", Value: renderer.Integer(int64(evidence.EventCount(domain.EventLoaded)))},
		{Key: "Advertised", Value: renderer.Integer(int64(evidence.EventCount(domain.EventAdvertised)))},
		{Key: "Sessions", Value: renderer.Integer(int64(evidence.DistinctSessionCount))},
		{Key: "Metadata", Value: humanMeasurement(evidence.MetadataTokens, renderer)},
		{Key: "Body when loaded", Value: humanMeasurement(evidence.BodyTokens, renderer)},
		{Key: "Coverage basis", Value: cleanText(evidence.EvidenceCoverage)},
		{Key: "Modeled coverage", Value: modeledCoverageLabel(evidence.EffectiveCoverage, now, renderer)},
	}
	fmt.Fprintln(out, indentHumanBlock(reportKeyValues(renderer, details, 6), 6))
	fmt.Fprintln(out, "    Exact timestamps")
	timestamps := []presentation.KeyValue{
		{Key: "First observed", Value: exactEvidenceTime(evidence.FirstObservedAt, renderer)},
		{Key: "Last observed", Value: exactEvidenceTime(evidence.LastObservedAt, renderer)},
		{Key: "First effective", Value: exactEvidenceTime(evidence.FirstEffectiveActivityAt, renderer)},
		{Key: "Last effective", Value: exactEvidenceTime(evidence.LastEffectiveActivityAt, renderer)},
	}
	fmt.Fprintln(out, indentHumanBlock(reportKeyValues(renderer, timestamps, 6), 6))
	provenance := make([]string, 0, len(evidence.EvidenceSources))
	for _, source := range evidence.EvidenceSources {
		provenance = append(provenance, string(source))
	}
	if len(provenance) == 0 {
		provenance = []string{"unknown"}
	}
	writeReportText(out, renderer, "Provenance      "+strings.Join(provenance, ", "), 4)
}

func reportClassificationBasis(evidence analysis.CapabilityEvidence, staleDays int) string {
	switch evidence.Classification {
	case analysis.STALE:
		if evidence.HasLastUsed {
			return fmt.Sprintf("last observed invocation is older than the %d-day stale threshold", staleDays)
		}
		return "stale classification requires an observed invocation; evidence is incomplete"
	case analysis.REVIEW:
		if evidence.ActivityCount == 0 {
			return "no loaded or invoked activity was observed; missing observations do not prove non-use"
		}
		if evidence.InvocationCount == 0 {
			return "loaded activity was observed but no invocation evidence was recorded"
		}
		if hasLargeKnownFootprint(evidence) && evidence.InvocationCount <= analysis.DefaultReviewUseCount {
			return "known footprint is large relative to observed invocation use"
		}
		return "evidence is retained for review because this classification is conservative"
	case analysis.KEEP:
		if evidence.LastUsedInFuture {
			return "observed invocation has a future timestamp; age is conservatively clamped to zero"
		}
		return fmt.Sprintf("observed invocation is within the %d-day stale threshold", staleDays)
	default:
		return "classification is unknown; no stronger conclusion is supported"
	}
}

func reportInterpretation(evidence analysis.CapabilityEvidence) string {
	switch evidence.Classification {
	case analysis.STALE:
		return "STALE describes recency in this store only; it does not prove lifetime non-use."
	case analysis.REVIEW:
		if evidence.ActivityCount == 0 {
			return "No activity was observed; this is not evidence that the capability was never used."
		}
		if evidence.InvocationCount == 0 {
			return "Loaded evidence is not invocation evidence, so no use is inferred."
		}
		return "REVIEW is a conservative prompt for inspection, not proof of waste or non-use."
	case analysis.KEEP:
		return "KEEP reflects observed recency only; it does not imply complete capture or universal use."
	default:
		return "The available evidence does not support a stronger interpretation."
	}
}

func hasLargeKnownFootprint(evidence analysis.CapabilityEvidence) bool {
	remaining := analysis.DefaultReviewFootprintTokens
	for _, measurement := range []domain.Measurement{evidence.MetadataTokens, evidence.BodyTokens} {
		if measurement.Confidence == domain.ConfidenceUnknown {
			continue
		}
		if measurement.Value >= remaining {
			return true
		}
		remaining -= measurement.Value
	}
	return false
}

func advertisementLabel(value domain.AdvertisementState) string {
	switch value {
	case domain.AdvertisementStateFullyAdvertised:
		return "fully advertised"
	case domain.AdvertisementStateNameOnly:
		return "name only"
	case domain.AdvertisementStateNotAdvertised:
		return "not advertised"
	default:
		return "unknown (no exposure inferred)"
	}
}

func humanMeasurement(value domain.Measurement, renderer presentation.HumanRenderer) string {
	if value.Confidence == domain.ConfidenceUnknown {
		return "Unknown"
	}
	return renderer.Tokens(value.Value) + " tokens"
}

func exactEvidenceTime(value *time.Time, renderer presentation.HumanRenderer) string {
	if value == nil || value.IsZero() {
		return "not observed"
	}
	return renderer.Timestamp(*value)
}

func modeledCoverageLabel(value *history.EffectiveCoverage, now time.Time, renderer presentation.HumanRenderer) string {
	duration, ok := modeledCoverageDuration(value, now)
	if !ok {
		return "unknown (no confirmed capture/presence intersection)"
	}
	return fmt.Sprintf("%s modeled intersection; not complete coverage", renderer.Duration(duration))
}

func modeledCoverageDuration(value *history.EffectiveCoverage, now time.Time) (time.Duration, bool) {
	if value == nil || value.Status == history.CoverageUnknown || len(value.Intervals) == 0 {
		return 0, false
	}
	var duration time.Duration
	for _, interval := range value.Intervals {
		end := interval.End
		if end.IsZero() || end.After(now) {
			end = now
		}
		if end.After(interval.Start) {
			duration += end.Sub(interval.Start)
		}
	}
	if duration <= 0 {
		return 0, false
	}
	return duration, true
}

// runExplain intentionally shares loadReport with report/stale. It does not
// read or reconstruct a report JSON DTO: human explanation is a separate
// presentation of the already-analysed evidence.
func runExplain(ctx context.Context, config commandConfig, flags parsedFlags, out io.Writer) error {
	result, aggregates, err := loadReport(ctx, config, config.days)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(flags.explainName)
	runtimeFilter := domain.Runtime("")
	if flags.runtimeSet {
		runtimeFilter, err = usageRuntime(flags.runtime)
		if err != nil {
			return err
		}
	}
	var typeFilter domain.CapabilityType
	if flags.typeSet {
		typeFilter, err = explainType(flags.capabilityType)
		if err != nil {
			return err
		}
	}
	var scopeFilter domain.Scope
	if flags.scopeSet {
		scopeFilter, err = explainScope(flags.scope)
		if err != nil {
			return err
		}
	}

	candidates := make([]reportCapability, 0)
	for _, item := range reportCapabilities(result) {
		capability := item.evidence.Capability
		if capability.Name != name {
			continue
		}
		if runtimeFilter != "" && capability.Runtime != runtimeFilter {
			continue
		}
		if flags.typeSet && !explainTypeMatches(typeFilter, flags.capabilityType, capability.Type) {
			continue
		}
		if scopeFilter != "" && capability.Scope != scopeFilter {
			continue
		}
		candidates = append(candidates, item)
	}
	if len(candidates) == 0 {
		return unknownExplainError(config.renderer, name, result, aggregates, flags)
	}
	if len(candidates) != 1 {
		return ambiguousExplainError(config.renderer, name, candidates)
	}
	renderExplainView(out, config.renderer, candidates[0], config.days, config.now, config.verbose)
	return nil
}

func explainTypeMatches(filter domain.CapabilityType, raw string, actual domain.CapabilityType) bool {
	if strings.EqualFold(strings.TrimSpace(raw), "mcp") {
		return actual == domain.CapabilityMCPServer || actual == domain.CapabilityMCPTool
	}
	return filter == actual
}

func hasName(result analysis.Report, name string) bool {
	for _, item := range result.Capabilities {
		if item.Capability.Name == name {
			return true
		}
	}
	return false
}

func renderExplainView(out io.Writer, renderer presentation.HumanRenderer, item reportCapability, staleDays int, now time.Time, verbose bool) {
	evidence := item.evidence
	writeHumanText(out, renderer, cleanText(evidence.Capability.Name), 0)
	writeReportText(out, renderer, fmt.Sprintf("%s · %s · %s · %s", renderer.Runtime(string(evidence.Capability.Runtime)), renderer.Type(string(evidence.Capability.Type)), scopeLabel(item.scope), reportStatus(renderer, evidence.Classification)), 0)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage")
	usage := []presentation.KeyValue{
		{Key: "Invocations", Value: renderer.Integer(int64(evidence.InvocationCount))},
		{Key: "Sessions", Value: renderer.Integer(int64(evidence.DistinctSessionCount))},
		{Key: "First observed", Value: explainUsedTime(renderer, evidence.HasFirstUsed, evidence.FirstUsedAt)},
		{Key: "Last observed", Value: explainUsedTime(renderer, evidence.HasLastUsed, evidence.LastUsedAt)},
	}
	fmt.Fprintln(out, indentHumanBlock(reportKeyValues(renderer, usage, 2), 2))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Exposure")
	exposure := []presentation.KeyValue{
		{Key: "Metadata", Value: humanMeasurement(evidence.MetadataTokens, renderer)},
		{Key: "Body", Value: bodyMeasurement(evidence.BodyTokens, renderer)},
		{Key: "Advertisement", Value: advertisementLabel(evidence.Capability.Advertisement)},
	}
	fmt.Fprintln(out, indentHumanBlock(reportKeyValues(renderer, exposure, 2), 2))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Coverage")
	coverage := []presentation.KeyValue{
		{Key: "Direct coverage", Value: directCoverageValue(evidence.EffectiveCoverage, now, renderer)},
		{Key: "Status", Value: coverageStatusValue(evidence.EffectiveCoverage)},
		{Key: "Confidence", Value: presentation.StatusWord(string(evidence.CoverageConfidence))},
	}
	fmt.Fprintln(out, indentHumanBlock(reportKeyValues(renderer, coverage, 2), 2))
	fmt.Fprintln(out)
	classification := string(evidence.Classification)
	if classification == "" {
		classification = "UNKNOWN"
	}
	fmt.Fprintf(out, "Why %s?\n", classification)
	writeReportText(out, renderer, sentence(reportClassificationBasis(evidence, staleDays)), 2)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Interpretation")
	writeReportText(out, renderer, reportInterpretation(evidence), 2)
	if verbose {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Details")
		details := []presentation.KeyValue{
			{Key: "Enabled", Value: string(evidence.Capability.Enabled)},
			{Key: "Advertised events", Value: renderer.Integer(int64(evidence.EventCount(domain.EventAdvertised)))},
			{Key: "Loaded events", Value: renderer.Integer(int64(evidence.EventCount(domain.EventLoaded)))},
			{Key: "Invoked events", Value: renderer.Integer(int64(evidence.EventCount(domain.EventInvoked)))},
			{Key: "Decision confidence", Value: presentation.StatusWord(string(evidence.Confidence))},
			{Key: "Coverage basis", Value: cleanText(evidence.EvidenceCoverage)},
			{Key: "First observed exact", Value: exactEvidenceTime(evidence.FirstObservedAt, renderer)},
			{Key: "Last observed exact", Value: exactEvidenceTime(evidence.LastObservedAt, renderer)},
			{Key: "First effective exact", Value: exactEvidenceTime(evidence.FirstEffectiveActivityAt, renderer)},
			{Key: "Last effective exact", Value: exactEvidenceTime(evidence.LastEffectiveActivityAt, renderer)},
			{Key: "Provenance", Value: evidenceProvenance(evidence)},
		}
		fmt.Fprintln(out, indentHumanBlock(reportKeyValues(renderer, details, 2), 2))
	}
}

func explainUsedTime(renderer presentation.HumanRenderer, observed bool, value time.Time) string {
	if !observed || value.IsZero() {
		return "Never"
	}
	return renderer.RelativeTime(value)
}

func bodyMeasurement(value domain.Measurement, renderer presentation.HumanRenderer) string {
	measurement := humanMeasurement(value, renderer)
	if measurement == "Unknown" {
		return measurement
	}
	return measurement + " when loaded"
}

func directCoverageValue(coverage *history.EffectiveCoverage, now time.Time, renderer presentation.HumanRenderer) string {
	duration, ok := modeledCoverageDuration(coverage, now)
	if !ok {
		return "Unknown"
	}
	return renderer.Duration(duration)
}

func coverageStatusValue(coverage *history.EffectiveCoverage) string {
	if coverage == nil || coverage.Status == "" {
		return "Unknown"
	}
	return presentation.StatusWord(string(coverage.Status))
}

func evidenceProvenance(evidence analysis.CapabilityEvidence) string {
	values := make([]string, 0, len(evidence.EvidenceSources))
	for _, source := range evidence.EvidenceSources {
		values = append(values, string(source))
	}
	if len(values) == 0 {
		return "Unknown"
	}
	return strings.Join(values, ", ")
}

func sentence(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	runes := []rune(value)
	runes[0] = unicode.ToUpper(runes[0])
	value = string(runes)
	switch value[len(value)-1] {
	case '.', '!', '?':
		return value
	default:
		return value + "."
	}
}

func unknownExplainError(renderer presentation.HumanRenderer, name string, result analysis.Report, aggregates []history.Aggregate, flags parsedFlags) error {
	renderer = plainRenderer(renderer)
	var output strings.Builder
	if hasName(result, name) {
		fmt.Fprintf(&output, "No current capability named %q matched the requested filters.\n", cleanText(name))
		available := make([]reportCapability, 0)
		for _, item := range reportCapabilities(result) {
			if item.evidence.Capability.Name == name {
				available = append(available, item)
			}
		}
		fmt.Fprintln(&output)
		fmt.Fprintln(&output, "Available matches:")
		writeExplainCandidates(&output, renderer, available)
	} else {
		fmt.Fprintf(&output, "No capability named %q was found.\n", cleanText(name))
		usageOnly := false
		for _, aggregate := range aggregates {
			if aggregate.CapabilityName == name && !aggregate.Installed {
				usageOnly = true
				break
			}
		}
		if usageOnly {
			fmt.Fprintln(&output)
			fmt.Fprintln(&output, "Observed usage exists for this name, but it is not current inventory.")
		}
	}
	fmt.Fprintln(&output)
	fmt.Fprintln(&output, "Try:")
	fmt.Fprintln(&output, "  harness-lint report --all")
	fmt.Fprintln(&output, "  harness-lint scan")
	if flags.runtimeSet || flags.typeSet || flags.scopeSet {
		fmt.Fprintln(&output)
		fmt.Fprintln(&output, "Filters apply only to current installed definitions.")
	}
	return fmt.Errorf("%s", strings.TrimSpace(output.String()))
}

func ambiguousExplainError(renderer presentation.HumanRenderer, name string, candidates []reportCapability) error {
	renderer = plainRenderer(renderer)
	var output strings.Builder
	fmt.Fprintf(&output, "Capability %q matches multiple current definitions:\n", cleanText(name))
	fmt.Fprintln(&output)
	writeExplainCandidates(&output, renderer, candidates)
	fmt.Fprintln(&output)
	if explainCandidatesNeedSource(candidates) {
		fmt.Fprintln(&output, "These definitions share the same runtime, type, and scope.")
		fmt.Fprintln(&output, "Run `harness-lint doctor` to inspect duplicate definitions.")
	} else {
		fmt.Fprintln(&output, "Refine with:")
		fmt.Fprintln(&output, "  --runtime claude|codex")
		fmt.Fprintln(&output, "  --type TYPE")
		fmt.Fprintln(&output, "  --scope global|user|project|session")
	}
	return fmt.Errorf("%s", strings.TrimSpace(output.String()))
}

func plainRenderer(renderer presentation.HumanRenderer) presentation.HumanRenderer {
	options := renderer.Options()
	options.Color = false
	return presentation.NewHumanRenderer(options)
}

func writeExplainCandidates(out io.Writer, renderer presentation.HumanRenderer, candidates []reportCapability) {
	headers := []string{"Runtime", "Type", "Name", "Scope"}
	includeSource := explainCandidatesNeedSource(candidates)
	if includeSource {
		headers = append(headers, "Source")
	}
	writeReportTable(out, renderer, headers, explainCandidateRows(renderer, candidates, includeSource))
}

func explainCandidatesNeedSource(candidates []reportCapability) bool {
	seen := make(map[string]struct{}, len(candidates))
	for _, item := range candidates {
		capability := item.evidence.Capability
		key := string(capability.Runtime) + "\x00" + string(capability.Type) + "\x00" + capability.Name + "\x00" + string(capability.Scope)
		if _, found := seen[key]; found {
			return true
		}
		seen[key] = struct{}{}
	}
	return false
}

func explainCandidateRows(renderer presentation.HumanRenderer, candidates []reportCapability, includeSource bool) [][]string {
	rows := make([][]string, 0, len(candidates))
	for _, item := range candidates {
		row := []string{
			renderer.Runtime(string(item.evidence.Capability.Runtime)),
			renderer.Type(string(item.evidence.Capability.Type)),
			cleanText(item.evidence.Capability.Name),
			scopeLabel(item.scope),
		}
		if includeSource {
			source := cleanText(item.evidence.Capability.Source)
			if source == "" {
				source = "Unknown"
			} else {
				source = renderer.Path(source)
			}
			row = append(row, source)
		}
		rows = append(rows, row)
	}
	return rows
}
