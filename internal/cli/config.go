package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kespineira/harness-lint/internal/compatibility"
	"github.com/kespineira/harness-lint/internal/domain"
)

const (
	defaultStaleDays = 60
	defaultUsageDays = 90
)

var supportedCommands = map[string]struct{}{
	"ingest":  {},
	"hooks":   {},
	"usage":   {},
	"scan":    {},
	"report":  {},
	"context": {},
	"stale":   {},
	"doctor":  {},
	"db":      {},
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
	dbPath         string
	dbSet          bool
	home           string
	homeSet        bool
	project        string
	projectSet     bool
	configDir      string
	configDirSet   bool
	codexHome      string
	codexSet       bool
	claudeConfig   string
	claudeSet      bool
	nowText        string
	nowSet         bool
	sinceText      string
	sinceSet       bool
	days           int
	daysSet        bool
	hooks          []string
	hooksSet       bool
	runtime        string
	runtimeSet     bool
	event          string
	eventSet       bool
	managedBy      string
	managedSet     bool
	capabilityType string
	typeSet        bool
	dryRun         bool
	dryRunSet      bool
	json           bool
	jsonSet        bool
	output         string
	outputSet      bool
	monthly        bool
	monthlySet     bool
	hooksAction    string
	hooksRuntime   string
	dbAction       string
}

func parseFlags(command string, args []string, stderr io.Writer) (parsedFlags, error) {
	fs := flag.NewFlagSet("harness-lint "+command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: harness-lint %s [options]\n", command)
		fmt.Fprintln(stderr, "commands: scan, usage, report, context, stale, doctor, ingest, hooks, db")
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
	fs.IntVar(&result.days, "days", defaultDays(command), "period or stale threshold in days")
	fs.StringVar(&result.runtime, "runtime", "", "runtime (claude, claude-code, or codex)")
	fs.StringVar(&result.capabilityType, "type", "", "usage type (skill, mcp, tool, or agent)")
	fs.StringVar(&result.event, "event", "", "documented runtime hook event name")
	fs.StringVar(&result.managedBy, "managed-by", "", "managed hook ownership marker")
	fs.BoolVar(&result.dryRun, "dry-run", false, "show hook changes without writing configuration")
	fs.BoolVar(&result.json, "json", false, "render stable JSON output")
	fs.StringVar(&result.output, "output", "", "backup destination path")
	fs.BoolVar(&result.monthly, "monthly", false, "include UTC monthly usage evidence")
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
		case "runtime":
			result.runtimeSet = true
		case "event":
			result.eventSet = true
		case "managed-by":
			result.managedSet = true
		case "type":
			result.typeSet = true
		case "now":
			result.nowSet = true
		case "since":
			result.sinceSet = true
		case "days":
			result.daysSet = true
		case "hook-capture":
			result.hooksSet = true
		case "dry-run":
			result.dryRunSet = true
		case "json":
			result.jsonSet = true
		case "output":
			result.outputSet = true
		case "monthly":
			result.monthlySet = true
		}
	})
	if result.days < 0 {
		return parsedFlags{}, errors.New("--days cannot be negative")
	}
	return result, nil
}

// parseCommandArgs extracts the nested hooks action and optional runtime before
// flag parsing. The standard flag package stops at the first positional
// argument, while the public hooks syntax intentionally places those
// positionals next to options.
func parseCommandArgs(command string, args []string) (parsedFlags, []string, error) {
	if command != "hooks" && command != "db" {
		return parsedFlags{}, args, nil
	}
	var positionals []string
	var positionalIndexes []int
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			for next := index + 1; next < len(args); next++ {
				positionals = append(positionals, args[next])
				positionalIndexes = append(positionalIndexes, next)
			}
			break
		}
		if strings.HasPrefix(arg, "-") {
			if !strings.Contains(arg, "=") && consumesValueFlag(arg) {
				index++
			}
			continue
		}
		positionals = append(positionals, arg)
		positionalIndexes = append(positionalIndexes, index)
	}
	if len(positionals) == 0 {
		if command == "db" {
			return parsedFlags{}, nil, errors.New("db action is required: status, check, or backup")
		}
		return parsedFlags{}, nil, errors.New("hooks action is required: status, test, install, or uninstall")
	}
	if len(positionals) > 2 {
		if command == "db" {
			return parsedFlags{}, nil, fmt.Errorf("unexpected db argument(s): %s", strings.Join(positionals[1:], " "))
		}
		return parsedFlags{}, nil, fmt.Errorf("unexpected hooks argument(s): %s", strings.Join(positionals[2:], " "))
	}
	result := parsedFlags{}
	if command == "db" {
		result.dbAction = positionals[0]
	} else {
		result.hooksAction = positionals[0]
	}
	if len(positionals) == 2 {
		if command == "db" {
			return parsedFlags{}, nil, fmt.Errorf("unexpected db argument(s): %s", strings.Join(positionals[1:], " "))
		}
		result.hooksRuntime = positionals[1]
	}
	removed := make(map[int]struct{}, len(positionalIndexes))
	for _, index := range positionalIndexes {
		removed[index] = struct{}{}
	}
	filtered := make([]string, 0, len(args)-len(removed))
	for index, arg := range args {
		if _, found := removed[index]; !found {
			filtered = append(filtered, arg)
		}
	}
	return result, filtered, nil
}

func validateCommandFlags(command string, flags parsedFlags) error {
	switch command {
	case "ingest":
		if flags.homeSet {
			return errors.New("ingest does not accept --home")
		}
		if flags.projectSet {
			return errors.New("ingest does not accept --project")
		}
		if flags.codexSet {
			return errors.New("ingest does not accept --codex-home")
		}
		if flags.claudeSet {
			return errors.New("ingest does not accept --claude-config")
		}
		if flags.sinceSet {
			return errors.New("ingest does not accept --since")
		}
		if flags.daysSet {
			return errors.New("ingest does not accept --days")
		}
		if flags.hooksSet {
			return errors.New("ingest does not accept --hook-capture")
		}
		if flags.nowSet {
			return errors.New("ingest does not accept --now; observed_at comes from the local receive clock")
		}
		if flags.dryRunSet {
			return errors.New("--dry-run is only valid for hooks install or hooks uninstall")
		}
		if flags.jsonSet {
			return errors.New("--json is only valid for hooks status or usage/report/stale")
		}
		if flags.typeSet || flags.monthlySet {
			return errors.New("--type and --monthly are only valid for usage")
		}
		if !flags.runtimeSet || strings.TrimSpace(flags.runtime) == "" {
			return errors.New("ingest requires --runtime claude|codex")
		}
		if _, err := ingestRuntime(flags.runtime); err != nil {
			return err
		}
	case "hooks":
		if flags.dbSet && flags.hooksAction != "test" {
			return errors.New("hooks does not accept --db")
		}
		if flags.projectSet {
			return errors.New("hooks does not accept --project")
		}
		if flags.configDirSet && flags.hooksAction != "test" {
			return errors.New("hooks does not accept --config-dir")
		}
		if flags.sinceSet {
			return errors.New("hooks does not accept --since")
		}
		if flags.daysSet {
			return errors.New("hooks does not accept --days")
		}
		if flags.hooksSet {
			return errors.New("hooks does not accept --hook-capture")
		}
		if flags.eventSet || flags.managedSet {
			return errors.New("--event and --managed-by are only valid for ingest")
		}
		if flags.runtimeSet && flags.hooksRuntime != "" {
			return errors.New("hooks runtime must be positional or --runtime, not both")
		}
		if flags.hooksRuntime != "" {
			if _, err := hookRuntime(flags.hooksRuntime); err != nil {
				return err
			}
		} else if flags.runtimeSet {
			if _, err := hookRuntime(flags.runtime); err != nil {
				return err
			}
		}
		switch flags.hooksAction {
		case "status":
			if flags.dryRunSet {
				return errors.New("--dry-run is only valid for hooks install or hooks uninstall")
			}
		case "install", "uninstall":
			if flags.jsonSet {
				return errors.New("--json is only valid for hooks status")
			}
		case "test":
			if flags.dryRunSet {
				return errors.New("--dry-run is only valid for hooks install or hooks uninstall")
			}
			if flags.jsonSet {
				return errors.New("hooks test does not support --json")
			}
		default:
			return fmt.Errorf("unknown hooks action %q (want status, test, install, or uninstall)", flags.hooksAction)
		}
		if flags.typeSet || flags.monthlySet {
			return errors.New("--type and --monthly are only valid for usage")
		}
	case "usage":
		if flags.homeSet {
			return errors.New("usage does not accept --home")
		}
		if flags.projectSet {
			return errors.New("usage does not accept --project")
		}
		if flags.codexSet {
			return errors.New("usage does not accept --codex-home")
		}
		if flags.claudeSet {
			return errors.New("usage does not accept --claude-config")
		}
		if flags.eventSet || flags.managedSet || flags.dryRunSet || flags.hooksSet || flags.sinceSet {
			return errors.New("usage does not accept ingest or hooks-only flags")
		}
		if flags.runtimeSet {
			if _, err := usageRuntime(flags.runtime); err != nil {
				return err
			}
		}
		if flags.typeSet {
			if _, err := usageType(flags.capabilityType); err != nil {
				return err
			}
		}
		if flags.days <= 0 {
			return errors.New("usage --days must be greater than zero")
		}
	case "db":
		if flags.homeSet || flags.projectSet || flags.codexSet || flags.claudeSet {
			return errors.New("db does not accept runtime or project configuration flags")
		}
		if flags.sinceSet || flags.eventSet || flags.managedSet || flags.dryRunSet || flags.hooksSet || flags.typeSet || flags.monthlySet || flags.daysSet {
			return errors.New("db does not accept ingest, hooks, usage, or report flags")
		}
		switch flags.dbAction {
		case "status", "check":
			if flags.outputSet {
				return errors.New("--output is only valid for db backup")
			}
		case "backup":
			if flags.jsonSet {
				return errors.New("db backup does not support --json")
			}
		default:
			return fmt.Errorf("unknown db action %q (want status, check, or backup)", flags.dbAction)
		}
	default:
		if flags.runtimeSet || flags.eventSet || flags.managedSet || flags.dryRunSet || flags.typeSet || flags.monthlySet || flags.outputSet || (flags.jsonSet && command != "report" && command != "stale") {
			return fmt.Errorf("ingest or hooks flags are not valid for %s", command)
		}
	}
	return nil
}

type commandConfig struct {
	dbPath          string
	home            string
	projectRoot     string
	currentDir      string
	codexHome       string
	claudeConfig    string
	hooks           []string
	since           time.Time
	now             time.Time
	days            int
	json            bool
	dataDir         string
	lookPath        func(string) (string, error)
	versionResolver compatibility.ExecutableResolver
	versionRunner   compatibility.CommandRunner
}

// resolveDBConfig is intentionally narrower than resolveConfig. Database
// diagnostics and backups do not inspect HOME, project trees, or runtime
// configuration, and backup's data root is explicitly injectable.
func resolveDBConfig(options Options, flags parsedFlags) (commandConfig, error) {
	currentDir, err := resolveCurrentDirectory(options)
	if err != nil {
		return commandConfig{}, err
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
			return commandConfig{}, errors.New("resolve user config directory")
		}
		dbPath = filepath.Join(base, "harness-lint", "harness-lint.db")
	}
	if strings.TrimSpace(dbPath) == "" {
		return commandConfig{}, errors.New("database path is empty")
	}
	if dbPath != ":memory:" {
		dbPath, err = absolutePath(dbPath, currentDir)
		if err != nil {
			return commandConfig{}, errors.New("resolve database path")
		}
	}
	now := time.Now()
	if options.Now != nil {
		now = options.Now()
	}
	if flags.nowText != "" {
		now, err = time.Parse(time.RFC3339Nano, flags.nowText)
		if err != nil {
			return commandConfig{}, errors.New("parse --now")
		}
	}
	if now.IsZero() {
		return commandConfig{}, errors.New("observation clock returned zero time")
	}
	dataDir := ""
	if flags.dbAction == "backup" {
		dataDir = options.DataDir
		if dataDir == "" {
			dataDir, err = userDataDirectory()
			if err != nil {
				return commandConfig{}, errors.New("resolve user data directory")
			}
		}
		dataDir, err = absolutePath(dataDir, currentDir)
		if err != nil {
			return commandConfig{}, errors.New("resolve data directory")
		}
	}
	return commandConfig{dbPath: dbPath, currentDir: currentDir, now: now.UTC(), json: flags.json, dataDir: dataDir}, nil
}

func userDataDirectory() (string, error) {
	if value := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); value != "" {
		return value, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support"), nil
	}
	return filepath.Join(home, ".local", "share"), nil
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
		dbPath:          dbPath,
		home:            home,
		projectRoot:     projectRoot,
		currentDir:      currentDir,
		codexHome:       codexHome,
		claudeConfig:    claudeConfig,
		hooks:           hooks,
		since:           since,
		now:             now,
		days:            flags.days,
		json:            flags.json,
		lookPath:        options.LookPath,
		versionResolver: options.VersionResolver,
		versionRunner:   options.VersionRunner,
	}, nil
}

// resolveIngestConfig is intentionally narrower than resolveConfig. A hook
// receiver must not resolve a home/project tree or construct runtime adapters;
// it only needs a local database path and the receive clock.
func resolveIngestConfig(options Options, flags parsedFlags) (commandConfig, error) {
	clock := options.Now
	now := time.Now()
	if clock != nil {
		now = clock()
	}
	if now.IsZero() {
		return commandConfig{}, errors.New("observation clock returned zero time")
	}

	dbPath := ""
	if flags.dbSet {
		dbPath = flags.dbPath
	} else if options.ConfigDir != "" {
		dbPath = filepath.Join(options.ConfigDir, "harness-lint", "harness-lint.db")
	} else if flags.configDirSet {
		dbPath = filepath.Join(flags.configDir, "harness-lint", "harness-lint.db")
	} else {
		base, err := os.UserConfigDir()
		if err != nil {
			return commandConfig{}, fmt.Errorf("resolve user config directory: %w", err)
		}
		dbPath = filepath.Join(base, "harness-lint", "harness-lint.db")
	}
	if strings.TrimSpace(dbPath) == "" {
		return commandConfig{}, errors.New("database path is empty")
	}
	return commandConfig{
		dbPath:   dbPath,
		now:      now.UTC(),
		days:     flags.days,
		lookPath: options.LookPath,
	}, nil
}

// resolveUsageConfig is intentionally narrower than resolveConfig. Usage is
// a store-history query and must not inspect a live HOME, project tree, or
// runtime configuration merely to render local aggregates.
func resolveUsageConfig(options Options, flags parsedFlags) (commandConfig, error) {
	currentDir, err := resolveCurrentDirectory(options)
	if err != nil {
		return commandConfig{}, err
	}
	now := time.Now()
	if options.Now != nil {
		now = options.Now()
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
	if strings.TrimSpace(dbPath) == "" {
		return commandConfig{}, errors.New("database path is empty")
	}
	if dbPath != ":memory:" {
		dbPath, err = absolutePath(dbPath, currentDir)
		if err != nil {
			return commandConfig{}, fmt.Errorf("resolve database path: %w", err)
		}
	}
	return commandConfig{dbPath: dbPath, currentDir: currentDir, now: now.UTC(), days: flags.days, json: flags.json}, nil
}

// resolveHooksConfig resolves only runtime hook roots and the injected clock.
// In particular, explicit --claude-config/--codex-home overrides do not cause
// an otherwise unnecessary HOME lookup, which keeps status/install tests and
// automation hermetic.
func resolveHooksConfig(options Options, flags parsedFlags) (commandConfig, error) {
	currentDir, err := resolveCurrentDirectory(options)
	if err != nil {
		return commandConfig{}, err
	}
	home := ""
	needClaude, needCodex := hookConfigNeedsHome(flags)
	if flags.homeSet || options.Home != "" || needClaude || needCodex {
		home, err = resolveHome(options, flags, currentDir)
		if err != nil {
			return commandConfig{}, err
		}
	}
	claudeConfig := ""
	if flags.claudeSet {
		claudeConfig = flags.claudeConfig
	} else if home != "" {
		claudeConfig = filepath.Join(home, ".claude")
	}
	codexHome := ""
	if flags.codexSet {
		codexHome = flags.codexHome
	} else if home != "" {
		codexHome = filepath.Join(home, ".codex")
	}
	claudeConfig, err = optionalAbsolutePath(claudeConfig, currentDir)
	if err != nil {
		return commandConfig{}, fmt.Errorf("resolve Claude config: %w", err)
	}
	codexHome, err = optionalAbsolutePath(codexHome, currentDir)
	if err != nil {
		return commandConfig{}, fmt.Errorf("resolve Codex home: %w", err)
	}
	now := time.Now()
	if options.Now != nil {
		now = options.Now()
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
	return commandConfig{
		home:            home,
		currentDir:      currentDir,
		codexHome:       codexHome,
		claudeConfig:    claudeConfig,
		now:             now.UTC(),
		lookPath:        options.LookPath,
		versionResolver: options.VersionResolver,
		versionRunner:   options.VersionRunner,
	}, nil
}

// resolveHooksTestConfig adds the isolated database selection needed by the
// read-only hooks test command. Hook status/install/uninstall intentionally do
// not open a database.
func resolveHooksTestConfig(options Options, flags parsedFlags) (commandConfig, error) {
	if flags.dbSet && strings.TrimSpace(flags.dbPath) == "" {
		return commandConfig{}, errors.New("database path is empty")
	}
	config, err := resolveHooksConfig(options, flags)
	if err != nil {
		return commandConfig{}, err
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
		dbPath, err = absolutePath(dbPath, config.currentDir)
		if err != nil {
			return commandConfig{}, fmt.Errorf("resolve database path: %w", err)
		}
	}
	config.dbPath = dbPath
	return config, nil
}

func hookConfigNeedsHome(flags parsedFlags) (bool, bool) {
	needClaude := !flags.claudeSet
	needCodex := !flags.codexSet
	selected := strings.TrimSpace(flags.hooksRuntime)
	if selected == "" && flags.runtimeSet {
		selected = strings.TrimSpace(flags.runtime)
	}
	if selected == "" {
		return needClaude, needCodex
	}
	switch strings.ToLower(selected) {
	case "claude", "claude-code":
		return needClaude, false
	case "codex":
		return false, needCodex
	default:
		return needClaude, needCodex
	}
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
			return "", nil, unknownCommandError(args[0])
		}
		return "", nil, errors.New("a command is required: scan, usage, report, context, stale, doctor, ingest, hooks, or db")
	}
	command := args[commandIndex]
	commandArgs := append([]string(nil), args[:commandIndex]...)
	commandArgs = append(commandArgs, args[commandIndex+1:]...)
	return command, commandArgs, nil
}

func unknownCommandError(command string) error {
	return fmt.Errorf("unknown command %q (want scan, usage, report, context, stale, doctor, ingest, hooks, or db)", command)
}

func consumesValueFlag(arg string) bool {
	switch arg {
	case "-db", "--db", "-home", "--home", "-project", "--project", "-config-dir", "--config-dir", "-codex-home", "--codex-home", "-claude-config", "--claude-config", "-now", "--now", "-since", "--since", "-days", "--days", "-hook-capture", "--hook-capture", "-runtime", "--runtime", "-type", "--type", "-event", "--event", "-managed-by", "--managed-by", "-output", "--output":
		return true
	default:
		return false
	}
}

func writeUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: harness-lint <scan|usage|report|context|stale|doctor|ingest|hooks|db> [options]")
	fmt.Fprintln(w, "commands: scan, usage, report, context, stale, doctor, ingest, hooks, db")
	fmt.Fprintln(w, "use harness-lint <command> --help for command options")
}

func writeCommandUsage(w io.Writer, command string) {
	if command == "ingest" {
		fmt.Fprintln(w, "usage: harness-lint ingest [options]")
		fmt.Fprintln(w, "options:")
		fmt.Fprintln(w, "  --db PATH                  SQLite database path")
		fmt.Fprintln(w, "  --config-dir PATH          default database configuration directory")
		fmt.Fprintln(w, "  --runtime claude|codex     named hook runtime")
		fmt.Fprintln(w, "  --event EVENT              documented hook event (optional)")
		fmt.Fprintln(w, "  --managed-by harness-lint-hooks/v1")
		fmt.Fprintln(w, "  stdin                      one metadata-only JSON hook document")
		return
	}
	if command == "usage" {
		fmt.Fprintln(w, "usage: harness-lint usage [options]")
		fmt.Fprintln(w, "options:")
		fmt.Fprintln(w, "  --db PATH                  SQLite database path")
		fmt.Fprintln(w, "  --days N                   closed UTC period length (default 90; positive)")
		fmt.Fprintln(w, "  --runtime claude|claude-code|codex")
		fmt.Fprintln(w, "  --type skill|mcp|tool|agent")
		fmt.Fprintln(w, "  --monthly                  include UTC monthly evidence")
		fmt.Fprintln(w, "  --json                     stable JSON output")
		fmt.Fprintln(w, "  --now RFC3339              generated-at clock")
		return
	}
	if command == "hooks" {
		fmt.Fprintln(w, "usage: harness-lint hooks <status|test|install|uninstall> [claude|codex] [options]")
		fmt.Fprintln(w, "options:")
		fmt.Fprintln(w, "  --json                     stable JSON output (status only)")
		fmt.Fprintln(w, "  --dry-run                  show install/uninstall changes without writing")
		fmt.Fprintln(w, "  --home PATH                synthetic user home")
		fmt.Fprintln(w, "  --codex-home PATH          Codex configuration root")
		fmt.Fprintln(w, "  --claude-config PATH       Claude configuration root")
		fmt.Fprintln(w, "  --now RFC3339              generated-at clock")
		return
	}
	if command == "db" {
		fmt.Fprintln(w, "usage: harness-lint db <status|check|backup> [options]")
		fmt.Fprintln(w, "options:")
		fmt.Fprintln(w, "  --db PATH                  SQLite database path")
		fmt.Fprintln(w, "  --config-dir PATH          default database configuration directory")
		fmt.Fprintln(w, "  --json                     stable JSON output (status/check only)")
		fmt.Fprintln(w, "  --output PATH              backup destination (backup only)")
		fmt.Fprintln(w, "  --now RFC3339              generated-at clock")
		return
	}
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
	fmt.Fprintf(w, "  --days N                   stale threshold in days (default %d)\n", defaultDays(command))
	fmt.Fprintln(w, "  --now RFC3339              observation time")
	if command == "report" || command == "stale" {
		fmt.Fprintln(w, "  --json                     stable JSON output")
	}
}

func defaultDays(command string) int {
	if command == "usage" {
		return defaultUsageDays
	}
	return defaultStaleDays
}

func usageRuntime(value string) (domain.Runtime, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "claude", "claude-code":
		return domain.RuntimeClaudeCode, nil
	case "codex":
		return domain.RuntimeCodex, nil
	default:
		return domain.RuntimeUnknown, errors.New("unknown usage runtime; want claude, claude-code, or codex")
	}
}

func usageType(value string) (domain.CapabilityType, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "skill":
		return domain.CapabilitySkill, nil
	case "mcp":
		return domain.CapabilityMCPServer, nil
	case "tool":
		return domain.CapabilityTool, nil
	case "agent":
		return domain.CapabilityAgent, nil
	default:
		return domain.CapabilityUnknown, errors.New("unknown usage type; want skill, mcp, tool, or agent")
	}
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
