package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/kespineira/harness-lint/internal/analysis"
	"github.com/kespineira/harness-lint/internal/presentation"
)

// renderStaleView intentionally receives the analysis result rather than a
// JSON document.  The classification is authoritative in analysis; this
// renderer only selects STALE rows and never reclassifies REVIEW or KEEP.
func renderStaleView(out io.Writer, renderer presentation.HumanRenderer, verbose bool, result analysis.Report, now time.Time, staleDays int) {
	items := reportCapabilities(result)
	stale := make([]reportCapability, 0)
	reviewCount := 0
	for _, item := range items {
		switch item.evidence.Classification {
		case analysis.STALE:
			stale = append(stale, item)
		case analysis.REVIEW:
			reviewCount++
		}
	}

	fmt.Fprintln(out, "Stale capabilities")
	fmt.Fprintf(out, "Threshold: %s days\n", renderer.Integer(int64(staleDays)))
	fmt.Fprintln(out)
	if len(stale) == 0 {
		if len(items) == 0 {
			fmt.Fprintln(out, "No capabilities are currently classified STALE.")
			fmt.Fprintln(out)
			fmt.Fprintln(out, "No current capabilities were discovered.")
			fmt.Fprintln(out, "Run `harness-lint scan` to refresh the inventory.")
		} else {
			fmt.Fprintln(out, "No capabilities are currently classified STALE.")
			fmt.Fprintln(out)
			if reviewCount == 0 {
				fmt.Fprintln(out, "No capabilities remain REVIEW.")
			} else {
				verb := "remain"
				if reviewCount == 1 {
					verb = "remains"
				}
				writeReportText(out, renderer, fmt.Sprintf("%s %s REVIEW; REVIEW is not evidence of staleness.", humanCount(renderer, reviewCount, "capability", "capabilities"), verb), 0)
			}
		}
		return
	}

	rows := reportRows(renderer, stale, false)
	if table := reportTable(renderer, []string{"Status", "Runtime", "Type", "Name", "Scope", "Last used"}, rows); table != "" {
		fmt.Fprintln(out, indentHumanBlock(table, 2))
	}
	if !verbose {
		return
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Exact evidence")
	for index, item := range stale {
		if index > 0 {
			fmt.Fprintln(out)
		}
		renderCapabilityDetails(out, renderer, item, staleDays, now)
	}
}
