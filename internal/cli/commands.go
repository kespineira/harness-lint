package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/analysis"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/runtime"
	"github.com/kespineira/harness-lint/internal/runtime/claude"
	"github.com/kespineira/harness-lint/internal/runtime/codex"
	"github.com/kespineira/harness-lint/internal/store"
)

const lastUsedWindow = 30 * 24 * time.Hour

var knownRuntimes = []domain.Runtime{
	domain.RuntimeClaudeCode,
	domain.RuntimeCodex,
}

type adapterSet struct {
	claude runtime.Adapter
	codex  runtime.Adapter
}

func newAdapters(config commandConfig) adapterSet {
	var systemRoots []string
	if info, err := os.Stat("/etc/codex/skills"); err == nil && info.IsDir() {
		systemRoots = append(systemRoots, "/etc/codex/skills")
	}
	now := config.now
	clock := func() time.Time { return now }
	return adapterSet{
		claude: claude.New(claude.Options{
			UserHome:         config.home,
			ConfigRoot:       config.claudeConfig,
			ProjectRoot:      config.projectRoot,
			CurrentDirectory: config.currentDir,
			Now:              clock,
			LookPath:         config.lookPath,
			HookEventPaths:   config.hooks,
		}),
		codex: codex.New(codex.Options{
			UserHome:         config.home,
			ConfigRoot:       config.codexHome,
			ProjectRoot:      config.projectRoot,
			CurrentDirectory: config.currentDir,
			SystemSkillRoots: systemRoots,
			HookEventPaths:   config.hooks,
			Now:              clock,
			LookPath:         config.lookPath,
		}),
	}
}

func orderedAdapters(set adapterSet) []runtime.Adapter {
	return []runtime.Adapter{set.claude, set.codex}
}

func openStore(config commandConfig) (*store.Store, error) {
	if config.dbPath == "" {
		return nil, errors.New("database path is empty")
	}
	if config.dbPath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(config.dbPath), 0o700); err != nil {
			return nil, fmt.Errorf("create database parent: %w", err)
		}
	}
	db, err := store.Open(config.dbPath)
	if err != nil {
		return nil, err
	}
	return db, nil
}

func runScan(ctx context.Context, config commandConfig, out io.Writer) error {
	db, err := openStore(config)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	return runScanWithAdapters(ctx, config, db, out, orderedAdapters(newAdapters(config)))
}

func runScanWithAdapters(ctx context.Context, config commandConfig, db *store.Store, out io.Writer, adapters []runtime.Adapter) error {
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
		status := "recorded"
		if !inventoryRecorded {
			status = "not-recorded"
		}
		fmt.Fprintf(out, "scan runtime=%s capabilities=%d events=%d findings=%d inventory=%s\n", runtimeName, len(discovery.Capabilities), len(events), len(discovery.Findings), status)
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func currentCapabilities(ctx context.Context, db *store.Store) ([]domain.Capability, error) {
	var result []domain.Capability
	for _, runtimeName := range knownRuntimes {
		capabilities, err := db.ListCurrentCapabilities(ctx, runtimeName)
		if err != nil {
			return nil, fmt.Errorf("list current %s capabilities: %w", runtimeName, err)
		}
		result = append(result, capabilities...)
	}
	return result, nil
}

func loadReport(ctx context.Context, config commandConfig, staleDays int) (analysis.Report, []domain.UsageEvent, error) {
	db, err := openStore(config)
	if err != nil {
		return analysis.Report{}, nil, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	capabilities, err := currentCapabilities(ctx, db)
	if err != nil {
		return analysis.Report{}, nil, err
	}
	events, err := db.ListUsageEvents(ctx, time.Time{})
	if err != nil {
		return analysis.Report{}, nil, fmt.Errorf("list usage history: %w", err)
	}
	if staleDays <= 0 {
		return analysis.Report{}, nil, errors.New("--days must be greater than zero")
	}
	policy := analysis.DefaultConfig()
	policy.StaleAfter = time.Duration(staleDays) * 24 * time.Hour
	result, err := analysis.Analyze(capabilities, events, policy, config.now)
	if err != nil {
		return analysis.Report{}, nil, fmt.Errorf("analyze current inventory: %w", err)
	}
	return result, events, nil
}

func runReport(ctx context.Context, config commandConfig, out io.Writer) error {
	result, events, err := loadReport(ctx, config, config.days)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "report as-of=%s stale-days=%d\n", config.now.Format(time.RFC3339), config.days)
	printRuntimeCounts(out, result, events, config.now)
	printCapabilityEvidence(out, result, config.now)
	printUsageOnly(out, result.Capabilities, events)
	return nil
}

func runStale(ctx context.Context, config commandConfig, out io.Writer) error {
	result, _, err := loadReport(ctx, config, config.days)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "stale as-of=%s days=%d\n", config.now.Format(time.RFC3339), config.days)
	if len(result.Capabilities) == 0 {
		fmt.Fprintln(out, "no current capabilities")
		return nil
	}
	printCapabilityEvidence(out, result, config.now)
	return nil
}

func runContext(ctx context.Context, config commandConfig, out io.Writer) error {
	result, _, err := loadReport(ctx, config, config.days)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "context as-of=%s\n", config.now.Format(time.RFC3339))
	if len(result.Context.Groups) == 0 {
		fmt.Fprintln(out, "no configured capabilities; configured baseline exposure=unknown; on-load footprint=unknown")
		return nil
	}
	for _, group := range result.Context.Groups {
		fmt.Fprintf(out, "runtime=%s type=%s capabilities=%d\n", group.Runtime, group.CapabilityType, group.CapabilityCount)
		switch group.CapabilityType {
		case domain.CapabilitySkill:
			fmt.Fprintf(out, "  configured baseline exposure: metadata=%s (according to Advertisement); body=not included (Skill body is on-load only)\n", formatMeasurementSummary(group.MetadataTokens))
			fmt.Fprintf(out, "  on-load footprint estimate: body=%s; metadata=not measured (unknown)\n", formatMeasurementSummary(group.BodyTokens))
		case domain.CapabilityInstructionFile:
			fmt.Fprintf(out, "  configured baseline exposure: body=%s; metadata=unknown\n", formatMeasurementSummary(group.BodyTokens))
			fmt.Fprintln(out, "  on-load footprint: unknown (not separately measured)")
		default:
			fmt.Fprintf(out, "  configured baseline exposure: metadata=%s; body=not included in baseline\n", formatMeasurementSummary(group.MetadataTokens))
			fmt.Fprintf(out, "  on-load/on-invocation footprint: body=%s\n", formatMeasurementSummary(group.BodyTokens))
		}
	}
	return nil
}

func runDoctor(ctx context.Context, config commandConfig, out io.Writer) error {
	set := newAdapters(config)
	var failures []string
	for _, adapter := range orderedAdapters(set) {
		runtimeName := adapter.Runtime()
		discovery, err := adapter.Discover(ctx)
		if err != nil {
			fmt.Fprintf(out, "runtime=%s discovery=error error=%s\n", runtimeName, cleanText(err.Error()))
			failures = append(failures, fmt.Sprintf("runtime %s discovery: %v", runtimeName, err))
			continue
		}
		fmt.Fprintf(out, "runtime=%s capabilities=%d findings=%d\n", runtimeName, len(discovery.Capabilities), len(discovery.Findings))
		for _, finding := range discovery.Findings {
			printFinding(out, finding)
		}
		duplicates, duplicateErr := analysis.DetectDuplicateNames(discovery.Capabilities)
		if duplicateErr != nil {
			failures = append(failures, fmt.Sprintf("runtime %s duplicate analysis: %v", runtimeName, duplicateErr))
			continue
		}
		for _, duplicate := range duplicates {
			fmt.Fprintf(out, "finding runtime=%s code=duplicate-capability severity=warning confidence=observed capability=%s/%s evidence=definitions=%d", duplicate.Runtime, duplicate.CapabilityType, cleanText(duplicate.Name), len(duplicate.Definitions))
			for _, definition := range duplicate.Definitions {
				fmt.Fprintf(out, " source=%s scope=%s", cleanText(definition.Source), definition.Scope)
			}
			fmt.Fprintln(out)
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}
