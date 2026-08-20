package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/capture"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/health"
	"github.com/kespineira/harness-lint/internal/history"
	"github.com/kespineira/harness-lint/internal/hooks"
)

var hookRuntimes = []hooks.Runtime{hooks.RuntimeClaude, hooks.RuntimeCodex}

func runHooks(ctx context.Context, config commandConfig, flags parsedFlags, out io.Writer) error {
	runtimes, err := selectedHookRuntimes(flags)
	if err != nil {
		return err
	}
	if flags.hooksAction == "status" {
		return runHookStatus(ctx, config, runtimes, flags.json, out)
	}
	if flags.hooksAction == "test" {
		return runHookTest(ctx, config, runtimes, out)
	}
	return runHookOperation(ctx, config, runtimes, flags.hooksAction, flags.dryRun, out)
}

func selectedHookRuntimes(flags parsedFlags) ([]hooks.Runtime, error) {
	value := strings.TrimSpace(flags.hooksRuntime)
	if value == "" && flags.runtimeSet {
		value = strings.TrimSpace(flags.runtime)
	}
	if value == "" {
		return append([]hooks.Runtime(nil), hookRuntimes...), nil
	}
	runtime, err := hookRuntime(value)
	if err != nil {
		return nil, err
	}
	return []hooks.Runtime{runtime}, nil
}

func hookRuntime(value string) (hooks.Runtime, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "claude", "claude-code":
		return hooks.RuntimeClaude, nil
	case "codex":
		return hooks.RuntimeCodex, nil
	default:
		return "", errors.New("unknown runtime; want claude or codex")
	}
}

func hookManager(config commandConfig, runtime hooks.Runtime) hooks.Manager {
	root := config.codexHome
	if runtime == hooks.RuntimeClaude {
		root = config.claudeConfig
	}
	return hooks.New(runtime, hooks.Options{ConfigRoot: root, LookPath: config.lookPath})
}

func runHookStatus(ctx context.Context, config commandConfig, runtimes []hooks.Runtime, asJSON bool, out io.Writer) error {
	reports := make([]hooks.StatusReport, 0, len(runtimes))
	var failures []string
	for _, runtime := range runtimes {
		report, err := hookManager(config, runtime).Status(ctx)
		reports = append(reports, report)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s status: %s", runtime, cleanText(err.Error())))
		}
	}
	if asJSON {
		if err := writeHookStatusJSON(out, config.now, reports); err != nil {
			return err
		}
	} else {
		renderHookStatusView(out, config.renderer, config.verbose, reports)
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func runHookOperation(ctx context.Context, config commandConfig, runtimes []hooks.Runtime, action string, dryRun bool, out io.Writer) error {
	var failures []string
	for _, runtime := range runtimes {
		manager := hookManager(config, runtime)
		var (
			result hooks.OperationResult
			err    error
		)
		if dryRun {
			result, err = manager.DryRun(ctx, hookAction(action))
		} else if action == "install" {
			result, err = manager.Install(ctx)
		} else {
			result, err = manager.Uninstall(ctx)
		}
		renderHookOperationView(out, config.renderer, config.verbose, result, dryRun)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s %s: %s", runtime, action, cleanText(err.Error())))
			continue
		}
		// A lifecycle close is evidence about the runtime only after the
		// managed configuration mutation itself committed. Dry-runs, no-ops,
		// installs, and failed config mutations must not touch capture epochs.
		if action == "uninstall" && !dryRun && result.Changed {
			if lifecycleErr := closeManagedHookCaptureEpoch(ctx, config, runtime); lifecycleErr != nil {
				fmt.Fprintln(out, "")
				fmt.Fprintln(out, "Warning")
				writeHumanText(out, config.renderer, "Capture lifecycle close failed after the configuration changed: "+cleanText(lifecycleErr.Error()), 2)
				failures = append(failures, fmt.Sprintf("%s uninstall capture lifecycle: %s", runtime, cleanText(lifecycleErr.Error())))
			}
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

// closeManagedHookCaptureEpoch deliberately runs after the filesystem
// mutation. If the database operation fails, the already-successful runtime
// uninstall is never rolled back or retried; the caller reports the lifecycle
// evidence gap explicitly instead.
func closeManagedHookCaptureEpoch(ctx context.Context, config commandConfig, runtime hooks.Runtime) error {
	if config.dbPath == "" {
		return errors.New("capture lifecycle database path is unavailable")
	}
	if _, err := os.Stat(config.dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat capture lifecycle database: %w", err)
	}
	db, err := openStore(config)
	if err != nil {
		return fmt.Errorf("open capture lifecycle database: %w", err)
	}
	lifecycleErr := db.CloseCaptureEpoch(ctx, hookDomainRuntime(runtime), config.now.UTC(), history.CaptureEndReasonManagedHookUninstall)
	dbCloseErr := db.Close()
	if lifecycleErr != nil {
		return lifecycleErr
	}
	if dbCloseErr != nil {
		return fmt.Errorf("close capture lifecycle database: %w", dbCloseErr)
	}
	return nil
}

func hookAction(action string) hooks.Action {
	if action == "uninstall" {
		return hooks.ActionUninstall
	}
	return hooks.ActionInstall
}

const hookTestFailureMessage = "hooks test failed: one or more selected runtimes are not healthy or idle"

type hookTestSummary struct {
	Healthy  int
	Idle     int
	Degraded int
	Broken   int
	Unknown  int
}

func runHookTest(ctx context.Context, config commandConfig, runtimes []hooks.Runtime, out io.Writer) error {
	db, openErr := openExistingStore(config)
	if db != nil {
		defer db.Close()
	}
	summary := hookTestSummary{}
	reports := make([]health.Report, 0, len(runtimes))
	compatibilityResults := make([]compatibilityDiagnostic, 0, len(runtimes))
	for _, runtimeName := range runtimes {
		runtime := hookDomainRuntime(runtimeName)
		result, err := health.Evaluate(ctx, health.Inputs{
			Runtime:      runtime,
			Hooks:        hookManager(config, runtimeName),
			Store:        db,
			StoreOpenErr: openErr,
		})
		if err != nil {
			// Evaluate only returns context cancellation as an error. Preserve
			// that cancellation instead of turning it into an unrelated unknown
			// health result.
			return err
		}
		reports = append(reports, result)
		compatibilityResults = append(compatibilityResults, detectCompatibility(ctx, config, runtime))
		switch result.State {
		case health.Healthy:
			summary.Healthy++
		case health.Idle:
			summary.Idle++
		case health.Degraded:
			summary.Degraded++
		case health.Broken:
			summary.Broken++
		default:
			summary.Unknown++
		}
	}
	renderHookTestView(out, config.renderer, config.verbose, reports, compatibilityResults, summary)
	if errors.Is(openErr, errDatabaseNotInitialized) {
		return errors.New("hooks test needs an initialized database; run `harness-lint scan` first")
	}
	if summary.Broken > 0 || summary.Degraded > 0 || summary.Unknown > 0 {
		return errors.New(hookTestFailureMessage)
	}
	return nil
}

func hookDomainRuntime(runtime hooks.Runtime) domain.Runtime {
	if runtime == hooks.RuntimeClaude {
		return domain.RuntimeClaudeCode
	}
	if runtime == hooks.RuntimeCodex {
		return domain.RuntimeCodex
	}
	return domain.RuntimeUnknown
}

func printHookTestReport(out io.Writer, report health.Report) {
	components := report.Components
	fmt.Fprintf(out, "runtime=%s state=%s config=%s hooks=%s managed-entries=%s executable=%s database=%s schema=%s selftest=%s delivery=%s\n",
		report.Runtime,
		report.State,
		components.Config.State,
		components.Hooks.State,
		components.ManagedEntries.State,
		components.Executable.State,
		components.Database.State,
		components.Schema.State,
		components.SelfTest.State,
		components.Delivery.State)
	printHookTestDelivery(out, report.Delivery)
}

func printHookTestDelivery(out io.Writer, delivery capture.DeliveryHealth) {
	lastSuccessful := "unknown"
	if delivery.LastSuccessfulDelivery != nil {
		lastSuccessful = delivery.LastSuccessfulDelivery.UTC().Format(time.RFC3339Nano)
	}
	lastFailed := "unknown"
	if delivery.LastFailedDelivery != nil {
		lastFailed = delivery.LastFailedDelivery.UTC().Format(time.RFC3339Nano)
	}
	failureKind := "unknown"
	if delivery.LastFailureKind != nil {
		failureKind = string(*delivery.LastFailureKind)
	}
	count := delivery.ConsecutiveFailures
	if count < 0 || count > capture.MaxConsecutiveFailures {
		count = 0
	}
	fmt.Fprintf(out, "  delivery last-successful=%s last-failed=%s failure-count=%d failure-kind=%s\n", lastSuccessful, lastFailed, count, failureKind)
}

func printHookStatus(out io.Writer, report hooks.StatusReport) {
	binary := "unresolved"
	if report.Binary.Resolved {
		binary = "resolved"
	}
	fmt.Fprintf(out, "hooks runtime=%s status=%s managed=%s config=%s binary=%s\n", report.Runtime, report.Code, report.Managed, cleanText(report.ConfigPath), binary)
	for _, entry := range report.ManagedEntries {
		fmt.Fprintf(out, "  event=%s state=%s exact=%d partial=%d lookalikes=%d\n", entry.Event, entry.State, entry.ExactHandlers, entry.Partial, entry.Lookalikes)
	}
	if report.InlineHooks {
		fmt.Fprintln(out, "  warning=Codex inline hooks require separate trust review")
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(out, "  warning=%s\n", cleanText(warning))
	}
}

func printHookOperation(out io.Writer, result hooks.OperationResult) {
	fmt.Fprintf(out, "hooks runtime=%s action=%s changed=%t would-change=%t summary=%s\n", result.Runtime, result.Action, result.Changed, result.WouldChange, cleanText(result.Summary))
	for _, change := range result.Plan {
		fmt.Fprintf(out, "  plan kind=%s path=%s detail=%s\n", cleanText(change.Kind), cleanText(change.Path), cleanText(change.Detail))
	}
}

// The JSON representation is deliberately a CLI-owned DTO. In particular,
// it does not inherit manager field names or future internal representation
// changes through json.Marshal(StatusReport).
type hookStatusJSON struct {
	SchemaVersion int                     `json:"schema_version"`
	GeneratedAt   string                  `json:"generated_at"`
	Runtimes      []hookRuntimeStatusJSON `json:"runtimes"`
}

type hookRuntimeStatusJSON struct {
	Runtime        string                 `json:"runtime"`
	Status         string                 `json:"status"`
	ConfigPath     string                 `json:"config_path"`
	ConfigExists   bool                   `json:"config_exists"`
	Managed        string                 `json:"managed"`
	ManagedEntries []hookManagedEntryJSON `json:"managed_entries"`
	Binary         hookBinaryJSON         `json:"binary"`
	InlineHooks    bool                   `json:"inline_hooks"`
	TrustReview    hookTrustReviewJSON    `json:"trust_review"`
	Warnings       []string               `json:"warnings"`
}

type hookManagedEntryJSON struct {
	Event         string `json:"event"`
	State         string `json:"state"`
	ExactHandlers int    `json:"exact_handlers"`
	Partial       int    `json:"partial_handlers"`
	Lookalikes    int    `json:"lookalike_handlers"`
}

type hookBinaryJSON struct {
	Name         string `json:"name"`
	Resolved     bool   `json:"resolved"`
	ResolvedPath string `json:"resolved_path"`
	Error        string `json:"error"`
}

type hookTrustReviewJSON struct {
	Required   bool   `json:"required"`
	Limitation string `json:"limitation"`
}

func writeHookStatusJSON(out io.Writer, generatedAt time.Time, reports []hooks.StatusReport) error {
	dto := hookStatusJSON{
		SchemaVersion: 1,
		GeneratedAt:   generatedAt.UTC().Format(time.RFC3339Nano),
		Runtimes:      make([]hookRuntimeStatusJSON, 0, len(reports)),
	}
	for _, report := range reports {
		entries := make([]hookManagedEntryJSON, 0, len(report.ManagedEntries))
		for _, entry := range report.ManagedEntries {
			entries = append(entries, hookManagedEntryJSON{
				Event:         entry.Event,
				State:         string(entry.State),
				ExactHandlers: entry.ExactHandlers,
				Partial:       entry.Partial,
				Lookalikes:    entry.Lookalikes,
			})
		}
		warnings := append([]string(nil), report.Warnings...)
		if warnings == nil {
			warnings = []string{}
		}
		dto.Runtimes = append(dto.Runtimes, hookRuntimeStatusJSON{
			Runtime:        string(report.Runtime),
			Status:         string(report.Code),
			ConfigPath:     report.ConfigPath,
			ConfigExists:   report.ConfigExists,
			Managed:        string(report.Managed),
			ManagedEntries: entries,
			Binary: hookBinaryJSON{
				Name:         report.Binary.Name,
				Resolved:     report.Binary.Resolved,
				ResolvedPath: report.Binary.ResolvedPath,
				Error:        report.Binary.Error,
			},
			InlineHooks: report.InlineHooks,
			TrustReview: hookTrustReviewJSON{
				Required:   report.TrustReview.Required,
				Limitation: report.TrustReview.Limitation,
			},
			Warnings: warnings,
		})
	}
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(dto)
}
