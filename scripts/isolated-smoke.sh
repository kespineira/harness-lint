#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: $0 /path/to/harness-lint" >&2
  exit 2
fi

binary=$1
if [ ! -x "$binary" ]; then
  echo "smoke binary is not executable: $binary" >&2
  exit 2
fi

root=$(mktemp -d "${TMPDIR:-/tmp}/harness-lint-smoke.XXXXXX")
trap 'rm -rf "$root"' EXIT HUP INT TERM
home=$root/home
project=$root/project
config=$root/config
db=$root/state/harness-lint.db
backup=$root/state/explicit-backup.db
mkdir -p "$home" "$project" "$config" "$root/state"

# Every process-dependent root is inside the disposable tree. In particular,
# no command in this smoke may read or write the operator's real HOME/config.
export HOME=$home
export XDG_CONFIG_HOME=$root/xdg-config
export XDG_DATA_HOME=$root/xdg-data
export XDG_CACHE_HOME=$root/xdg-cache
PATH="$(dirname "$binary"):$PATH"
export PATH

now=2026-08-14T12:00:00Z
run() {
  "$binary" "$@"
}

run --help
run hooks --help
run db --help
run scan --db "$db" --home "$home" --project "$project" --config-dir "$config" \
  --codex-home "$home/.codex" --claude-config "$home/.claude" --now "$now"
run hooks install claude --home "$home" --codex-home "$home/.codex" --claude-config "$home/.claude"
run hooks install codex --home "$home" --codex-home "$home/.codex" --claude-config "$home/.claude"
run hooks status --home "$home" --codex-home "$home/.codex" --claude-config "$home/.claude" --now "$now"
run hooks test --db "$db" --home "$home" --codex-home "$home/.codex" --claude-config "$home/.claude" --now "$now"
run usage --db "$db" --now "$now"
run usage --db "$db" --days 7 --now "$now"
run usage --db "$db" --monthly --now "$now"
run usage --db "$db" --json --now "$now"
run stale --db "$db" --days 60 --now "$now"
run stale --db "$db" --days 60 --json --now "$now"
run context --db "$db" --now "$now"
run doctor --home "$home" --project "$project" --config-dir "$config" \
  --codex-home "$home/.codex" --claude-config "$home/.claude" --now "$now"
run db status --db "$db" --now "$now"
run db check --db "$db" --now "$now"
run db backup --db "$db" --output "$backup" --now "$now"

test -s "$backup"
