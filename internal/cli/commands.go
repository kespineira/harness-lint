package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/analysis"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/history"
	reportdto "github.com/kespineira/harness-lint/internal/report"
	"github.com/kespineira/harness-lint/internal/runtime"
	"github.com/kespineira/harness-lint/internal/runtime/claude"
	"github.com/kespineira/harness-lint/internal/runtime/codex"
	"github.com/kespineira/harness-lint/internal/store"
	usagedto "github.com/kespineira/harness-lint/internal/usage"
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

func runUsage(ctx context.Context, config commandConfig, flags parsedFlags, out io.Writer) error {
	if config.days <= 0 {
		return errors.New("usage --days must be greater than zero")
	}
	duration, err := durationForDays(config.days)
	if err != nil {
		return fmt.Errorf("usage %w", err)
	}
	runtimeFilter, typeFilter, mcpUnion, err := usageFilters(flags)
	if err != nil {
		return err
	}
	start := config.now.Add(-duration)
	query := history.Query{Start: start, End: config.now, Runtime: runtimeFilter}
	if !mcpUnion {
		query.CapabilityType = typeFilter
	}
	db, err := openStore(config)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	aggregates, err := queryUsageAggregates(ctx, db, query, mcpUnion)
	if err != nil {
		return fmt.Errorf("query usage history: %w", err)
	}
	var monthly []history.MonthlyAggregate
	if flags.monthly {
		monthly, err = queryUsageMonthly(ctx, db, query, mcpUnion)
		if err != nil {
			return fmt.Errorf("query monthly usage history: %w", err)
		}
	}
	runtimeFilterText := usageRuntimeFilterText(runtimeFilter, flags.runtimeSet)
	typeFilterText := usageTypeFilterText(typeFilter, mcpUnion, flags.typeSet)
	document, err := usagedto.Build(usagedto.BuildInput{
		GeneratedAt:    config.now,
		Days:           config.days,
		RuntimeFilter:  runtimeFilterText,
		TypeFilter:     typeFilterText,
		Aggregates:     aggregates,
		Monthly:        monthly,
		IncludeMonthly: flags.monthly,
	})
	if err != nil {
		return fmt.Errorf("build usage output: %w", err)
	}
	if config.json {
		return usagedto.WriteJSON(out, document)
	}
	printUsageDocument(out, document)
	return nil
}

func queryUsageAggregates(ctx context.Context, db *store.Store, query history.Query, mcpUnion bool) ([]history.Aggregate, error) {
	if !mcpUnion {
		return db.QueryInvocationHistory(ctx, query)
	}
	result := make([]history.Aggregate, 0)
	for _, capabilityType := range []domain.CapabilityType{domain.CapabilityMCPServer, domain.CapabilityMCPTool} {
		bounded := query
		bounded.CapabilityType = capabilityType
		aggregates, err := db.QueryInvocationHistory(ctx, bounded)
		if err != nil {
			return nil, err
		}
		result = append(result, aggregates...)
	}
	sortUsageAggregates(result)
	return result, nil
}

func queryUsageMonthly(ctx context.Context, db *store.Store, query history.Query, mcpUnion bool) ([]history.MonthlyAggregate, error) {
	if !mcpUnion {
		return db.QueryMonthlyInvocations(ctx, query)
	}
	result := make([]history.MonthlyAggregate, 0)
	for _, capabilityType := range []domain.CapabilityType{domain.CapabilityMCPServer, domain.CapabilityMCPTool} {
		bounded := query
		bounded.CapabilityType = capabilityType
		monthly, err := db.QueryMonthlyInvocations(ctx, bounded)
		if err != nil {
			return nil, err
		}
		result = append(result, monthly...)
	}
	sortUsageMonthly(result)
	return result, nil
}

func sortUsageAggregates(values []history.Aggregate) {
	sort.SliceStable(values, func(left, right int) bool {
		if values[left].Runtime != values[right].Runtime {
			return values[left].Runtime < values[right].Runtime
		}
		if values[left].CapabilityType != values[right].CapabilityType {
			return values[left].CapabilityType < values[right].CapabilityType
		}
		return values[left].CapabilityName < values[right].CapabilityName
	})
}

func sortUsageMonthly(values []history.MonthlyAggregate) {
	sort.SliceStable(values, func(left, right int) bool {
		if values[left].Runtime != values[right].Runtime {
			return values[left].Runtime < values[right].Runtime
		}
		if values[left].CapabilityType != values[right].CapabilityType {
			return values[left].CapabilityType < values[right].CapabilityType
		}
		if values[left].CapabilityName != values[right].CapabilityName {
			return values[left].CapabilityName < values[right].CapabilityName
		}
		return values[left].Month.Before(values[right].Month)
	})
}

func usageFilters(flags parsedFlags) (domain.Runtime, domain.CapabilityType, bool, error) {
	runtimeFilter := domain.Runtime("")
	if flags.runtimeSet {
		parsed, err := usageRuntime(flags.runtime)
		if err != nil {
			return "", "", false, err
		}
		runtimeFilter = parsed
	}
	typeFilter := domain.CapabilityType("")
	mcpUnion := false
	if flags.typeSet {
		parsed, err := usageType(flags.capabilityType)
		if err != nil {
			return "", "", false, err
		}
		typeFilter = parsed
		mcpUnion = strings.EqualFold(strings.TrimSpace(flags.capabilityType), "mcp")
	}
	return runtimeFilter, typeFilter, mcpUnion, nil
}

func usageRuntimeFilterText(value domain.Runtime, selected bool) *string {
	if !selected {
		return nil
	}
	normalized := string(value)
	return &normalized
}

func usageTypeFilterText(value domain.CapabilityType, mcpUnion, selected bool) *string {
	if !selected {
		return nil
	}
	normalized := ""
	if mcpUnion {
		normalized = "mcp"
	} else {
		normalized = string(value)
	}
	return &normalized
}

const maxDurationDays = int64(1<<63-1) / int64(24*time.Hour)

func durationForDays(days int) (time.Duration, error) {
	if days <= 0 {
		return 0, errors.New("days must be greater than zero")
	}
	if int64(days) > maxDurationDays {
		return 0, errors.New("days exceed supported UTC period range")
	}
	return time.Duration(days) * 24 * time.Hour, nil
}

func loadReport(ctx context.Context, config commandConfig, staleDays int) (analysis.Report, []history.Aggregate, error) {
	staleAfter, err := durationForDays(staleDays)
	if err != nil {
		return analysis.Report{}, nil, fmt.Errorf("--days %w", err)
	}
	db, err := openStore(config)
	if err != nil {
		return analysis.Report{}, nil, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	capabilities, err := currentCapabilities(ctx, db)
	if err != nil {
		return analysis.Report{}, nil, err
	}
	aggregates, err := db.QueryInvocationHistory(ctx, history.Query{})
	if err != nil {
		return analysis.Report{}, nil, fmt.Errorf("query usage history: %w", err)
	}
	policy := analysis.DefaultConfig()
	policy.StaleAfter = staleAfter
	result, err := analysis.AnalyzeHistory(capabilities, aggregates, policy, config.now)
	if err != nil {
		return analysis.Report{}, nil, fmt.Errorf("analyze current inventory: %w", err)
	}
	return result, aggregates, nil
}

func runReport(ctx context.Context, config commandConfig, out io.Writer) error {
	result, aggregates, err := loadReport(ctx, config, config.days)
	if err != nil {
		return err
	}
	if config.json {
		document, buildErr := reportdto.BuildReportHistory(result, aggregates, config.now, config.days)
		if buildErr != nil {
			return fmt.Errorf("build report JSON: %w", buildErr)
		}
		return reportdto.WriteJSON(out, document)
	}
	fmt.Fprintf(out, "report as-of=%s stale-days=%d\n", config.now.Format(time.RFC3339), config.days)
	aggregateIndex := buildAggregateIndex(aggregates)
	printRuntimeCounts(out, result, aggregates, config.now)
	printCapabilityEvidenceWithHistory(out, result, aggregateIndex, config.now)
	printDuplicateFindings(out, result)
	printUsageOnlyWithAnalysis(out, result, aggregateIndex, config.now)
	return nil
}

func runStale(ctx context.Context, config commandConfig, out io.Writer) error {
	result, aggregates, err := loadReport(ctx, config, config.days)
	if err != nil {
		return err
	}
	if config.json {
		document, buildErr := reportdto.BuildStaleHistory(result, aggregates, config.now, config.days)
		if buildErr != nil {
			return fmt.Errorf("build stale JSON: %w", buildErr)
		}
		return reportdto.WriteJSON(out, document)
	}
	fmt.Fprintf(out, "stale as-of=%s days=%d\n", config.now.Format(time.RFC3339), config.days)
	if len(result.Capabilities) == 0 {
		fmt.Fprintln(out, "no current capabilities")
		return nil
	}
	aggregateIndex := buildAggregateIndex(aggregates)
	printCapabilityEvidenceWithHistory(out, result, aggregateIndex, config.now)
	printDuplicateFindings(out, result)
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
		printCompatibilityDiagnostic(out, detectCompatibility(ctx, config, runtimeName))
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
