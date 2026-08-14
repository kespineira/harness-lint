// Package cli implements the small, local harness-lint command line
// application. It intentionally keeps argument parsing, orchestration, and
// presentation at the edge; adapters, analysis, persistence, and hook
// configuration management remain independently testable packages.
package cli

import (
	"context"
	"io"
	"time"

	"github.com/kespineira/harness-lint/internal/compatibility"
)

// Options injects process-dependent values for deterministic tests. Non-empty
// Home, CWD, and ProjectRoot values override the live defaults.
type Options struct {
	Home        string
	CWD         string
	ProjectRoot string
	ConfigDir   string
	Now         func() time.Time
	LookPath    func(string) (string, error)

	// VersionResolver and VersionRunner are used only by low-frequency
	// doctor/hooks-test diagnostics. They are deliberately absent from the
	// ingest configuration path so receiving a hook can never execute a
	// runtime command.
	VersionResolver compatibility.ExecutableResolver
	VersionRunner   compatibility.CommandRunner
}

// Execute runs one command using process standard inputs and outputs passed
// explicitly by the caller. It is the public seam used by cmd/harness-lint
// and end-to-end tests.
func Execute(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return ExecuteWithOptions(Options{}, args, stdin, stdout, stderr)
}

// ExecuteWithOptions is Execute with injected home, current directory, clock,
// and executable lookup values. The ingest command consumes only its one
// metadata-only JSON document from stdin; other commands leave stdin alone.
func ExecuteWithOptions(options Options, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
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

	nested, parseArgs, err := parseCommandArgs(command, commandArgs)
	if err != nil {
		return err
	}
	parsed, err := parseFlags(command, parseArgs, stderr)
	if err != nil {
		return err
	}
	parsed.hooksAction = nested.hooksAction
	parsed.hooksRuntime = nested.hooksRuntime
	parsed.dbAction = nested.dbAction
	if err := validateCommandFlags(command, parsed); err != nil {
		return err
	}
	ctx := context.Background()
	if command == "ingest" {
		config, err := resolveIngestConfig(options, parsed)
		if err != nil {
			return err
		}
		return runIngest(ctx, config, parsed, stdin)
	}
	var config commandConfig
	if command == "usage" {
		config, err = resolveUsageConfig(options, parsed)
	} else if command == "db" {
		config, err = resolveDBConfig(options, parsed)
	} else if command == "hooks" {
		if parsed.hooksAction == "test" {
			config, err = resolveHooksTestConfig(options, parsed)
		} else {
			config, err = resolveHooksConfig(options, parsed)
		}
	} else {
		config, err = resolveConfig(options, parsed)
	}
	if err != nil {
		return err
	}

	switch command {
	case "hooks":
		return runHooks(ctx, config, parsed, stdout)
	case "usage":
		return runUsage(ctx, config, parsed, stdout)
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
	case "db":
		return runDatabase(ctx, config, parsed, stdout)
	default:
		return unknownCommandError(command)
	}
}
