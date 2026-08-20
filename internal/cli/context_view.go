package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/kespineira/harness-lint/internal/analysis"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/presentation"
)

// renderContextView is the human presentation boundary for context.  The
// analysis package has already kept metadata and body measurements separate;
// this view only gives those dimensions a runtime/type-aware label.
func renderContextView(out io.Writer, renderer presentation.HumanRenderer, summary analysis.ContextSummary) {
	fmt.Fprintln(out, "Context footprint")
	fmt.Fprintln(out)

	if len(summary.Groups) == 0 {
		fmt.Fprintln(out, "No configured capabilities were discovered.")
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Run `harness-lint scan` to refresh the current inventory.")
		fmt.Fprintln(out)
		renderContextCaveats(out, renderer, 0, false)
		return
	}

	groups := append([]analysis.ContextGroup(nil), summary.Groups...)
	sort.SliceStable(groups, func(left, right int) bool {
		if groups[left].Runtime != groups[right].Runtime {
			return groups[left].Runtime < groups[right].Runtime
		}
		return groups[left].CapabilityType < groups[right].CapabilityType
	})

	currentRuntime := domain.Runtime("")
	unknownMeasurements := 0
	mcpPresent := false
	for _, group := range groups {
		if group.Runtime != currentRuntime {
			if currentRuntime != "" {
				fmt.Fprintln(out)
			}
			currentRuntime = group.Runtime
			fmt.Fprintln(out, renderer.Runtime(string(group.Runtime)))
		}

		if group.CapabilityType == domain.CapabilityMCPServer || group.CapabilityType == domain.CapabilityMCPTool {
			mcpPresent = true
		}
		fmt.Fprintf(out, "  %s (%s)\n", renderer.Type(string(group.CapabilityType)), renderer.Integer(int64(group.CapabilityCount)))

		metadataLabel, bodyLabel := contextDimensionLabels(group.CapabilityType)
		unknownMeasurements += group.MetadataTokens.UnknownCount
		metadata, known := contextMeasurementValue(group.MetadataTokens, renderer)
		if known {
			fmt.Fprintf(out, "    %-20s %s\n", metadataLabel, metadata)
		}
		unknownMeasurements += group.BodyTokens.UnknownCount
		body, known := contextMeasurementValue(group.BodyTokens, renderer)
		if known {
			fmt.Fprintf(out, "    %-20s %s\n", bodyLabel, body)
		}
	}

	fmt.Fprintln(out)
	renderContextCaveats(out, renderer, unknownMeasurements, mcpPresent)
}

func contextDimensionLabels(capabilityType domain.CapabilityType) (metadata, body string) {
	switch capabilityType {
	case domain.CapabilitySkill:
		return "Configured baseline metadata", "On-load body estimate"
	case domain.CapabilityInstructionFile:
		return "Metadata", "Configured baseline body"
	default:
		return "Configured baseline metadata", "On-load body estimate"
	}
}

func contextMeasurementValue(summary analysis.MeasurementSummary, renderer presentation.HumanRenderer) (string, bool) {
	if !summary.IsKnown() {
		return "", false
	}
	value := renderer.Tokens(summary.Value) + " tokens"
	if summary.UnknownCount > 0 {
		return value + " (partial)", true
	}
	confidence := strings.TrimSpace(string(summary.Confidence))
	if confidence != "" && confidence != string(domain.ConfidenceUnknown) {
		return value + " (" + presentation.StatusWord(confidence) + ")", true
	}
	return value, true
}

func renderContextCaveats(out io.Writer, renderer presentation.HumanRenderer, unknownMeasurements int, mcpPresent bool) {
	fmt.Fprintln(out, "Caveats")
	writeReportText(out, renderer, "Token values are estimates of context exposure, not a measurement of model billing or runtime behavior.", 2)
	if unknownMeasurements > 0 {
		verb := "are"
		if unknownMeasurements == 1 {
			verb = "is"
		}
		writeReportText(out, renderer, fmt.Sprintf("%s %s unknown and omitted from the subtotals.", humanCount(renderer, unknownMeasurements, "token measurement", "token measurements"), verb), 2)
	}
	// MCP schema size is not available from the inventory contract. Keep this
	// caveat visible even when no MCP row is present so the view never implies
	// that an absent schema measurement is a zero-cost schema.
	if mcpPresent {
		writeReportText(out, renderer, "MCP schema cost is unknown and is not included in these estimates.", 2)
	} else {
		writeReportText(out, renderer, "MCP schema cost is unknown; no schema exposure is inferred.", 2)
	}
}
