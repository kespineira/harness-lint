package cli

import (
	"fmt"
	"io"
)

// The help text is kept in one small command-owned file so argument parsing
// and command execution do not need to know how the navigation tree is
// described. Help is intentionally plain text: it is frequently redirected
// and should never depend on terminal color policy.
func writeRootHelp(w io.Writer) {
	fmt.Fprintln(w, "Inspect local AI harness capabilities, usage, and health")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  harness-lint <command> [options]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  scan       Discover capabilities and import local usage evidence")
	fmt.Fprintln(w, "  report     Summarize capability usage and cleanup candidates")
	fmt.Fprintln(w, "  explain    Explain one current capability's evidence and classification")
	fmt.Fprintln(w, "  context    Estimate configured and on-load context footprint")
	fmt.Fprintln(w, "  usage      Rank observed usage over a closed UTC period")
	fmt.Fprintln(w, "  stale      Show capabilities classified stale by observed recency")
	fmt.Fprintln(w, "  doctor     Find runtime configuration and discovery problems")
	fmt.Fprintln(w, "  hooks      Inspect or manage runtime hook configuration")
	fmt.Fprintln(w, "  db         Inspect, check, or back up the local database")
	fmt.Fprintln(w, "  ingest     Receive one bounded metadata-only hook document")
	fmt.Fprintln(w, "  version    Print build version metadata")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Global options:")
	fmt.Fprintln(w, "  --color auto|always|never  Control ANSI status styling (default auto)")
	fmt.Fprintln(w, "  --help                     Show command help")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Run `harness-lint <command> --help` for details.")
}

func writeGroupHelp(w io.Writer, command string) {
	switch command {
	case "hooks":
		fmt.Fprintln(w, "Manage runtime hooks")
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Usage:")
		fmt.Fprintln(w, "  harness-lint hooks <command> [runtime] [options]")
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Commands:")
		fmt.Fprintln(w, "  status      Show managed hook state, executable resolution, and warnings")
		fmt.Fprintln(w, "  test        Check local hook health without invoking a runtime")
		fmt.Fprintln(w, "  install     Add harness-lint handlers to runtime configuration")
		fmt.Fprintln(w, "  uninstall   Remove only harness-lint-owned handlers")
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Runtime may be claude or codex; omit it to inspect both.")
		fmt.Fprintln(w, "Run `harness-lint hooks <command> --help` for details.")
	case "db":
		fmt.Fprintln(w, "Manage the local harness-lint database")
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Usage:")
		fmt.Fprintln(w, "  harness-lint db <command> [options]")
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Commands:")
		fmt.Fprintln(w, "  status      Show database path, size, schema, and history metadata")
		fmt.Fprintln(w, "  check       Run read-only integrity checks")
		fmt.Fprintln(w, "  backup      Create a consistent SQLite backup")
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Run `harness-lint db <command> --help` for details.")
	default:
		writeCommandHelp(w, command)
	}
}

func writeActionHelp(w io.Writer, command, action string) {
	if command == "hooks" {
		writeHooksActionHelp(w, action)
		return
	}
	if command == "db" {
		writeDatabaseActionHelp(w, action)
		return
	}
	writeCommandHelp(w, command)
}

func writeHooksActionHelp(w io.Writer, action string) {
	var description string
	switch action {
	case "status":
		description = "Show managed hook state, executable resolution, and bounded warnings."
	case "test":
		description = "Check local hook health without invoking Claude Code or Codex."
	case "install":
		description = "Install harness-lint-owned handlers without starting a configured command."
	case "uninstall":
		description = "Remove harness-lint-owned handlers while preserving unrelated hooks."
	default:
		writeGroupHelp(w, "hooks")
		return
	}
	fmt.Fprintln(w, description)
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintf(w, "  harness-lint hooks %s [claude|codex] [options]\n", action)
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Options:")
	fmt.Fprintln(w, "  --runtime RUNTIME           Runtime selector (or use positional runtime)")
	fmt.Fprintln(w, "  --home PATH                Synthetic user home")
	fmt.Fprintln(w, "  --codex-home PATH          Codex configuration root")
	fmt.Fprintln(w, "  --claude-config PATH       Claude configuration root")
	fmt.Fprintln(w, "  --color auto|always|never  Status color policy")
	fmt.Fprintln(w, "  --verbose                  Include matcher and delivery details")
	fmt.Fprintln(w, "  --now RFC3339              Fixed observation clock")
	if action == "status" {
		fmt.Fprintln(w, "  --json                     Stable JSON status output")
	}
	if action == "test" {
		fmt.Fprintln(w, "  --db PATH                  SQLite database path")
	}
	if action == "install" || action == "uninstall" {
		fmt.Fprintln(w, "  --dry-run                  Preview changes without writing")
	}
}

func writeDatabaseActionHelp(w io.Writer, action string) {
	var description string
	switch action {
	case "status":
		description = "Show bounded database metadata without running integrity checks."
	case "check":
		description = "Run read-only SQLite quick, foreign-key, and schema checks."
	case "backup":
		description = "Create a consistent backup without overwriting an existing file."
	default:
		writeGroupHelp(w, "db")
		return
	}
	fmt.Fprintln(w, description)
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintf(w, "  harness-lint db %s [options]\n", action)
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Options:")
	fmt.Fprintln(w, "  --db PATH                  SQLite database path")
	fmt.Fprintln(w, "  --config-dir PATH          Default database configuration directory")
	fmt.Fprintln(w, "  --color auto|always|never  Status color policy")
	fmt.Fprintln(w, "  --verbose                  Include diagnostic details")
	fmt.Fprintln(w, "  --now RFC3339              Fixed observation clock")
	if action == "status" || action == "check" {
		fmt.Fprintln(w, "  --json                     Stable JSON output")
	}
	if action == "backup" {
		fmt.Fprintln(w, "  --output PATH              Exclusive backup destination")
	}
}

func writeCommandHelp(w io.Writer, command string) {
	// Keep the established command help wording for protected renderers while
	// exposing the shared color policy on commands that produce human output.
	switch command {
	case "version":
		fmt.Fprintln(w, "usage: harness-lint version")
		fmt.Fprintln(w, "prints the semantic version, commit, and build date")
	case "ingest":
		fmt.Fprintln(w, "usage: harness-lint ingest [options]")
		fmt.Fprintln(w, "options:")
		fmt.Fprintln(w, "  --db PATH                  SQLite database path")
		fmt.Fprintln(w, "  --config-dir PATH          default database configuration directory")
		fmt.Fprintln(w, "  --runtime claude|codex     named hook runtime")
		fmt.Fprintln(w, "  --event EVENT              documented hook event (optional)")
		fmt.Fprintln(w, "  --managed-by harness-lint-hooks/v1")
		fmt.Fprintln(w, "  stdin                      one metadata-only JSON hook document")
	case "usage":
		writeLeafCommandHelp(w,
			"Rank observed capability usage over a closed UTC period.",
			"harness-lint usage [options]",
			[]string{
				"--db PATH                  SQLite database path",
				"--days N                   Closed UTC period length (default 90)",
				"--runtime claude|codex     Filter by runtime",
				"--type skill|mcp|tool|agent",
				"--monthly                  Include UTC monthly totals",
				"--json                     Stable machine-readable JSON output",
				"--now RFC3339              Observation clock",
				"--color auto|always|never  Status color policy",
			})
	case "report":
		writeLeafCommandHelp(w,
			"Summarize capability usage and cleanup candidates.",
			"harness-lint report [options]",
			[]string{
				"--db PATH                  SQLite database path",
				"--days N                   Stale threshold in days (default 60)",
				"--all                      Show every current capability",
				"--verbose                  Include exact evidence and coverage details",
				"--json                     Stable machine-readable JSON output",
				"--now RFC3339              Observation clock",
				"--color auto|always|never  Status color policy",
			})
	case "explain":
		writeLeafCommandHelp(w,
			"Explain why one current capability received its classification.",
			"harness-lint explain <name> [options]",
			[]string{
				"--db PATH                  SQLite database path",
				"--days N                   Stale threshold in days (default 60)",
				"--runtime claude|codex     Narrow an ambiguous name by runtime",
				"--type TYPE                Narrow by capability type",
				"--scope SCOPE              Narrow by global, user, project, or session scope",
				"--verbose                  Include exact evidence and coverage details",
				"--now RFC3339              Observation clock",
				"--color auto|always|never  Status color policy",
			})
	case "scan":
		writeLeafCommandHelp(w,
			"Discover capabilities and import local usage evidence.",
			"harness-lint scan [options]",
			[]string{
				"--db PATH                  SQLite database path",
				"--home PATH                Synthetic user home",
				"--project PATH             Project root",
				"--config-dir PATH          Default database configuration directory",
				"--codex-home PATH          Codex configuration root",
				"--claude-config PATH       Claude configuration root",
				"--hook-capture PATH        Repeatable metadata-only hook capture path",
				"--since RFC3339            Inclusive usage-import boundary",
				"--verbose                  Include inventory recording details",
				"--now RFC3339              Observation clock",
				"--color auto|always|never  Status color policy",
			})
	case "context":
		writeLeafCommandHelp(w,
			"Estimate configured and on-load context footprint.",
			"harness-lint context [options]",
			[]string{
				"--db PATH                  SQLite database path",
				"--now RFC3339              Observation clock",
				"--color auto|always|never  Status color policy",
			})
	case "stale":
		writeLeafCommandHelp(w,
			"Show only capabilities classified STALE under the selected threshold.",
			"harness-lint stale [options]",
			[]string{
				"--db PATH                  SQLite database path",
				"--days N                   Stale threshold in days (default 60)",
				"--verbose                  Include exact stale evidence",
				"--json                     Stable machine-readable JSON output",
				"--now RFC3339              Observation clock",
				"--color auto|always|never  Status color policy",
			})
	case "doctor":
		writeLeafCommandHelp(w,
			"Find runtime configuration and discovery problems.",
			"harness-lint doctor [options]",
			[]string{
				"--home PATH                Synthetic user home",
				"--project PATH             Project root",
				"--codex-home PATH          Codex configuration root",
				"--claude-config PATH       Claude configuration root",
				"--verbose                  Include compatibility details",
				"--now RFC3339              Observation clock",
				"--color auto|always|never  Status color policy",
			})
	default:
		fmt.Fprintf(w, "usage: harness-lint %s [options]\n", command)
	}
}

func writeLeafCommandHelp(w io.Writer, description, usage string, options []string) {
	fmt.Fprintln(w, description)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintf(w, "  %s\n", usage)
	if len(options) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Options:")
	for _, option := range options {
		fmt.Fprintf(w, "  %s\n", option)
	}
}
