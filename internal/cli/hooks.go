package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

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
		for _, report := range reports {
			printHookStatus(out, report)
		}
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
		printHookOperation(out, result)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s %s: %s", runtime, action, cleanText(err.Error())))
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func hookAction(action string) hooks.Action {
	if action == "uninstall" {
		return hooks.ActionUninstall
	}
	return hooks.ActionInstall
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
