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
	fmt.Fprintln(w, "  report     Summarize current inventory and observed usage")
	fmt.Fprintln(w, "  explain    Explain one current capability's evidence and classification")
	fmt.Fprintln(w, "  context    Explain configured and on-load context measurements")
	fmt.Fprintln(w, "  usage      Query usage history over a closed UTC period")
	fmt.Fprintln(w, "  stale      Classify current capabilities by observed recency")
	fmt.Fprintln(w, "  doctor     Inspect runtime discovery findings and compatibility")
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
		fmt.Fprintln(w, "  --json                    Stable JSON status output")
	}
	if action == "test" {
		fmt.Fprintln(w, "  --db PATH                 SQLite database path")
	}
	if action == "install" || action == "uninstall" {
		fmt.Fprintln(w, "  --dry-run                 Preview changes without writing")
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
	fmt.Fprintln(w, "  --db PATH                 SQLite database path")
	fmt.Fprintln(w, "  --config-dir PATH        Default database configuration directory")
	fmt.Fprintln(w, "  --color auto|always|never Status color policy")
	fmt.Fprintln(w, "  --verbose                Include diagnostic details")
	fmt.Fprintln(w, "  --now RFC3339             Fixed observation clock")
	if action == "status" || action == "check" {
		fmt.Fprintln(w, "  --json                   Stable JSON output")
	}
	if action == "backup" {
		fmt.Fprintln(w, "  --output PATH             Exclusive backup destination")
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
		fmt.Fprintln(w, "usage: harness-lint usage [options]")
		fmt.Fprintln(w, "query usage history over a closed UTC period")
		fmt.Fprintln(w, "options:")
		fmt.Fprintln(w, "  --db PATH                  SQLite database path")
		fmt.Fprintln(w, "  --days N                   closed UTC period length (default 90; positive)")
		fmt.Fprintln(w, "  --runtime claude|claude-code|codex")
		fmt.Fprintln(w, "  --type skill|mcp|tool|agent")
		fmt.Fprintln(w, "  --monthly                  include UTC monthly evidence")
		fmt.Fprintln(w, "  --json                     stable JSON output")
		fmt.Fprintln(w, "  --now RFC3339              generated-at clock")
	case "report":
		fmt.Fprintln(w, "usage: harness-lint report [options]")
		fmt.Fprintln(w, "summarize current inventory and observed usage")
		fmt.Fprintln(w, "options:")
		fmt.Fprintln(w, "  --db PATH                  SQLite database path")
		fmt.Fprintln(w, "  --days N                   stale threshold in days (default 60)")
		fmt.Fprintln(w, "  --now RFC3339              observation time")
		fmt.Fprintln(w, "  --all                      show every current capability")
		fmt.Fprintln(w, "  --verbose                  include exact evidence and coverage details")
		fmt.Fprintln(w, "  --json                     stable JSON output (human options do not alter DTOs)")
		fmt.Fprintln(w, "  --color auto|always|never  status color policy")
	case "explain":
		fmt.Fprintln(w, "usage: harness-lint explain <name> [options]")
		fmt.Fprintln(w, "explain one current capability using conservative stored evidence")
		fmt.Fprintln(w, "options:")
		fmt.Fprintln(w, "  --db PATH                  SQLite database path")
		fmt.Fprintln(w, "  --days N                   stale threshold in days (default 60)")
		fmt.Fprintln(w, "  --runtime claude|codex     narrow a duplicate name")
		fmt.Fprintln(w, "  --type TYPE               narrow by capability type")
		fmt.Fprintln(w, "  --scope global|user|project|session")
		fmt.Fprintln(w, "  --now RFC3339              observation time")
		fmt.Fprintln(w, "  --verbose                  include exact evidence and coverage details")
		fmt.Fprintln(w, "  --color auto|always|never  status color policy")
	default:
		fmt.Fprintf(w, "usage: harness-lint %s [options]\n", command)
		fmt.Fprintln(w, "options:")
		fmt.Fprintln(w, "  --db PATH                  SQLite database path")
		fmt.Fprintln(w, "  --home PATH                synthetic user home")
		fmt.Fprintln(w, "  --project PATH             project root")
		fmt.Fprintln(w, "  --config-dir PATH          configuration directory")
		fmt.Fprintln(w, "  --codex-home PATH          Codex configuration root")
		fmt.Fprintln(w, "  --claude-config PATH       Claude configuration root")
		fmt.Fprintln(w, "  --hook-capture PATH        repeatable metadata-only hook capture path")
		fmt.Fprintln(w, "  --since RFC3339            inclusive usage-import boundary")
		fmt.Fprintf(w, "  --days N                   stale threshold in days (default %d)\n", defaultDays(command))
		fmt.Fprintln(w, "  --now RFC3339              observation time")
		fmt.Fprintln(w, "  --color auto|always|never  status color policy")
		if command == "scan" {
			fmt.Fprintln(w, "  --verbose                  include inventory recording details")
		}
		if command == "stale" {
			fmt.Fprintln(w, "  --verbose                  include exact stale evidence")
		}
		if command == "doctor" {
			fmt.Fprintln(w, "  --verbose                  include compatibility details")
		}
		if command == "report" || command == "stale" {
			fmt.Fprintln(w, "  --json                     stable JSON output")
		}
	}
}
