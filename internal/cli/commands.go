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

var errDatabaseNotInitialized = errors.New("database is not initialized")

func openExistingStore(config commandConfig) (*store.Store, error) {
	if config.dbPath == "" {
		return nil, errors.New("database path is empty")
	}
	db, err := store.OpenExisting(config.dbPath)
	if errors.Is(err, store.ErrStoreNotFound) {
		return nil, fmt.Errorf("%w; run `harness-lint scan` first", errDatabaseNotInitialized)
	}
	if err != nil {
		return nil, errors.New("database is unavailable")
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
	view, failures := collectScanView(ctx, config, db, adapters)
	renderScanView(out, config.renderer, config.verbose, view)
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
	db, err := openExistingStore(config)
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
	if config.json {
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
		return usagedto.WriteJSON(out, document)
	}
	renderUsageView(out, config.renderer, usageView{
		now:            config.now,
		days:           config.days,
		runtimeFilter:  runtimeFilter,
		typeFilter:     typeFilter,
		mcpUnion:       mcpUnion,
		runtimeSet:     flags.runtimeSet,
		typeSet:        flags.typeSet,
		aggregates:     aggregates,
		monthly:        monthly,
		includeMonthly: flags.monthly,
	})
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
	db, err := openExistingStore(config)
	if err != nil {
		return analysis.Report{}, nil, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	capabilities, err := currentCapabilities(ctx, db)
	if err != nil {
		return analysis.Report{}, nil, err
	}
	// Keep released report/stale history semantics unbounded: future observations
	// and their diagnostics remain visible. Effective coverage is clipped only
	// when projected into the report DTO at config.now.
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
	renderReportView(out, config.renderer, config.all, config.verbose, result, aggregates, config.now, config.days)
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
	renderStaleView(out, config.renderer, config.verbose, result, config.now, config.days)
	return nil
}

func runContext(ctx context.Context, config commandConfig, out io.Writer) error {
	result, _, err := loadReport(ctx, config, config.days)
	if err != nil {
		return err
	}
	renderContextView(out, config.renderer, result.Context)
	return nil
}

func runDoctor(ctx context.Context, config commandConfig, out io.Writer) error {
	set := newAdapters(config)
	var failures []string
	runtimes := make([]doctorRuntimeView, 0, len(knownRuntimes))
	for _, adapter := range orderedAdapters(set) {
		runtimeName := adapter.Runtime()
		view := doctorRuntimeView{runtime: runtimeName, compatibility: detectCompatibility(ctx, config, runtimeName)}
		discovery, err := adapter.Discover(ctx)
		if err != nil {
			view.discoveryUnavailable = true
			runtimes = append(runtimes, view)
			failures = append(failures, fmt.Sprintf("runtime %s discovery: %v", runtimeName, err))
			continue
		}
		view.capabilities = len(discovery.Capabilities)
		view.findings = append(view.findings, discovery.Findings...)
		duplicates, duplicateErr := analysis.DetectDuplicateNames(discovery.Capabilities)
		if duplicateErr != nil {
			view.duplicateCheckError = true
			runtimes = append(runtimes, view)
			failures = append(failures, fmt.Sprintf("runtime %s duplicate analysis: %v", runtimeName, duplicateErr))
			continue
		}
		view.duplicates = duplicates
		runtimes = append(runtimes, view)
	}
	renderDoctorView(out, config.renderer, config.verbose, runtimes)
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}
