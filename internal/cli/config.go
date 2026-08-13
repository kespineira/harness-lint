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
			return "", nil, unknownCommandError(args[0])
		}
		return "", nil, errors.New("a command is required: scan, report, context, stale, or doctor")
	}
	command := args[commandIndex]
	commandArgs := append([]string(nil), args[:commandIndex]...)
	commandArgs = append(commandArgs, args[commandIndex+1:]...)
	return command, commandArgs, nil
}

func unknownCommandError(command string) error {
	return fmt.Errorf("unknown command %q (want scan, report, context, stale, or doctor)", command)
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
