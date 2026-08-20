package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/kespineira/harness-lint/internal/health"
	"github.com/kespineira/harness-lint/internal/hooks"
	"github.com/kespineira/harness-lint/internal/presentation"
)

func renderHookStatusView(out io.Writer, renderer presentation.HumanRenderer, verbose bool, reports []hooks.StatusReport) {
	fmt.Fprintln(out, "Managed hooks")
	fmt.Fprintln(out)
	rows := make([][]string, 0, len(reports))
	for _, report := range reports {
		handlers := 0
		for _, entry := range report.ManagedEntries {
			handlers += entry.ExactHandlers
		}
		rows = append(rows, []string{
			renderer.Runtime(string(report.Runtime)),
			renderer.Status(hookStatusToken(report.Code)),
			humanCount(renderer, handlers, "handler", "handlers"),
		})
	}
	if table := humanRows(renderer, rows, 2); table != "" {
		fmt.Fprintln(out, indentHumanBlock(table, 2))
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Executable")
	renderHookExecutable(out, renderer, reports)

	warnings := hookWarnings(reports)
	if len(warnings) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Warning")
		for _, warning := range warnings {
			writeHumanText(out, renderer, warning, 2)
		}
	}

	if !verbose {
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Details")
	for _, report := range reports {
		fmt.Fprintf(out, "  %s\n", renderer.Runtime(string(report.Runtime)))
		details := make([][]string, 0, len(report.ManagedEntries)+3)
		if report.ConfigPath != "" {
			details = append(details, []string{"Configuration", renderer.Path(report.ConfigPath)})
		}
		for _, entry := range report.ManagedEntries {
			details = append(details, []string{entry.Event, fmt.Sprintf("%s · exact %s · partial %s · lookalikes %s",
				renderer.Status(hookEntryStatusToken(entry.State)), renderer.Integer(int64(entry.ExactHandlers)),
				renderer.Integer(int64(entry.Partial)), renderer.Integer(int64(entry.Lookalikes)))})
		}
		if report.Binary.Resolved && report.Binary.ResolvedPath != "" {
			details = append(details, []string{"Executable", renderer.Path(report.Binary.ResolvedPath)})
		}
		if report.InlineHooks {
			details = append(details, []string{"Ownership", "inline Codex hooks require separate trust review"})
		}
		if table := humanRows(renderer, details, 4); table != "" {
			fmt.Fprintln(out, indentHumanBlock(table, 4))
		}
	}
}

func renderHookExecutable(out io.Writer, renderer presentation.HumanRenderer, reports []hooks.StatusReport) {
	paths := make(map[string]struct{})
	allResolved := len(reports) > 0
	for _, report := range reports {
		if !report.Binary.Resolved || strings.TrimSpace(report.Binary.ResolvedPath) == "" {
			allResolved = false
			continue
		}
		paths[report.Binary.ResolvedPath] = struct{}{}
	}
	if allResolved && len(paths) == 1 {
		for path := range paths {
			writeHumanText(out, renderer, renderer.Path(path), 2)
		}
		return
	}
	rows := make([][]string, 0, len(reports))
	for _, report := range reports {
		value := renderer.Status("unavailable")
		if report.Binary.Resolved && report.Binary.ResolvedPath != "" {
			value = renderer.Path(report.Binary.ResolvedPath)
		}
		rows = append(rows, []string{renderer.Runtime(string(report.Runtime)), value})
	}
	if table := humanRows(renderer, rows, 2); table != "" {
		fmt.Fprintln(out, indentHumanBlock(table, 2))
	}
}

func hookWarnings(reports []hooks.StatusReport) []string {
	var warnings []string
	for _, report := range reports {
		runtimeLabel := presentation.RuntimeLabel(string(report.Runtime))
		for _, raw := range report.Warnings {
			warning := boundedHookWarning(raw)
			if warning != "" {
				warnings = append(warnings, runtimeLabel+": "+warning)
			}
		}
		if report.InlineHooks && len(report.Warnings) == 0 {
			warnings = append(warnings, runtimeLabel+": additional user-managed inline hooks require separate review")
		}
	}
	return warnings
}

func humanCount(renderer presentation.HumanRenderer, count int, singular, plural string) string {
	word := plural
	if count == 1 {
		word = singular
	}
	return renderer.Integer(int64(count)) + " " + word
}

func hookStatusToken(status hooks.StatusCode) string {
	switch status {
	case hooks.StatusInstalled:
		return "installed"
	case hooks.StatusPartiallyInstalled:
		return "partially_installed"
	case hooks.StatusMalformed:
		return "broken"
	case hooks.StatusConfigurationNotFound, hooks.StatusNotInstalled:
		return "not_installed"
	case hooks.StatusUnsupported:
		return "unknown"
	default:
		return "unknown"
	}
}

func hookEntryStatusToken(status hooks.ManagedEntryState) string {
	switch status {
	case hooks.ManagedEntryInstalled:
		return "installed"
	case hooks.ManagedEntryPartial:
		return "partial"
	case hooks.ManagedEntryStale:
		return "stale"
	case hooks.ManagedEntryNotInstalled:
		return "not_installed"
	default:
		return "unknown"
	}
}

func renderHookTestView(out io.Writer, renderer presentation.HumanRenderer, verbose bool, reports []health.Report, compatibilityResults []compatibilityDiagnostic, summary hookTestSummary) {
	fmt.Fprintln(out, "Hook health")
	fmt.Fprintln(out)
	rows := make([][]string, 0, len(reports))
	for _, report := range reports {
		rows = append(rows, []string{
			renderer.Runtime(string(report.Runtime)),
			renderer.Status(string(report.State)),
			healthReason(renderer, report),
		})
	}
	if table := humanRows(renderer, rows, 2); table != "" {
		fmt.Fprintln(out, indentHumanBlock(table, 2))
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, hookHealthSummary(renderer, len(reports), summary))

	if !verbose {
		return
	}
	diagnostics := make(map[string]compatibilityDiagnostic, len(compatibilityResults))
	for _, diagnostic := range compatibilityResults {
		diagnostics[string(diagnostic.detection.Runtime)] = diagnostic
	}
	for _, report := range reports {
		fmt.Fprintln(out)
		renderHealthDetails(out, renderer, report, diagnostics[string(report.Runtime)])
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Limitations")
	fmt.Fprintln(out, "  Synthetic self-test verifies local ingest and SQLite; live delivery")
	fmt.Fprintln(out, "  requires observed runtime activity.")
	fmt.Fprintln(out, "  Runtime-version compatibility ranges are not yet encoded.")
}

func healthReason(renderer presentation.HumanRenderer, report health.Report) string {
	switch report.State {
	case health.Healthy:
		if report.Delivery.LastSuccessfulDelivery != nil {
			return "last event " + renderer.RelativeTime(*report.Delivery.LastSuccessfulDelivery)
		}
		return "live delivery observed"
	case health.Idle:
		return "no direct activity yet"
	case health.Degraded:
		if report.Components.Delivery.State == health.ComponentFailed {
			return humanCount(renderer, report.Delivery.ConsecutiveFailures, "recent delivery failure", "recent delivery failures")
		}
		if report.Components.SelfTest.State == health.ComponentFailed {
			return "local self-test failed"
		}
		return "review component warnings"
	case health.Broken:
		if report.Components.Executable.State == health.ComponentUnresolved {
			return "harness-lint executable not found"
		}
		if report.Components.Config.State == health.ComponentMissing || report.Components.Hooks.State == health.ComponentMissing {
			return "hook configuration is not installed"
		}
		if report.Components.Config.State == health.ComponentMalformed || report.Components.Hooks.State == health.ComponentMalformed {
			return "hook configuration is malformed"
		}
		if report.Components.Database.State == health.ComponentUnavailable {
			return "database unavailable"
		}
		return "one or more checks failed"
	default:
		return "health could not be determined"
	}
}

func hookHealthSummary(renderer presentation.HumanRenderer, runtimes int, summary hookTestSummary) string {
	runtimeWord := "runtimes"
	if runtimes == 1 {
		runtimeWord = "runtime"
	}
	if summary.Healthy == runtimes {
		return fmt.Sprintf("%s/%s %s healthy", renderer.Integer(int64(summary.Healthy)), renderer.Integer(int64(runtimes)), runtimeWord)
	}
	parts := []string{fmt.Sprintf("%s/%s %s healthy", renderer.Integer(int64(summary.Healthy)), renderer.Integer(int64(runtimes)), runtimeWord)}
	for _, item := range []struct {
		count int
		word  string
	}{{summary.Idle, "idle"}, {summary.Degraded, "degraded"}, {summary.Broken, "broken"}, {summary.Unknown, "unknown"}} {
		if item.count > 0 {
			parts = append(parts, renderer.Integer(int64(item.count))+" "+item.word)
		}
	}
	return strings.Join(parts, " · ")
}

func renderHealthDetails(out io.Writer, renderer presentation.HumanRenderer, report health.Report, diagnostic compatibilityDiagnostic) {
	fmt.Fprintln(out, renderer.Runtime(string(report.Runtime)))
	rows := [][]string{
		{"State", renderer.Status(string(report.State))},
		{"Configuration", renderer.Status(componentStatusToken(report.Components.Config.State))},
		{"Managed hooks", renderer.Status(componentStatusToken(report.Components.ManagedEntries.State))},
		{"Executable", renderer.Status(componentStatusToken(report.Components.Executable.State))},
		{"Database", renderer.Status(componentStatusToken(report.Components.Database.State))},
		{"Schema", renderer.Status(componentStatusToken(report.Components.Schema.State))},
		{"Self-test", renderer.Status(componentStatusToken(report.Components.SelfTest.State))},
		{"Live delivery", renderer.Status(componentStatusToken(report.Components.Delivery.State))},
	}
	fmt.Fprintln(out, indentHumanBlock(humanRows(renderer, rows, 2), 2))

	details := make([][]string, 0, 6)
	if report.Delivery.LastSuccessfulDelivery != nil {
		details = append(details,
			[]string{"Last delivery", renderer.RelativeTime(*report.Delivery.LastSuccessfulDelivery)},
			[]string{"", renderer.Timestamp(*report.Delivery.LastSuccessfulDelivery)})
	} else {
		details = append(details, []string{"Last delivery", "Never"})
	}
	if report.Delivery.LastFailedDelivery != nil {
		details = append(details,
			[]string{"Last failure", renderer.RelativeTime(*report.Delivery.LastFailedDelivery)},
			[]string{"", renderer.Timestamp(*report.Delivery.LastFailedDelivery)})
	}
	details = append(details, []string{"Failed deliveries", renderer.Integer(int64(report.Delivery.ConsecutiveFailures))})
	if report.Delivery.LastFailureKind != nil {
		details = append(details, []string{"Failure kind", string(*report.Delivery.LastFailureKind)})
	}
	details = append(details, []string{"Runtime version", compatibilityVersion(diagnostic)})
	validation := "Synthetic fixtures"
	if report.Delivery.LastSuccessfulDelivery != nil {
		validation += " + observed live delivery"
	}
	details = append(details, []string{"Validation", validation})
	fmt.Fprintln(out)
	fmt.Fprintln(out, indentHumanBlock(humanRows(renderer, details, 2), 2))
}

func compatibilityVersion(diagnostic compatibilityDiagnostic) string {
	if diagnostic.detection.Version != nil {
		return diagnostic.detection.Version.String()
	}
	switch diagnostic.detection.Status {
	case "missing-executable":
		return "Not available (runtime executable not found)"
	case "unparsable-output":
		return "Unknown (version output not recognized)"
	case "command-failed":
		return "Unknown (version command failed)"
	case "timeout":
		return "Unknown (version command timed out)"
	default:
		return "Unknown"
	}
}

func componentStatusToken(state health.ComponentState) string {
	switch state {
	case health.ComponentOK:
		return "healthy"
	case health.ComponentNone:
		return "idle"
	case health.ComponentMissing:
		return "not_installed"
	case health.ComponentMalformed, health.ComponentInvalid, health.ComponentFailed:
		return "broken"
	case health.ComponentIncomplete, health.ComponentMismatch:
		return "degraded"
	case health.ComponentUnresolved, health.ComponentUnavailable, health.ComponentUnsupported:
		return "unavailable"
	default:
		return "unknown"
	}
}

func renderHookOperationView(out io.Writer, renderer presentation.HumanRenderer, verbose bool, result hooks.OperationResult, dryRun bool) {
	title := "Install hooks"
	if result.Action == hooks.ActionUninstall {
		title = "Uninstall hooks"
	}
	if dryRun {
		title += " (dry run)"
	}
	fmt.Fprintln(out, title)
	fmt.Fprintln(out)
	resultText := "No changes needed"
	if dryRun && result.WouldChange {
		resultText = "Changes would be made"
	} else if result.Changed {
		resultText = "Configuration updated"
	} else if summary := boundedHookText(result.Summary); summary != "" {
		resultText = summary
	}
	rows := [][]string{
		{"Runtime", renderer.Runtime(string(result.Runtime))},
		{"Status", renderer.Status(hookStatusToken(result.Status.Code))},
		{"Result", resultText},
	}
	if result.BackupPath != "" {
		rows = append(rows, []string{"Backup", renderer.Path(result.BackupPath)})
	}
	fmt.Fprintln(out, indentHumanBlock(humanRows(renderer, rows, 2), 2))
	if len(result.Plan) == 0 {
		return
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Changes:")
	for _, change := range result.Plan {
		detail := boundedHookText(change.Detail)
		line := boundedHookText(change.Kind) + ": " + detail
		if change.Path != "" {
			line += " (" + renderer.Path(change.Path) + ")"
		}
		writeHumanText(out, renderer, line, 2)
	}
	if verbose {
		fmt.Fprintf(out, "\n%s\n", humanCount(renderer, len(result.Plan), "planned change", "planned changes"))
	}
}

func boundedHookWarning(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	switch {
	case strings.Contains(value, "inline hooks"):
		return "inline Codex hooks require separate trust review"
	case strings.Contains(value, "malformed"), strings.Contains(value, "must be"):
		return "configuration is malformed"
	case strings.Contains(value, "symlink"), strings.Contains(value, "unsafe"):
		return "configuration path is unsafe"
	case strings.Contains(value, "root is empty"):
		return "configuration root is unavailable"
	default:
		return boundedHookText(value)
	}
}

func boundedHookText(value string) string {
	value = cleanText(value)
	const max = 180
	if len(value) > max {
		return value[:max] + "…"
	}
	return value
}
