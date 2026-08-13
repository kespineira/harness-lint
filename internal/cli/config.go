package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultStaleDays = 60

var supportedCommands = map[string]struct{}{
	"ingest":  {},
	"hooks":   {},
	"scan":    {},
	"report":  {},
	"context": {},
	"stale":   {},
	"doctor":  {},
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
	nowSet       bool
	sinceText    string
	sinceSet     bool
	days         int
	daysSet      bool
	hooks        []string
	hooksSet     bool
	runtime      string
	runtimeSet   bool
	event        string
	eventSet     bool
	managedBy    string
	managedSet   bool
	dryRun       bool
	dryRunSet    bool
	json         bool
	jsonSet      bool
	hooksAction  string
	hooksRuntime string
}

func parseFlags(command string, args []string, stderr io.Writer) (parsedFlags, error) {
	fs := flag.NewFlagSet("harness-lint "+command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: harness-lint %s [options]\n", command)
		fmt.Fprintln(stderr, "commands: scan, report, context, stale, doctor, ingest, hooks")
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
	fs.StringVar(&result.runtime, "runtime", "", "runtime (claude, claude-code, or codex)")
	fs.StringVar(&result.event, "event", "", "documented runtime hook event name")
	fs.StringVar(&result.managedBy, "managed-by", "", "managed hook ownership marker")
	fs.BoolVar(&result.dryRun, "dry-run", false, "show hook changes without writing configuration")
	fs.BoolVar(&result.json, "json", false, "render hook status as stable JSON")
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
	if command != "hooks" {
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
		return parsedFlags{}, nil, errors.New("hooks action is required: status, install, or uninstall")
	}
	if len(positionals) > 2 {
		return parsedFlags{}, nil, fmt.Errorf("unexpected hooks argument(s): %s", strings.Join(positionals[2:], " "))
	}
	result := parsedFlags{hooksAction: positionals[0]}
	if len(positionals) == 2 {
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
			return errors.New("--json is only valid for hooks status")
		}
		if !flags.runtimeSet || strings.TrimSpace(flags.runtime) == "" {
			return errors.New("ingest requires --runtime claude|codex")
		}
		if _, err := ingestRuntime(flags.runtime); err != nil {
			return err
		}
	case "hooks":
		if flags.dbSet {
			return errors.New("hooks does not accept --db")
		}
		if flags.projectSet {
			return errors.New("hooks does not accept --project")
		}
		if flags.configDirSet {
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
		default:
			return fmt.Errorf("unknown hooks action %q (want status, install, or uninstall)", flags.hooksAction)
		}
	default:
		if flags.runtimeSet || flags.eventSet || flags.managedSet || flags.dryRunSet || flags.jsonSet {
			return fmt.Errorf("ingest or hooks flags are not valid for %s", command)
		}
	}
	return nil
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
		home:         home,
		codexHome:    codexHome,
		claudeConfig: claudeConfig,
		now:          now.UTC(),
		lookPath:     options.LookPath,
	}, nil
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
		return "", nil, errors.New("a command is required: scan, report, context, stale, doctor, ingest, or hooks")
	}
	command := args[commandIndex]
	commandArgs := append([]string(nil), args[:commandIndex]...)
	commandArgs = append(commandArgs, args[commandIndex+1:]...)
	return command, commandArgs, nil
}

func unknownCommandError(command string) error {
	return fmt.Errorf("unknown command %q (want scan, report, context, stale, doctor, ingest, or hooks)", command)
}

func consumesValueFlag(arg string) bool {
	switch arg {
	case "-db", "--db", "-home", "--home", "-project", "--project", "-config-dir", "--config-dir", "-codex-home", "--codex-home", "-claude-config", "--claude-config", "-now", "--now", "-since", "--since", "-days", "--days", "-hook-capture", "--hook-capture", "-runtime", "--runtime", "-event", "--event", "-managed-by", "--managed-by":
		return true
	default:
		return false
	}
}

func writeUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: harness-lint <scan|report|context|stale|doctor|ingest|hooks> [options]")
	fmt.Fprintln(w, "commands: scan, report, context, stale, doctor, ingest, hooks")
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
	if command == "hooks" {
		fmt.Fprintln(w, "usage: harness-lint hooks <status|install|uninstall> [claude|codex] [options]")
		fmt.Fprintln(w, "options:")
		fmt.Fprintln(w, "  --json                     stable JSON output (status only)")
		fmt.Fprintln(w, "  --dry-run                  show install/uninstall changes without writing")
		fmt.Fprintln(w, "  --home PATH                synthetic user home")
		fmt.Fprintln(w, "  --codex-home PATH          Codex configuration root")
		fmt.Fprintln(w, "  --claude-config PATH       Claude configuration root")
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
