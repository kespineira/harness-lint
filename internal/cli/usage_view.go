package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
	"github.com/kespineira/harness-lint/internal/presentation"
)

type usageView struct {
	now            time.Time
	days           int
	runtimeFilter  domain.Runtime
	typeFilter     domain.CapabilityType
	mcpUnion       bool
	runtimeSet     bool
	typeSet        bool
	aggregates     []history.Aggregate
	monthly        []history.MonthlyAggregate
	includeMonthly bool
}

type usageHumanRow struct {
	aggregate history.Aggregate
	lastUsed  string
}

type usageRuntimeTotals struct {
	runtime    string
	advertised int64
	loaded     int64
	invoked    int64
	hook       int64
	transcript int64
	imported   int64
}

const defaultUsageRowLimit = 20

// renderUsageView is deliberately separate from the usage JSON DTO builder.
// It consumes the privacy-safe history query results directly so human
// presentation cannot accidentally become coupled to a public wire contract.
func renderUsageView(out io.Writer, renderer presentation.HumanRenderer, view usageView) {
	if view.includeMonthly {
		fmt.Fprintln(out, "Monthly usage")
	} else {
		fmt.Fprintln(out, "Usage")
	}
	fmt.Fprintf(out, "Last %s days\n", renderer.Integer(int64(view.days)))

	filters := make([]string, 0, 2)
	if view.runtimeSet {
		filters = append(filters, renderer.Runtime(string(view.runtimeFilter)))
	}
	if view.typeSet {
		if view.mcpUnion {
			filters = append(filters, "MCP")
		} else {
			filters = append(filters, renderer.Type(string(view.typeFilter)))
		}
	}
	if len(filters) > 0 {
		fmt.Fprintln(out)
		writeReportText(out, renderer, "Filters: "+strings.Join(filters, " · "), 2)
	}
	fmt.Fprintln(out)

	if len(view.aggregates) == 0 {
		fmt.Fprintln(out, "No usage observations were found in this period.")
		return
	}

	allRows := make([]usageHumanRow, 0, len(view.aggregates))
	invocationRows := make([]usageHumanRow, 0, len(view.aggregates))
	for _, aggregate := range view.aggregates {
		row := usageHumanRow{
			aggregate: aggregate,
			lastUsed:  usageLastUsed(renderer, aggregate.LastObservedAt),
		}
		allRows = append(allRows, row)
		if aggregate.Uses > 0 {
			invocationRows = append(invocationRows, row)
		}
	}
	sort.SliceStable(invocationRows, func(left, right int) bool {
		if invocationRows[left].aggregate.Uses != invocationRows[right].aggregate.Uses {
			return invocationRows[left].aggregate.Uses > invocationRows[right].aggregate.Uses
		}
		if invocationRows[left].aggregate.Runtime != invocationRows[right].aggregate.Runtime {
			return invocationRows[left].aggregate.Runtime < invocationRows[right].aggregate.Runtime
		}
		if invocationRows[left].aggregate.CapabilityType != invocationRows[right].aggregate.CapabilityType {
			return invocationRows[left].aggregate.CapabilityType < invocationRows[right].aggregate.CapabilityType
		}
		return invocationRows[left].aggregate.CapabilityName < invocationRows[right].aggregate.CapabilityName
	})

	if view.includeMonthly {
		monthlyRows := usageMonthlyRows(renderer, view.monthly)
		if table := reportTable(renderer, []string{"Month (UTC)", "Uses"}, monthlyRows); table != "" {
			fmt.Fprintln(out, indentHumanBlock(table, 2))
		} else {
			fmt.Fprintln(out, "No monthly invocation observations were returned.")
		}
	} else if len(invocationRows) == 0 {
		fmt.Fprintln(out, "No capability invocations were observed in this period.")
	} else {
		fmt.Fprintln(out, "Capabilities ranked by uses")
		displayed := invocationRows
		if len(displayed) > defaultUsageRowLimit {
			displayed = displayed[:defaultUsageRowLimit]
		}
		tableRows := make([][]string, 0, len(displayed))
		for _, row := range displayed {
			aggregate := row.aggregate
			tableRows = append(tableRows, []string{
				cleanText(aggregate.CapabilityName),
				renderer.Runtime(string(aggregate.Runtime)),
				renderer.Integer(aggregate.Uses),
				renderer.Integer(aggregate.DistinctInvocationSessions),
				row.lastUsed,
				renderer.Type(string(aggregate.CapabilityType)),
			})
		}
		if table := reportTable(renderer, []string{"Capability", "Runtime", "Uses", "Sessions", "Last used", "Type"}, tableRows); table != "" {
			fmt.Fprintln(out, indentHumanBlock(table, 2))
		}
		if len(displayed) < len(invocationRows) {
			fmt.Fprintln(out)
			writeReportText(out, renderer, fmt.Sprintf("Showing %s of %s capabilities with observed invocations.", renderer.Integer(int64(len(displayed))), renderer.Integer(int64(len(invocationRows)))), 2)
			writeReportText(out, renderer, "Refine with --runtime or --type, or use `harness-lint usage --json` for complete data.", 2)
		}
	}

	totals := usageTotals(allRows)
	if !usageTotalsHaveEvidence(totals) {
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Observation totals")
	observationRows := make([][]string, 0, len(totals))
	provenanceRows := make([][]string, 0, len(totals))
	for _, total := range totals {
		observationRows = append(observationRows, []string{
			renderer.Runtime(total.runtime),
			renderer.Integer(total.advertised),
			renderer.Integer(total.loaded),
			renderer.Integer(total.invoked),
		})
		provenanceRows = append(provenanceRows, []string{
			renderer.Runtime(total.runtime),
			renderer.Integer(total.hook),
			renderer.Integer(total.transcript),
			renderer.Integer(total.imported),
		})
	}
	if table := reportTable(renderer, []string{"Runtime", "Advertised", "Loaded", "Invoked"}, observationRows); table != "" {
		fmt.Fprintln(out, indentHumanBlock(table, 2))
	}
	if usageTotalsHaveProvenance(totals) {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Invocation evidence")
		if table := reportTable(renderer, []string{"Runtime", "Hook", "Transcript", "Import"}, provenanceRows); table != "" {
			fmt.Fprintln(out, indentHumanBlock(table, 2))
		}
	}

}

func usageLastUsed(renderer presentation.HumanRenderer, value *time.Time) string {
	if value == nil || value.IsZero() {
		return "Never"
	}
	return renderer.RelativeTime(value.UTC())
}

func usageTotals(rows []usageHumanRow) []usageRuntimeTotals {
	byRuntime := make(map[string]usageRuntimeTotals)
	for _, row := range rows {
		aggregate := row.aggregate
		runtime := string(aggregate.Runtime)
		total := byRuntime[runtime]
		total.runtime = runtime
		total.advertised += aggregate.AdvertisedObservations
		total.loaded += aggregate.LoadedObservations
		total.invoked += aggregate.Uses
		total.hook += aggregate.InvocationEvidenceCount(domain.ProvenanceHook)
		total.transcript += aggregate.InvocationEvidenceCount(domain.ProvenanceTranscript)
		total.imported += aggregate.InvocationEvidenceCount(domain.ProvenanceImport)
		byRuntime[runtime] = total
	}
	result := make([]usageRuntimeTotals, 0, len(byRuntime))
	for _, total := range byRuntime {
		result = append(result, total)
	}
	sort.SliceStable(result, func(left, right int) bool {
		return result[left].runtime < result[right].runtime
	})
	return result
}

func usageTotalsHaveEvidence(values []usageRuntimeTotals) bool {
	for _, value := range values {
		if value.advertised > 0 || value.loaded > 0 || value.invoked > 0 || value.hook > 0 || value.transcript > 0 || value.imported > 0 {
			return true
		}
	}
	return false
}

func usageTotalsHaveProvenance(values []usageRuntimeTotals) bool {
	for _, value := range values {
		if value.hook > 0 || value.transcript > 0 || value.imported > 0 {
			return true
		}
	}
	return false
}

func usageMonthlyRows(renderer presentation.HumanRenderer, monthly []history.MonthlyAggregate) [][]string {
	type monthlyRow struct {
		month string
		uses  int64
	}
	values := make([]monthlyRow, 0)
	byMonth := make(map[string]monthlyRow)
	for _, month := range monthly {
		monthName := month.Month.UTC().Format("2006-01")
		value := byMonth[monthName]
		value.month = monthName
		value.uses += month.Uses
		byMonth[monthName] = value
	}
	for _, value := range byMonth {
		values = append(values, value)
	}
	sort.SliceStable(values, func(left, right int) bool {
		if values[left].month != values[right].month {
			return values[left].month < values[right].month
		}
		return values[left].uses > values[right].uses
	})
	result := make([][]string, 0, len(values))
	for _, value := range values {
		result = append(result, []string{
			value.month,
			renderer.Integer(value.uses),
		})
	}
	return result
}
