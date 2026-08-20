#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: $0 /path/to/harness-lint" >&2
  exit 2
fi

smoke_binary=$1
if [ ! -x "$smoke_binary" ]; then
  echo "smoke binary is not executable: $smoke_binary" >&2
  exit 2
fi

smoke_tmp_parent=${TMPDIR:-/tmp}
smoke_root=$(mktemp -d "$smoke_tmp_parent/harness-lint-smoke.XXXXXX")
case "$smoke_root" in
  "$smoke_tmp_parent"/harness-lint-smoke.??????) ;;
  *) echo "mktemp returned an unexpected smoke root: $smoke_root" >&2; exit 2 ;;
esac
if [ ! -d "$smoke_root" ]; then
  echo "mktemp did not create smoke root: $smoke_root" >&2
  exit 2
fi
trap 'rm -rf "$smoke_root"' EXIT HUP INT TERM
smoke_home=$smoke_root/home
smoke_project=$smoke_root/project
smoke_config=$smoke_root/config
smoke_db=$smoke_root/state/harness-lint.db
smoke_explicit_backup=$smoke_root/state/explicit-backup.db
mkdir -p "$smoke_home" "$smoke_project" "$smoke_config" "$smoke_root/state"

# Every process-dependent root is inside the disposable tree. In particular,
# no command in this smoke may read or write the operator's real HOME/config.
export HOME="$smoke_home"
export XDG_CONFIG_HOME="$smoke_root/xdg-config"
export XDG_DATA_HOME="$smoke_root/xdg-data"
export XDG_CACHE_HOME="$smoke_root/xdg-cache"
smoke_path="$(dirname "$smoke_binary"):$PATH"
export PATH="$smoke_path"

smoke_now=2026-08-14T12:00:00Z
smoke_run() {
  "$smoke_binary" "$@"
}

smoke_run --help
smoke_run hooks --help
smoke_run db --help
smoke_run scan --db "$smoke_db" --home "$smoke_home" --project "$smoke_project" --config-dir "$smoke_config" \
  --codex-home "$smoke_home/.codex" --claude-config "$smoke_home/.claude" --now "$smoke_now"
smoke_run hooks install claude --home "$smoke_home" --codex-home "$smoke_home/.codex" --claude-config "$smoke_home/.claude"
smoke_run hooks install codex --home "$smoke_home" --codex-home "$smoke_home/.codex" --claude-config "$smoke_home/.claude"
smoke_run hooks status --home "$smoke_home" --codex-home "$smoke_home/.codex" --claude-config "$smoke_home/.claude" --now "$smoke_now"
smoke_run hooks status --home "$smoke_home" --codex-home "$smoke_home/.codex" --claude-config "$smoke_home/.claude" --verbose --now "$smoke_now"
smoke_run hooks test --db "$smoke_db" --home "$smoke_home" --codex-home "$smoke_home/.codex" --claude-config "$smoke_home/.claude" --now "$smoke_now"
smoke_run hooks test --db "$smoke_db" --home "$smoke_home" --codex-home "$smoke_home/.codex" --claude-config "$smoke_home/.claude" --verbose --now "$smoke_now"
smoke_run usage --db "$smoke_db" --now "$smoke_now"
smoke_run usage --db "$smoke_db" --days 90 --now "$smoke_now"
smoke_run usage --db "$smoke_db" --runtime claude --now "$smoke_now"
smoke_run usage --db "$smoke_db" --type skill --now "$smoke_now"
smoke_run usage --db "$smoke_db" --monthly --now "$smoke_now"
smoke_run usage --db "$smoke_db" --json --now "$smoke_now"
smoke_run report --db "$smoke_db" --now "$smoke_now"
smoke_run report --db "$smoke_db" --all --now "$smoke_now"
smoke_run report --db "$smoke_db" --verbose --now "$smoke_now"
smoke_run report --db "$smoke_db" --json --now "$smoke_now"
smoke_run stale --db "$smoke_db" --now "$smoke_now"
smoke_run stale --db "$smoke_db" --verbose --now "$smoke_now"
smoke_run stale --db "$smoke_db" --json --now "$smoke_now"
smoke_run context --db "$smoke_db" --now "$smoke_now"
smoke_run doctor --home "$smoke_home" --project "$smoke_project" --config-dir "$smoke_config" \
  --codex-home "$smoke_home/.codex" --claude-config "$smoke_home/.claude" --now "$smoke_now"
smoke_run doctor --home "$smoke_home" --project "$smoke_project" --config-dir "$smoke_config" \
  --codex-home "$smoke_home/.codex" --claude-config "$smoke_home/.claude" --verbose --now "$smoke_now"
smoke_run db status --db "$smoke_db" --now "$smoke_now"
smoke_run db status --db "$smoke_db" --verbose --now "$smoke_now"
smoke_run db status --db "$smoke_db" --json --now "$smoke_now"
smoke_run db check --db "$smoke_db" --now "$smoke_now"
smoke_run db check --db "$smoke_db" --verbose --now "$smoke_now"
smoke_run db check --db "$smoke_db" --json --now "$smoke_now"
smoke_default_backup_output=$(smoke_run db backup --db "$smoke_db" --now "$smoke_now")
# The human backup view is intentionally readable rather than field-style. Its
# wrapped Destination row may span more than one physical line, so join the
# indented continuation before checking the generated file.
smoke_default_backup=$(printf '%s\n' "$smoke_default_backup_output" | awk '
  /^  Destination[[:space:]]/ {
    value = $0
    sub(/^  Destination[[:space:]]+/, "", value)
    destination = value
    active = 1
    next
  }
  active && /^               / {
    value = $0
    sub(/^[[:space:]]+/, "", value)
    destination = destination value
    next
  }
  active && /^  [^ ]/ { active = 0 }
  END { print destination }
')
if [ -z "$smoke_default_backup" ] || [ ! -s "$smoke_default_backup" ]; then
  echo "default database backup is empty or missing: $smoke_default_backup" >&2
  exit 1
fi
smoke_run db backup --db "$smoke_db" --output "$smoke_explicit_backup" --now "$smoke_now"
test -s "$smoke_explicit_backup"
