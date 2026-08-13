// Package cli implements the small, local, read-only harness-lint command
// line application. It intentionally keeps argument parsing, orchestration,
// and presentation at the edge; adapters, analysis, and persistence remain
// independently testable packages.
package cli

import (
	"context"
	"io"
	"time"
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
		return unknownCommandError(command)
	}
}
