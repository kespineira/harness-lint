package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/presentation"
	"github.com/kespineira/harness-lint/internal/runtime"
	"github.com/kespineira/harness-lint/internal/store"
)

type scanRuntimeView struct {
	Runtime      domain.Runtime
	Capabilities int
	Events       int
	Findings     int
	Inventory    string
}

type scanView struct {
	Runtimes     []scanRuntimeView
	Capabilities int
	Events       int
	Findings     int
}

func collectScanView(ctx context.Context, config commandConfig, db *store.Store, adapters []runtime.Adapter) (scanView, []string) {
	view := scanView{Runtimes: make([]scanRuntimeView, 0, len(adapters))}
	var failures []string
	for _, adapter := range adapters {
		runtimeName := adapter.Runtime()
		discovery, discoverErr := adapter.Discover(ctx)
		inventoryRecorded := false
		if discoverErr == nil {
			if recordErr := db.RecordInventory(ctx, runtimeName, config.now, discovery.Capabilities); recordErr != nil {
				discoverErr = fmt.Errorf("record inventory: %w", recordErr)
			} else {
				inventoryRecorded = true
			}
		}

		events, usageErr := adapter.ImportUsage(ctx, config.since)
		if usageErr == nil && len(events) > 0 {
			usageErr = db.InsertUsageEvents(ctx, events)
		}
		if discoverErr != nil {
			failures = append(failures, fmt.Sprintf("runtime %s discovery: %v", runtimeName, discoverErr))
		}
		if usageErr != nil {
			failures = append(failures, fmt.Sprintf("runtime %s usage import: %v", runtimeName, usageErr))
		}
		row := scanRuntimeView{
			Runtime:      runtimeName,
			Capabilities: len(discovery.Capabilities),
			Events:       len(events),
			Findings:     len(discovery.Findings),
			Inventory:    "not recorded",
		}
		if inventoryRecorded {
			row.Inventory = "recorded"
		}
		view.Runtimes = append(view.Runtimes, row)
		view.Capabilities += row.Capabilities
		view.Events += row.Events
		view.Findings += row.Findings
	}
	return view, failures
}

func renderScanView(out io.Writer, renderer presentation.HumanRenderer, verbose bool, view scanView) {
	fmt.Fprintln(out, "Scan complete")
	fmt.Fprintln(out)
	headers := []string{"Runtime", "Capabilities", "Events", "Findings"}
	rows := make([][]string, 0, len(view.Runtimes))
	for _, row := range view.Runtimes {
		rows = append(rows, []string{
			renderer.Runtime(string(row.Runtime)),
			renderer.Integer(int64(row.Capabilities)),
			renderer.Integer(int64(row.Events)),
			renderer.Integer(int64(row.Findings)),
		})
	}
	if table := humanTable(renderer, headers, rows, 2); table != "" {
		fmt.Fprintln(out, indentHumanBlock(table, 2))
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "%s · %s\n",
		humanCount(renderer, view.Capabilities, "capability discovered", "capabilities discovered"),
		humanCount(renderer, view.Events, "observation imported", "observations imported"))
	if verbose && len(view.Runtimes) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Inventory")
		for _, row := range view.Runtimes {
			fmt.Fprintf(out, "  %-12s %s\n", renderer.Runtime(string(row.Runtime)), row.Inventory)
		}
	}
	if view.Findings > 0 {
		fmt.Fprintln(out)
		if view.Findings == 1 {
			fmt.Fprintln(out, "1 finding needs attention. Run `harness-lint doctor`.")
		} else {
			fmt.Fprintf(out, "%s findings need attention. Run `harness-lint doctor`.\n", renderer.Integer(int64(view.Findings)))
		}
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Run `harness-lint report` to review usage and attention items.")
}
