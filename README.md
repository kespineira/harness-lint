# harness-lint

Local context hygiene and usage analyzer for coding agent harnesses.

`harness-lint` inventories the local capabilities that a coding-agent
harness can see, records metadata-only usage observations, and produces
deterministic reports about exposure, activity, stale definitions, and
configuration findings. The MVP supports Claude Code and Codex.

It is an analyzer, not a harness manager. It does not install, enable,
disable, rewrite, or delete skills, agents, MCP configuration, hooks, or
instruction files. It does not execute configured MCP commands or hooks,
contact an MCP endpoint, start a daemon, run a server, send data to a cloud
service, or perform destructive actions. A scan writes only its own local
SQLite state; the runtime configuration and source files are read-only.

## Install and build

Requirements:

- Go 1.24 or newer (the module declares `go 1.24.0`).
- A filesystem on which the local user can read the configured harness
  sources and create the local state directory.
- No external SQLite service: the build uses the embedded Go SQLite driver.

Build from a checkout:

```sh
go build ./cmd/harness-lint
./harness-lint --help
```

The resulting `harness-lint` binary is created in the repository root by
that exact build command. To place it on `PATH`, copy it to a user-owned bin
directory, for example:

```sh
install -m 0755 ./harness-lint "$HOME/.local/bin/harness-lint"
```

`go run ./cmd/harness-lint --help` is also sufficient for trying the CLI
without installing a binary.

## First scan

From the project being analyzed:

```sh
harness-lint scan --project "$PWD" --since 2026-01-01T00:00:00Z
```

The first scan discovers both supported runtimes, imports usage signals from
their configured local history, and records a current inventory plus
historical definitions in SQLite. Use `--db PATH` for an explicit state file
or `--db :memory:` for a one-process smoke test; an in-memory database is
gone when that command exits.

The default database is:

```text
<os.UserConfigDir()>/harness-lint/harness-lint.db
```

The path is resolved by Go's `os.UserConfigDir`, so it follows the host
platform. On macOS this is normally
`~/Library/Application Support/harness-lint/harness-lint.db`; on Linux it is
normally `$XDG_CONFIG_HOME/harness-lint/harness-lint.db` or
`~/.config/harness-lint/harness-lint.db`. `--config-dir PATH` overrides the
base used for the default database. Changing `--home` does not move the
database.

## Commands and flags

The implemented commands are `scan`, `report`, `context`, `stale`, and
`doctor`. Run `harness-lint <command> --help` for the same options shown by
the binary. The relevant command forms are:

```text
harness-lint scan    [--db PATH] [--home PATH] [--project PATH] [--config-dir PATH] [--codex-home PATH] [--claude-config PATH] [--hook-capture PATH]... [--since RFC3339] [--now RFC3339]
harness-lint report  [--db PATH] [--days N] [--now RFC3339]
harness-lint context [--db PATH] [--days N] [--now RFC3339]
harness-lint stale   [--db PATH] [--days N] [--now RFC3339]
harness-lint doctor  [--home PATH] [--project PATH] [--config-dir PATH] [--codex-home PATH] [--claude-config PATH] [--now RFC3339]
```

Flag meanings:

- `--db PATH` selects SQLite state; `:memory:` is transient.
- `--home PATH` supplies a synthetic user home for discovery. By default the
  process user home is used.
- `--project PATH` selects the project root. Without it, the CLI uses a
  repository ancestor of the current directory.
- `--config-dir PATH` selects the base for the default database path.
- `--codex-home PATH` and `--claude-config PATH` override the respective
  runtime configuration roots (normally `$HOME/.codex` and `$HOME/.claude`).
- `--hook-capture PATH` adds a repeatable, metadata-only JSON or JSONL usage
  capture path. It is not a hook installer or hook runner.
- `--since RFC3339` sets an inclusive lower bound for usage import during
  `scan`.
- `--days N` sets the stale threshold, in days; the default is `60` and it
  must be positive when analyzing a stored report.
- `--now RFC3339` sets the observation/analysis clock, useful for reproducible
  reports and tests.

Command responsibilities:

- `scan` refreshes each runtime's current inventory and imports usage history.
  A failed discovery does not replace that runtime's previously successful
  current inventory.
- `report` prints per-runtime counts, per-capability evidence, classifications,
  and usage-only observations.
- `context` prints configured baseline and on-load/body measurement summaries,
  keeping metadata and body semantics separate.
- `stale` prints the same capability evidence focused on `KEEP`, `REVIEW`,
  `STALE`, and `DEAD` classifications. A definition is stale only when its
  last loaded/invoked observation is older than the threshold; equality is
  still `KEEP`.
- `doctor` performs discovery and prints malformed, duplicate, broken-path,
  and unresolved-command findings without opening or creating the SQLite
  database.

Representative output shape (the zero-inventory form is also a useful smoke
test):

```text
$ harness-lint scan --db :memory: --home /tmp/no-home --project /tmp/no-project --now 2026-08-13T15:00:00Z
scan runtime=claude-code capabilities=0 events=0 findings=0 inventory=recorded
scan runtime=codex capabilities=0 events=0 findings=0 inventory=recorded

$ harness-lint report --db :memory: --now 2026-08-13T15:00:00Z
report as-of=2026-08-13T15:00:00Z stale-days=60
runtime=claude-code installed=0 advertised=0 loaded=0 invoked=0 configured-advertised=0 used-last-30d=0 never-used=0 usage-events=0
runtime=codex installed=0 advertised=0 loaded=0 invoked=0 configured-advertised=0 used-last-30d=0 never-used=0 usage-events=0
no current capabilities

$ harness-lint context --db :memory: --now 2026-08-13T15:00:00Z
context as-of=2026-08-13T15:00:00Z
no configured capabilities; configured baseline exposure=unknown; on-load footprint=unknown

$ harness-lint stale --db :memory: --days 60 --now 2026-08-13T15:00:00Z
stale as-of=2026-08-13T15:00:00Z days=60
no current capabilities

$ harness-lint doctor --home /tmp/no-home --project /tmp/no-project --now 2026-08-13T15:00:00Z
runtime=claude-code capabilities=0 findings=0
runtime=codex capabilities=0 findings=0
```

With inventory and observations, `report`/`stale` add lines in this form;
values are evidence, not a runtime billing calculation:

```text
runtime=codex installed=1 advertised=0 loaded=0 invoked=1 configured-advertised=1 used-last-30d=1 never-used=0 usage-events=1
  runtime=codex type=skill name=lint status=KEEP advertised=0 loaded=0 invoked=1 exposure=fully_advertised used-last-30d=yes last-used=2026-08-13T14:30:00Z evidence=observed loaded/invoked activity is within the stale threshold source=/path/to/SKILL.md
runtime=codex type=skill capabilities=1
  configured baseline exposure: metadata=42 tokens (estimated) (according to Advertisement); body=not included (Skill body is on-load only)
  on-load footprint estimate: body=18 tokens (estimated); metadata=not measured (unknown)
finding runtime=codex code=unresolved-mcp-command severity=warning confidence=observed capability=mcp_server/example message=configured local MCP command is not resolvable
```

In `report`, `advertised=N`, `loaded=N`, and `invoked=N` are counts of
observed usage events. `configured-advertised=N` is the count of current
installed definitions whose configured `Advertisement` state is advertised;
it is not an observed-event count. The per-capability `exposure=...` field is
the configured exposure state, so these values must not be collapsed into a
single notion of “used”.

## Inventory and usage sources

The adapters read documented local file locations and retain only normalized
metadata. Paths below are the defaults; the root flags can redirect them.

Claude Code discovery includes:

- user and project `.claude/skills/**/SKILL.md`, `commands/**/*.md`, and
  `agents/**/*.md` files;
- the user/project instruction hierarchy (`CLAUDE.md` and
  `CLAUDE.local.md`), Claude settings JSON, and project `.mcp.json`;
- user/global and relevant project `.claude.json` MCP definitions, including
  configured enabled/disabled state; and
- configured hook definitions as inventory metadata.

Codex discovery includes:

- project-ancestor and user `.agents/skills/**/SKILL.md` files, plus the
  optional `/etc/codex/skills` system root when it exists;
- user and project `.codex/agents/*.toml` files;
- the effective `AGENTS.override.md`/`AGENTS.md` instruction chain;
- user/project `config.toml` MCP servers and explicitly listed MCP tools;
  configured `hooks.json` and inline hook definitions; and
- the enabled/disabled skill and MCP settings that can be established from
  those files.

Usage import is deliberately narrower than inventory. Claude Code reads
best-effort JSONL transcript records below the configured Claude projects
root. Codex reads best-effort JSON/JSONL records below its sessions root.
Both adapters also accept explicitly supplied `--hook-capture` JSON/JSONL
files or directories. Only explicit, timestamped tool-use signals are
imported; arbitrary prompt/message/response content does not become usage.

Current limitations are runtime-specific: transcript schemas and formats are
best-effort and may change, malformed or unsupported records can be skipped,
and a hook capture must contain an explicit timestamp plus usable metadata.
Timestamp-less hook records are ignored; `harness-lint` does not tail a live
hook or install one. Runtime configuration can prove configured exposure, but
loaded/invoked evidence appears only when a matching transcript or capture
signal is observable.

## Official runtime references

These primary references describe the formats that the adapters observe.
They evolve independently of this project, so discovery and transcript
interpretation remain best-effort:

- Claude Code: [settings](https://code.claude.com/docs/en/settings),
  [skills](https://code.claude.com/docs/en/skills),
  [hooks](https://code.claude.com/docs/en/hooks), and
  [sub-agents](https://code.claude.com/docs/en/sub-agents).
- Codex: [skills](https://learn.chatgpt.com/docs/build-skills),
  [configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference),
  [MCP for the CLI](https://learn.chatgpt.com/docs/extend/mcp?surface=cli),
  [hooks](https://learn.chatgpt.com/docs/hooks), and
  [subagents](https://learn.chatgpt.com/docs/agent-configuration/subagents).

## Privacy and measurement contract

The SQLite schema is metadata-only. It can contain runtime, capability type
and name, scope, source path, enabled/advertisement state, content hashes,
measurement provenance, UTC timestamps, fingerprints, and one-way SHA-256
session/project identifiers. Files are read transiently to extract metadata,
hashes, and measurements, then discarded.

It never persists prompts, responses, source-code text, tool arguments or
inputs, tool outputs, MCP configuration payloads/endpoints/command arguments,
or model output. MCP server/tool names are capability metadata only; exchanged
MCP data is not stored. The local database is not uploaded or shared by the
CLI.

Token-looking measurements are advertised-size evidence, not a tokenizer or
runtime bill. `harness-lint` never claims an exact runtime token cost. Every
measurement carries one of these confidence labels:

| Confidence | Meaning |
| --- | --- |
| `exact` | Exact for the measured field supplied by its source; still not runtime cost. |
| `observed` | Directly observed state or usage evidence, such as a timestamped event or hidden configuration. |
| `estimated` | A documented size estimate (for example, UTF-8 bytes divided by four), not an exact tokenization. |
| `unknown` | The source does not expose enough information; unknown values are excluded from numeric subtotals. |

The report keeps these signals distinct: `installed != advertised != loaded != invoked`. Installed means present in the latest successful inventory.
Advertised means configured exposure or an observed advertised event; the
report labels configured exposure separately as `configured-advertised`.
Loaded and invoked require their respective usage evidence. A definition may
be installed but not advertised, advertised but never loaded, loaded but
never invoked, or observed in usage without appearing in the current
inventory. Such unmatched events are printed as `usage-only` rather than
invented inventory.

## Architecture / MVP decisions

```text
Claude Code files/transcripts ─┐
                               ├─ runtime adapters ── metadata snapshots/events
Codex files/transcripts ───────┘                         │
                                                        ▼
                                      SQLite inventory + usage history
                                                        │
                                                        ▼
                                      deterministic analysis and labels
                                                        │
                                                        ▼
                                             read-only CLI presentation
```

- Runtime adapters isolate Claude Code and Codex path/config/transcript
  semantics and return runtime-neutral inventory and usage values.
- SQLite stores historical definitions, current-inventory markers, and
  deduplicated usage events through forward migrations. Empty scans preserve
  history while replacing only the successful runtime's current snapshot.
- Analysis is deterministic: it validates inputs, separates advertised,
  loaded, and invoked evidence, propagates confidence, detects duplicates,
  and applies explicit stale/review policy without opaque scores.
- The CLI is read-only against harness sources and writes only its own local
  state. Command lookup is used only to report whether a configured executable
  appears resolvable; no configured command is started.

The project is macOS-first and Linux-compatible. It uses Go filesystem paths,
the host `os.UserConfigDir`, and local runtime conventions; other operating
systems may require path/layout validation. The current MVP intentionally
supports only Claude Code and Codex adapters.

## Verification

Run the repository checks:

```sh
git diff --check
go test ./...
go vet ./...
go build ./cmd/harness-lint
```

The following practical smokes exercise every command without writing a
persistent database (each `:memory:` invocation is independent):

```sh
go run ./cmd/harness-lint --help
go run ./cmd/harness-lint scan --db :memory: --home /tmp/no-home --project /tmp/no-project --now 2026-08-13T15:00:00Z
go run ./cmd/harness-lint report --db :memory: --now 2026-08-13T15:00:00Z
go run ./cmd/harness-lint context --db :memory: --now 2026-08-13T15:00:00Z
go run ./cmd/harness-lint stale --db :memory: --days 60 --now 2026-08-13T15:00:00Z
go run ./cmd/harness-lint doctor --home /tmp/no-home --project /tmp/no-project --now 2026-08-13T15:00:00Z
```

## License

Apache-2.0. `LICENSE` is the canonical Apache License, Version 2.0 text.
