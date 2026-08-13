// Package cli implements the small, local, read-only harness-lint command
// line application. It intentionally keeps argument parsing, orchestration,
// and presentation at the edge; adapters, analysis, and persistence remain
// independently testable packages.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/kespineira/harness-lint/internal/analysis"
	"github.com/kespineira/harness-lint/internal/domain"
	"github.com/kespineira/harness-lint/internal/runtime"
	"github.com/kespineira/harness-lint/internal/runtime/claude"
	"github.com/kespineira/harness-lint/internal/runtime/codex"
	"github.com/kespineira/harness-lint/internal/store"
)

const (
	defaultStaleDays = 60
	lastUsedWindow   = 30 * 24 * time.Hour
)

var supportedCommands = map[string]struct{}{
	"scan":    {},
	"report":  {},
	"context": {},
	"stale":   {},
	"doctor":  {},
}

var knownRuntimes = []domain.Runtime{
	domain.RuntimeClaudeCode,
	domain.RuntimeCodex,
}

// Options injects process-dependent values for deterministic tests. Non-empty
// Home, CWD, and ProjectRoot values override the live defaults.
type Options struct {
	Home        string
	CWD         string
	ProjectRoot string
	ConfigDir   string
	Now         func() time.Time
	LookPath    func(string) (string, error)
}

// Execute runs one command using process standard inputs and outputs passed
// explicitly by the caller. It is the public seam used by cmd/harness-lint
// and end-to-end tests.
func Execute(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return ExecuteWithOptions(Options{}, args, stdin, stdout, stderr)
}

// ExecuteWithOptions is Execute with injected home, current directory, clock,
// and executable lookup values. stdin is reserved for future metadata-only
// input sources and is intentionally not read by the current commands.
func ExecuteWithOptions(options Options, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	_ = stdin
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if !hasCommandToken(args) && hasHelpFlag(args) {
		writeUsage(stdout)
		return nil
	}

	command, commandArgs, err := splitCommand(args)
	if err != nil {
		return err
	}
	if command == "" {
		writeUsage(stdout)
		return nil
	}
	if hasHelpFlag(args) {
		writeCommandUsage(stdout, command)
		return nil
	}

	parsed, err := parseFlags(command, commandArgs, stderr)
	if err != nil {
		return err
	}
	config, err := resolveConfig(options, parsed)
	if err != nil {
		return err
	}

	ctx := context.Background()
	switch command {
	case "scan":
		return runScan(ctx, config, stdout)
	case "report":
		return runReport(ctx, config, stdout)
	case "context":
		return runContext(ctx, config, stdout)
	case "stale":
		return runStale(ctx, config, stdout)
	case "doctor":
		return runDoctor(ctx, config, stdout)
	default:
		return fmt.Errorf("unknown command %q (want scan, report, context, stale, or doctor)", command)
	}
}

type stringListFlag []string

func (f *stringListFlag) String() string { return strings.Join(*f, ",") }

func (f *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("hook-capture path cannot be empty")
	}
	*f = append(*f, value)
	return nil
}

type parsedFlags struct {
	dbPath       string
	dbSet        bool
	home         string
	homeSet      bool
	project      string
	projectSet   bool
	configDir    string
	configDirSet bool
	codexHome    string
	codexSet     bool
	claudeConfig string
	claudeSet    bool
	nowText      string
	sinceText    string
	days         int
	hooks        []string
}

func parseFlags(command string, args []string, stderr io.Writer) (parsedFlags, error) {
	fs := flag.NewFlagSet("harness-lint "+command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: harness-lint %s [options]\n", command)
		fmt.Fprintln(stderr, "commands: scan, report, context, stale, doctor")
	}
	var result parsedFlags
	fs.StringVar(&result.dbPath, "db", "", "SQLite database path")
	fs.StringVar(&result.home, "home", "", "synthetic user home")
	fs.StringVar(&result.project, "project", "", "project root (defaults to a repository ancestor of the current directory)")
	fs.StringVar(&result.configDir, "config-dir", "", "configuration directory used for the default database")
	fs.StringVar(&result.codexHome, "codex-home", "", "Codex configuration root")
	fs.StringVar(&result.claudeConfig, "claude-config", "", "Claude configuration root")
	fs.StringVar(&result.nowText, "now", "", "observation time in RFC3339 form")
	fs.StringVar(&result.sinceText, "since", "", "inclusive usage-import boundary in RFC3339 form")
	fs.IntVar(&result.days, "days", defaultStaleDays, "stale threshold in days")
	var hooks stringListFlag
	fs.Var(&hooks, "hook-capture", "repeatable metadata-only hook capture path")

	if err := fs.Parse(args); err != nil {
		return parsedFlags{}, err
	}
	if fs.NArg() != 0 {
		return parsedFlags{}, fmt.Errorf("unexpected argument(s) for %s: %s", command, strings.Join(fs.Args(), " "))
	}
	result.hooks = append([]string(nil), hooks...)
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "db":
			result.dbSet = true
		case "home":
			result.homeSet = true
		case "project":
			result.projectSet = true
		case "config-dir":
			result.configDirSet = true
		case "codex-home":
			result.codexSet = true
		case "claude-config":
			result.claudeSet = true
		}
	})
	if result.days < 0 {
		return parsedFlags{}, errors.New("--days cannot be negative")
	}
	return result, nil
}

type commandConfig struct {
	dbPath       string
	home         string
	projectRoot  string
	currentDir   string
	codexHome    string
	claudeConfig string
	hooks        []string
	since        time.Time
	now          time.Time
	days         int
	lookPath     func(string) (string, error)
}

func resolveConfig(options Options, flags parsedFlags) (commandConfig, error) {
	currentDir, err := resolveCurrentDirectory(options)
	if err != nil {
		return commandConfig{}, err
	}
	home, err := resolveHome(options, flags, currentDir)
	if err != nil {
		return commandConfig{}, err
	}
	projectRoot, err := resolveProjectRoot(options, flags, currentDir)
	if err != nil {
		return commandConfig{}, err
	}

	clock := options.Now
	now := time.Now()
	if clock != nil {
		now = clock()
	}
	if flags.nowText != "" {
		now, err = time.Parse(time.RFC3339Nano, flags.nowText)
		if err != nil {
			return commandConfig{}, fmt.Errorf("parse --now: %w", err)
		}
	}
	if now.IsZero() {
		return commandConfig{}, errors.New("observation clock returned zero time")
	}
	now = now.UTC()

	since := time.Time{}
	if flags.sinceText != "" {
		since, err = time.Parse(time.RFC3339Nano, flags.sinceText)
		if err != nil {
			return commandConfig{}, fmt.Errorf("parse --since: %w", err)
		}
		since = since.UTC()
	}

	dbPath := ""
	if flags.dbSet {
		dbPath = flags.dbPath
	} else if options.ConfigDir != "" {
		dbPath = filepath.Join(options.ConfigDir, "harness-lint", "harness-lint.db")
	} else if flags.configDirSet {
		dbPath = filepath.Join(flags.configDir, "harness-lint", "harness-lint.db")
	} else {
		base, configErr := os.UserConfigDir()
		if configErr != nil {
			return commandConfig{}, fmt.Errorf("resolve user config directory: %w", configErr)
		}
		dbPath = filepath.Join(base, "harness-lint", "harness-lint.db")
	}
	if dbPath != ":memory:" {
		dbPath, err = absolutePath(dbPath, currentDir)
		if err != nil {
			return commandConfig{}, fmt.Errorf("resolve database path: %w", err)
		}
	}

	codexHome := ""
	if home != "" {
		codexHome = filepath.Join(home, ".codex")
	}
	if flags.codexSet {
		codexHome = flags.codexHome
	}
	claudeConfig := ""
	if home != "" {
		claudeConfig = filepath.Join(home, ".claude")
	}
	if flags.claudeSet {
		claudeConfig = flags.claudeConfig
	}
	codexHome, err = optionalAbsolutePath(codexHome, currentDir)
	if err != nil {
		return commandConfig{}, fmt.Errorf("resolve Codex home: %w", err)
	}
	claudeConfig, err = optionalAbsolutePath(claudeConfig, currentDir)
	if err != nil {
		return commandConfig{}, fmt.Errorf("resolve Claude config: %w", err)
	}

	hooks := make([]string, 0, len(flags.hooks))
	for _, hook := range flags.hooks {
		path, pathErr := absolutePath(hook, currentDir)
		if pathErr != nil {
			return commandConfig{}, fmt.Errorf("resolve hook-capture path: %w", pathErr)
		}
		hooks = append(hooks, path)
	}
	return commandConfig{
		dbPath:       dbPath,
		home:         home,
		projectRoot:  projectRoot,
		currentDir:   currentDir,
		codexHome:    codexHome,
		claudeConfig: claudeConfig,
		hooks:        hooks,
		since:        since,
		now:          now,
		days:         flags.days,
		lookPath:     options.LookPath,
	}, nil
}

func resolveHome(options Options, flags parsedFlags, currentDir string) (string, error) {
	if flags.homeSet {
		path, err := optionalAbsolutePath(flags.home, currentDir)
		return path, err
	}
	if options.Home != "" {
		path, err := optionalAbsolutePath(options.Home, currentDir)
		return path, err
	}
	path, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	path, err = optionalAbsolutePath(path, "")
	return path, err
}

func resolveCurrentDirectory(options Options) (string, error) {
	value := ""
	if options.CWD != "" {
		value = options.CWD
	} else {
		var err error
		value, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current directory: %w", err)
		}
	}
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return absolutePath(value, "")
}

func resolveProjectRoot(options Options, flags parsedFlags, currentDir string) (string, error) {
	if flags.projectSet {
		if strings.TrimSpace(flags.project) == "" {
			return currentDir, nil
		}
		return absolutePath(flags.project, currentDir)
	}
	if options.ProjectRoot != "" {
		return absolutePath(options.ProjectRoot, currentDir)
	}
	return detectProjectRoot(currentDir), nil
}

func absolutePath(path, base string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) {
		if base == "" {
			var err error
			base, err = os.Getwd()
			if err != nil {
				return "", err
			}
		}
		path = filepath.Join(base, path)
	}
	return filepath.Abs(filepath.Clean(path))
}

func optionalAbsolutePath(path, base string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	return absolutePath(path, base)
}

func detectProjectRoot(currentDir string) string {
	if currentDir == "" {
		return ""
	}
	dir := filepath.Clean(currentDir)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Clean(currentDir)
		}
		dir = parent
	}
}

func splitCommand(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, nil
	}
	commandIndex := -1
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			if strings.Contains(arg, "=") {
				continue
			}
			if consumesValueFlag(arg) {
				i++
			}
			continue
		}
		if _, ok := supportedCommands[arg]; ok {
			commandIndex = i
			break
		}
	}
	if commandIndex < 0 {
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			return "", nil, fmt.Errorf("unknown command %q (want scan, report, context, stale, or doctor)", args[0])
		}
		return "", nil, errors.New("a command is required: scan, report, context, stale, or doctor")
	}
	command := args[commandIndex]
	commandArgs := append([]string(nil), args[:commandIndex]...)
	commandArgs = append(commandArgs, args[commandIndex+1:]...)
	return command, commandArgs, nil
}

func consumesValueFlag(arg string) bool {
	switch arg {
	case "-db", "--db", "-home", "--home", "-project", "--project", "-config-dir", "--config-dir", "-codex-home", "--codex-home", "-claude-config", "--claude-config", "-now", "--now", "-since", "--since", "-days", "--days", "-hook-capture", "--hook-capture":
		return true
	default:
		return false
	}
}

func writeUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: harness-lint <scan|report|context|stale|doctor> [options]")
	fmt.Fprintln(w, "commands: scan, report, context, stale, doctor")
	fmt.Fprintln(w, "use harness-lint <command> --help for command options")
}

func writeCommandUsage(w io.Writer, command string) {
	fmt.Fprintf(w, "usage: harness-lint %s [options]\n", command)
	fmt.Fprintln(w, "options:")
	fmt.Fprintln(w, "  --db PATH                  SQLite database path")
	fmt.Fprintln(w, "  --home PATH                synthetic user home")
	fmt.Fprintln(w, "  --project PATH             project root")
	fmt.Fprintln(w, "  --config-dir PATH          default database configuration directory")
	fmt.Fprintln(w, "  --codex-home PATH          Codex configuration root")
	fmt.Fprintln(w, "  --claude-config PATH       Claude configuration root")
	fmt.Fprintln(w, "  --hook-capture PATH        repeatable metadata-only hook capture path")
	fmt.Fprintln(w, "  --since RFC3339            inclusive usage-import boundary")
	fmt.Fprintln(w, "  --days N                   stale threshold in days (default 60)")
	fmt.Fprintln(w, "  --now RFC3339              observation time")
}

func hasCommandToken(args []string) bool {
	for _, arg := range args {
		if _, ok := supportedCommands[arg]; ok {
			return true
		}
	}
	return false
}

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
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

func printFinding(out io.Writer, finding domain.Finding) {
	capability := "unknown"
	if finding.CapabilityType.Valid() {
		capability = string(finding.CapabilityType) + "/" + cleanText(finding.CapabilityName)
	}
	fmt.Fprintf(out, "finding runtime=%s code=%s severity=%s confidence=%s capability=%s message=%s\n", finding.Runtime, cleanText(finding.Code), finding.Severity, finding.Confidence, capability, cleanText(finding.Message))
}

func printRuntimeCounts(out io.Writer, result analysis.Report, events []domain.UsageEvent, now time.Time) {
	for _, runtimeName := range knownRuntimes {
		installed := 0
		configuredAdvertised := 0
		advertisedEvents, loadedEvents, invokedEvents := 0, 0, 0
		usedLast30, neverUsed := 0, 0
		for _, event := range events {
			if event.Runtime != runtimeName {
				continue
			}
			switch event.EventType {
			case domain.EventAdvertised:
				advertisedEvents++
			case domain.EventLoaded:
				loadedEvents++
			case domain.EventInvoked:
				invokedEvents++
			}
		}
		for _, evidence := range result.Capabilities {
			if evidence.Capability.Runtime != runtimeName {
				continue
			}
			installed++
			if evidence.Capability.Advertisement == domain.AdvertisementStateFullyAdvertised || evidence.Capability.Advertisement == domain.AdvertisementStateNameOnly {
				configuredAdvertised++
			}
			if evidence.ActivityCount == 0 {
				neverUsed++
			}
			if evidence.HasLastUsed && !evidence.LastUsedAt.Before(now.Add(-lastUsedWindow)) && !evidence.LastUsedAt.After(now) {
				usedLast30++
			}
		}
		usageEvents := 0
		for _, event := range events {
			if event.Runtime == runtimeName {
				usageEvents++
			}
		}
		fmt.Fprintf(out, "runtime=%s installed=%d advertised=%d loaded=%d invoked=%d configured-advertised=%d used-last-30d=%d never-used=%d usage-events=%d\n", runtimeName, installed, advertisedEvents, loadedEvents, invokedEvents, configuredAdvertised, usedLast30, neverUsed, usageEvents)
	}
}

func printCapabilityEvidence(out io.Writer, result analysis.Report, now time.Time) {
	if len(result.Capabilities) == 0 {
		fmt.Fprintln(out, "no current capabilities")
		return
	}
	fmt.Fprintln(out, "capabilities:")
	for _, evidence := range result.Capabilities {
		lastUsed := "never"
		usedLast30 := "no"
		if evidence.HasLastUsed {
			lastUsed = evidence.LastUsedAt.UTC().Format(time.RFC3339)
			if !evidence.LastUsedAt.Before(now.Add(-lastUsedWindow)) && !evidence.LastUsedAt.After(now) {
				usedLast30 = "yes"
			}
		}
		fmt.Fprintf(out, "  runtime=%s type=%s name=%s status=%s advertised=%d loaded=%d invoked=%d exposure=%s used-last-30d=%s last-used=%s evidence=%s source=%s\n", evidence.Capability.Runtime, evidence.Capability.Type, cleanText(evidence.Capability.Name), evidence.Classification, evidence.EventCount(domain.EventAdvertised), evidence.EventCount(domain.EventLoaded), evidence.EventCount(domain.EventInvoked), evidence.Capability.Advertisement, usedLast30, lastUsed, cleanText(evidence.Basis), cleanText(evidence.Capability.Source))
	}
}

type usageKey struct {
	runtime domain.Runtime
	typ     domain.CapabilityType
	name    string
}

func printUsageOnly(out io.Writer, installed []analysis.CapabilityEvidence, events []domain.UsageEvent) {
	installedKeys := make(map[usageKey]struct{}, len(installed))
	for _, evidence := range installed {
		installedKeys[usageKey{runtime: evidence.Capability.Runtime, typ: evidence.Capability.Type, name: evidence.Capability.Name}] = struct{}{}
	}
	type usageCounts struct {
		advertised int
		loaded     int
		invoked    int
	}
	counts := make(map[usageKey]*usageCounts)
	for _, event := range events {
		key := usageKey{runtime: event.Runtime, typ: event.CapabilityType, name: event.CapabilityName}
		if _, ok := installedKeys[key]; ok {
			continue
		}
		count := counts[key]
		if count == nil {
			count = &usageCounts{}
			counts[key] = count
		}
		switch event.EventType {
		case domain.EventAdvertised:
			count.advertised++
		case domain.EventLoaded:
			count.loaded++
		case domain.EventInvoked:
			count.invoked++
		}
	}
	keys := make([]usageKey, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].runtime != keys[j].runtime {
			return keys[i].runtime < keys[j].runtime
		}
		if keys[i].typ != keys[j].typ {
			return keys[i].typ < keys[j].typ
		}
		return keys[i].name < keys[j].name
	})
	if len(keys) == 0 {
		return
	}
	fmt.Fprintln(out, "usage-only (observed usage is not installed inventory):")
	for _, key := range keys {
		count := counts[key]
		fmt.Fprintf(out, "  runtime=%s type=%s name=%s advertised=%d loaded=%d invoked=%d status=usage-only\n", key.runtime, key.typ, cleanText(key.name), count.advertised, count.loaded, count.invoked)
	}
}

func formatMeasurementSummary(summary analysis.MeasurementSummary) string {
	confidence := summary.Confidence
	if summary.KnownCount == 0 {
		return string(domain.ConfidenceUnknown)
	}
	value := fmt.Sprintf("%d tokens (%s", summary.Value, confidence)
	if summary.UnknownCount > 0 {
		value += "; partial"
	}
	return value + ")"
}

func cleanText(value string) string {
	var builder strings.Builder
	for _, character := range value {
		if unicode.IsControl(character) {
			builder.WriteByte(' ')
			continue
		}
		builder.WriteRune(character)
	}
	return strings.TrimSpace(builder.String())
}
