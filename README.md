# harness-lint

Local context hygiene and usage analyzer for coding agent harnesses.

`harness-lint` inventories the local capabilities that a coding-agent
harness can see, records metadata-only usage observations, and produces
deterministic reports about exposure, activity, stale definitions, and
configuration findings. The MVP supports Claude Code and Codex.

Usage can come from a directly installed metadata-only command hook (the
preferred source) or from best-effort transcript/file imports used as
backfill and verification. Hook management is opt-in and limited to the
owned entries in the two supported runtime configuration files; it does not
manage skills, agents, MCP configuration, or instruction files. The tool does
not execute configured MCP commands or hooks, contact an MCP endpoint, start
a daemon, run a server, send data to a cloud service, or perform destructive
actions. A scan writes only its own local SQLite state, while hook install and
uninstall make narrowly scoped, safe configuration edits described below.

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

The implemented commands are `scan`, `usage`, `report`, `context`, `stale`,
`doctor`, `hooks`, and `ingest`. Run `harness-lint <command> --help` for the
same options shown by the binary. The relevant command forms are:

```text
harness-lint scan    [--db PATH] [--home PATH] [--project PATH] [--config-dir PATH] [--codex-home PATH] [--claude-config PATH] [--hook-capture PATH]... [--since RFC3339] [--now RFC3339]
harness-lint usage   [--json] [--db PATH] [--days N] [--runtime claude|claude-code|codex] [--type skill|mcp|tool|agent] [--monthly] [--now RFC3339]
harness-lint report  [--json] [--db PATH] [--days N] [--now RFC3339]
harness-lint context [--db PATH] [--days N] [--now RFC3339]
harness-lint stale   [--json] [--db PATH] [--days N] [--now RFC3339]
harness-lint doctor  [--home PATH] [--project PATH] [--config-dir PATH] [--codex-home PATH] [--claude-config PATH] [--now RFC3339]
harness-lint db      <status|check|backup> [--json] [--db PATH] [--config-dir PATH] [--output PATH] [--now RFC3339]
harness-lint hooks status    [claude|codex] [--json] [--home PATH] [--codex-home PATH] [--claude-config PATH] [--now RFC3339]
harness-lint hooks test      [claude|codex] [--db PATH] [--home PATH] [--codex-home PATH] [--claude-config PATH] [--now RFC3339]
harness-lint hooks install   [claude|codex] [--dry-run] [--home PATH] [--codex-home PATH] [--claude-config PATH] [--now RFC3339]
harness-lint hooks uninstall [claude|codex] [--dry-run] [--home PATH] [--codex-home PATH] [--claude-config PATH] [--now RFC3339]
harness-lint ingest  --runtime <claude|codex> [--event EVENT] [--managed-by harness-lint-hooks/v1] [--db PATH] < one JSON document on stdin
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
- `--days N` sets the stale threshold for `report`/`stale` (default `60`) or
  the closed UTC history period for `usage` (default `90`); it must be
  positive.
- `--runtime`, `--type`, and `--monthly` are `usage`-only filters/output
  controls. `--type mcp` includes both MCP servers and MCP tools while the
  emitted filter remains `mcp`.
- `--now RFC3339` sets the observation/analysis clock, useful for reproducible
  reports and tests.

The hook commands are a small machine-facing API. They are normally invoked
by an operator during setup or diagnosis, while `ingest` is normally invoked
by an installed runtime hook and is intentionally quiet on success:

```sh
harness-lint hooks status [claude|codex] [--json]
harness-lint hooks test [claude|codex] --db PATH
harness-lint hooks install [claude|codex] [--dry-run]
harness-lint hooks uninstall [claude|codex] [--dry-run]
harness-lint ingest --runtime claude|codex [--event EVENT] [--managed-by harness-lint-hooks/v1] --db PATH < hook-payload.json
```

`hooks status` reads Claude `$HOME/.claude/settings.json` and Codex
`$HOME/.codex/hooks.json` (or the corresponding root flags), reports the
owned event entries, and diagnoses whether the stable `harness-lint` name is
resolvable on `PATH`. `hooks install` and `hooks uninstall` target the
runtime-specific configuration root; neither accepts `--db`, and neither
starts a configured command. `--dry-run` is a flag on install or uninstall:
it prints the planned changes and does not create directories, backups, or
files. `ingest` receives exactly one JSON document, normalizes only the
documented metadata needed for one event, and writes only the local SQLite
event; users should not call it with prompts, tool results, or other data.

`hooks test` is a read-only health check. It checks configuration, owned
entries, executable resolution, local SQLite/schema state, a synthetic ingest
self-test, and bounded delivery health without invoking Claude or Codex. It
does not insert a usage event. The exact output states are
`healthy`, `idle`, `degraded`, `broken`, and `unknown`; `--json` is rejected
for this command. The synthetic self-test proves local ingest/SQLite behavior,
but not true runtime delivery when no activity has occurred.

Database diagnostics are separate from runtime discovery. `db status` opens
the selected SQLite file, reports schema/count/time-range metadata, and does
not run integrity checks. `db check` runs read-only SQLite quick, foreign-key,
and embedded-schema checks; it never migrates, repairs, checkpoints, or
deletes data. `db backup` creates a consistent SQLite Online Backup API copy;
`--output PATH` selects an exclusive destination, while omitting it creates a
timestamped file under `<db-directory>/backups/`. Backups never overwrite an
existing destination, and there is no `restore` command or automatic backup
pruning.

Command responsibilities:

- `scan` refreshes each runtime's current inventory and imports usage history.
  A failed discovery does not replace that runtime's previously successful
  current inventory.
- `report` prints per-runtime counts, per-capability evidence, classifications,
  and usage-only observations.
- `usage` queries stored invocation history over a closed UTC interval. It can
  filter by runtime/type and add UTC monthly evidence without reading live
  runtime configuration.
- `context` prints configured baseline and on-load/body measurement summaries,
  keeping metadata and body semantics separate.
- `stale` prints the same capability evidence focused on `KEEP`, `REVIEW`,
  and `STALE` classifications. A definition is stale only when its last
  loaded/invoked observation is older than the threshold; equality is still
  `KEEP`. `DEAD` is reserved for a future completeness signal: an absent
  event, source, or scan cannot prove terminal non-use.
- `doctor` performs discovery and prints malformed, duplicate, broken-path,
  and unresolved-command findings without opening or creating the SQLite
  database.
- `db status`, `db check`, and `db backup` operate only on the selected local
  SQLite file; they do not inspect runtime configuration or start commands.

Representative output shape (the zero-inventory form is also a useful smoke
test):

```text
$ harness-lint scan --db :memory: --home /tmp/no-home --project /tmp/no-project --now 2026-08-13T15:00:00Z
scan runtime=claude-code capabilities=0 events=0 findings=0 inventory=recorded
scan runtime=codex capabilities=0 events=0 findings=0 inventory=recorded

$ harness-lint report --db :memory: --now 2026-08-13T15:00:00Z
report as-of=2026-08-13T15:00:00Z stale-days=60
runtime=claude-code installed=0 advertised=0 loaded=0 invoked=0 configured-advertised=0 used-last-30d=0 no-activity-observed=0 usage-events=0
runtime=codex installed=0 advertised=0 loaded=0 invoked=0 configured-advertised=0 used-last-30d=0 no-activity-observed=0 usage-events=0
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

An isolated history query against a populated state file is:

```sh
harness-lint usage --db /path/to/harness-lint.db --days 90 --runtime codex --type mcp --monthly --json
```

The returned period is `[generated_at - 90*24h, generated_at]`, inclusive at
both boundaries. The same query can be rendered for a person with `--json`
omitted. For a local hook readiness check, use the read-only command with
explicit roots:

```sh
harness-lint hooks test --db /path/to/harness-lint.db --claude-config "$HOME/.claude" --codex-home "$HOME/.codex"
```

## Direct hook capture and transcript fallback

Direct command-hook capture is the preferred usage source because the runtime
delivers one event at the point where the tool call is observed. The generated
hook only forwards a bounded JSON document to the quiet `ingest` receiver; it
does not run `scan`, read transcripts, start a daemon, use the network, or
gate the agent on report generation. The handler is asynchronous (`async: true`
with the manager's ten-second timeout), so the runtime remains responsible for
its own hook scheduling and failure behavior; `harness-lint` does not make the
hook a synchronous policy gate.

Transcript and file-capture imports are fallback, backfill, and verification
paths. They are useful when hooks were not installed, when a delivery was
missed, or when comparing direct evidence with historical records. A path
passed through repeatable `--hook-capture` is classified as `import`, not as
proof that a live hook delivered the document. Transcript formats are parsed
best-effort and only records with explicit, usable tool identity and a
timestamp become events. The analyzer never tails a live transcript and does
not infer usage from arbitrary prompts, messages, responses, or neighboring
records.

The normalized event contract keeps two clocks separate:

- `observed_at` is always the local receive/import time recorded by
  `harness-lint`.
- `source_timestamp` is optional and is populated only when a transcript or
  other source explicitly supplies a timestamp that the adapter recognizes.
  Current direct Claude and Codex hook payloads use local `observed_at` and
  leave `source_timestamp` empty; undocumented timestamp-looking fields are
  ignored.

Every event also records `provenance` (`hook`, `transcript`, or `import`), a
schema version, and an invocation origin when the source proves one. A stable
runtime delivery identity (for example, the scoped `tool_use_id`, kept only
as a one-way hash) is the first deduplication key. If it is unavailable, a
recognized source timestamp is used; otherwise the local observed time is the
conservative fallback. Provenance is not allowed to make the same stable
delivery count twice, while a legitimate second call with a distinct delivery
identity remains a second invocation.

Capture-health diagnostics are deliberately bounded. The store retains only
the runtime, last successful/failed delivery times, a consecutive-failure
count capped at `32`, and one allow-listed failure kind:
`malformed_payload`, `unsupported_event`, `database_busy`,
`database_unavailable`, `schema_error`, or `internal_error`. Raw errors,
stdin, prompts, arguments, results, and payload text are not retained or
echoed.

### Claude Code

The installed Claude entries cover `PostToolUse`, `PostToolUseFailure`, and
`UserPromptExpansion`. `PostToolUse` and `PostToolUseFailure` prove a tool
invocation; the same `tool_use_id` is intentionally deduplicated across a
success/failure retry. A `PostToolUse` with `tool_name=Skill` and a usable
`tool_input.skill` identifies a Skill, while `Agent`/`Task` uses
`tool_input.subagent_type`; MCP tool names and other built-in tool names are
retained only as capability metadata.

For an explicit slash command, `UserPromptExpansion` with
`expansion_type=slash_command` and `command_name` is recorded as a
user-explicit `command`. The payload does not provide a proven Skill identity
on this path, so the adapter does not invent one: explicit Skill identity is
captured through the `Skill` `PostToolUse` path. Other expansion types,
subagent lifecycle events, and unsupported hook events are not counted as
invocations. Hook input, tool arguments, and tool results are consumed only
to validate shape or select a documented identity and are discarded.

### Codex

The installed Codex entry covers only proven `PostToolUse` semantics. The
adapter records the documented tool name, recognizes `spawn_agent`/`Agent` as
the `spawn_agent` agent identity, and recognizes valid MCP tool names. It does
not manufacture Claude-style Skill events or claim Skill parity where Codex
hook input does not prove it. Other Codex lifecycle or prompt events are
ignored by direct ingestion; transcript import remains the historical
fallback.

Codex user-level hooks may also be present inline in `config.toml`. Status
reports that condition separately and includes the runtime trust-review
limitation: writing `hooks.json` cannot grant the trust/review decision needed
by the Codex UI. The manager preserves inline configuration and does not try
to enable or execute it.

With inventory and observations, `report`/`stale` add lines in this form;
values are evidence, not a runtime billing calculation:

```text
runtime=codex installed=1 advertised=0 loaded=0 invoked=1 configured-advertised=1 used-last-30d=1 no-activity-observed=0 usage-events=1
capabilities:
  runtime=codex type=skill name=lint status=KEEP advertised=0 loaded=0 invocation-uses=1 distinct-sessions=1 exposure=fully_advertised used-last-30d=yes evidence-sources=hook confidence=observed coverage-confidence=unknown
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

The `invoked_in_advertised_sessions` value is an observation-efficiency
intersection: the number of distinct invocation sessions that also have an
explicit advertised observation in the selected evidence. It is not a
percentage, conversion rate, or context-cost estimate. It is `unknown`
(`null` in JSON) when advertised-session evidence is absent; a known zero
means advertised sessions were observed but none of those sessions contained
an invocation.

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
files or directories. Only explicit, timestamped transcript/import tool-use
signals are imported; arbitrary prompt/message/response content does not
become usage. Direct hook stdin is handled by `ingest` instead, and uses the
local receive clock because the current documented hook payloads do not prove
a source occurrence timestamp.

Runtime configuration can prove configured exposure, but loaded/invoked
evidence appears only when a matching direct hook, transcript, or file-capture
signal is observable. An empty or failed discovery does not prove that a
capability was unused.

## Hook installation safety and relocation

The manager owns only entries bearing its exact versioned shape and marker:
`harness-lint ingest --runtime ... --managed-by harness-lint-hooks/v1`. It
does not remove lookalike commands, older markers, handlers under a user
matcher, or unrelated fields. Installation merges the owned event groups with
existing JSON, preserves unrelated hooks and field order, creates private
configuration directories as needed, and refuses malformed JSON, symlinked or
unsafe paths, and unresolved binaries before mutation.

The generated command stores the stable executable name `harness-lint`, not
the absolute path returned by `PATH` lookup. This keeps a copied or relocated
binary usable after a normal `PATH` change, but it means the runtime that
launches the hook must be able to resolve that name. `hooks status --json`
reports `binary.resolved`, `binary.resolved_path` for diagnostics, and a safe
error when the executable is missing; install refuses to proceed while it is
unresolved. A user who relocates the binary should update the runtime process'
`PATH` and rerun status rather than editing the generated command.

Existing configuration is copied to the next unused `.bak` path before an
update. New content is written to a private temporary file, synced, and
atomically renamed into place while retaining the existing file mode. The
operation is idempotent: a second install makes no change and does not create
another backup. Uninstall removes only exact current-version entries, leaves
unrelated hooks and configuration in place, and is a no-op when no owned entry
is present. Dry-run performs inspection and prints a plan without creating a
directory, backup, temporary file, or configuration.

Claude and Codex configuration locations are independent of the SQLite state:
`$HOME/.claude/settings.json` and `$HOME/.codex/hooks.json`, respectively.
Use `--claude-config` or `--codex-home` for isolated roots; status, install,
and uninstall never use `--db`.

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

The accepted runtime-specific evidence boundaries, fixture coverage, and
unsupported cases are recorded in the [runtime conformance matrix](docs/runtime-conformance.md).
That matrix is the conformance reference; this README does not claim Claude
Code/Codex telemetry parity.

Compatibility diagnostics are deliberately conservative. `doctor` and
`hooks test` may report a detected local runtime version, but the current
validation basis is the dated synthetic conformance fixture set and no live
runtime version is considered validated. Consequently a detected version is
normally `status=unknown`, while missing executables, failed commands, or
unparseable output are bounded diagnostics rather than compatibility proof.

## Privacy and measurement contract

The SQLite schema is metadata-only. Retained fields can include runtime,
capability type and name, scope, source path, enabled/advertisement state,
content hashes, measurement values and provenance, UTC `observed_at` and
optional `source_timestamp`, `provenance`, `schema_version`, invocation
origin, fingerprints, and one-way SHA-256 session/project/source identities.
Files are read transiently to extract metadata, hashes, and measurements, then
discarded. The local database is not uploaded or shared by the CLI.

The database and JSON reports prohibit prompts, responses, source-code text,
tool arguments or inputs, tool outputs, MCP configuration payloads/endpoints/
command arguments, and model output. MCP server/tool names are capability
metadata only; exchanged MCP data is not stored. The hook receiver also
avoids echoing malformed stdin in errors. Hook configuration files remain
runtime-owned files: the safe manager preserves unrelated JSON fields and
handlers but does not copy their contents into SQLite or reports.

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

For both human and JSON output, an advertised-session count is `unknown` (or
`null`/omitted in the documented JSON shape) when no explicit advertised
event was observed. It is not reported as zero: zero would assert that an
advertised event occurred in zero sessions. See the [public JSON schema
reference](docs/cli-json-schemas.md) for the command-specific nullability.

Capability presence is also distinct from event history. A successful scan
opens a presence epoch for each discovered definition, retaining its complete
runtime/type/name/scope/source identity; an empty or failed scan does not
backfill a historical interval. Confirmed direct hook delivery opens a
separate capture epoch. Modeled effective coverage is only the intersection
of those confirmed epochs, and observation windows alone never establish
continuity or lifetime completeness. This is why `coverage_confidence` can
remain `unknown` even when events exist.

## JSON contracts and examples

`report --json` and `stale --json` emit versioned schema-v2 objects with
`schema_version`, `generated_at`, `stale_after_days`, `runtimes`,
`capabilities`, and (for `report`) `usage_only` and `findings`. Arrays are
present as `[]` when empty. A privacy-safe abbreviated report looks like:

```json
{
  "schema_version": 2,
  "generated_at": "2026-08-14T12:00:00Z",
  "stale_after_days": 60,
  "runtimes": [{"runtime":"codex","installed":1,"advertised":0,"loaded":0,"invoked":1,"configured_advertised":1,"invoked_last_30d":1,"no_activity_observed":0,"usage_events":1}],
  "capabilities": [{"runtime":"codex","type":"skill","name":"lint","scope":"user","enabled":"enabled","advertisement":"fully_advertised","status":"KEEP","confidence":"observed","coverage_confidence":"unknown","evidence_sources":["hook"],"invocation_count":1,"distinct_sessions":1}],
  "usage_only": [],
  "findings": []
}
```

`hooks status --json` emits schema-v1 `schema_version`, `generated_at`, and one runtime
object per selected runtime. Each object includes `status`, `config_path`,
`managed_entries`, `binary` (`name`, `resolved`, optional diagnostic path or
error), `inline_hooks`, `trust_review`, and `warnings`. A missing executable
is represented as `binary.resolved: false`; its diagnostic path is not used
as the installed command. `ingest` emits no JSON or human output on success;
its input is one runtime-specific hook document and its output is the local
SQLite event.

The complete field tables, nullability rules, RFC3339Nano timestamp contract,
monthly usage shape, filters, observation coverage, and pre-1.0 additive
compatibility guidance are in [Public CLI JSON schemas](docs/cli-json-schemas.md).

## State, migrations, and upgrades

The default state file is
`<os.UserConfigDir()>/harness-lint/harness-lint.db` (normally
`~/Library/Application Support/harness-lint/harness-lint.db` on macOS or
`$XDG_CONFIG_HOME/harness-lint/harness-lint.db` / `~/.config/harness-lint/harness-lint.db`
on Linux). `--config-dir PATH` selects the base for that default, and
`--db PATH` selects an exact SQLite file; `--db :memory:` is process-local and
disappears on exit. Runtime hook configuration is separate under the Claude
and Codex roots described above; changing `--home` does not move the
database.

SQLite migrations are embedded, numbered, and forward-only. Opening an older
database applies the missing migrations in order. A v5 database upgrades to
v7 automatically: v6 backfills one normalized evidence relation per existing
usage row, and v7 adds empty capture/presence epoch tables without fabricating
historical epochs. Existing inventory, usage rows, fingerprints, and the
legacy `timestamp` column remain available to old readers. There is no
automatic downgrade or remote migration service; copy a state file before an
operational upgrade when rollback of local state matters. See [SQLite
migration architecture](docs/migration-architecture.md)
for the runner invariants and preservation boundaries.

The MVP has no automatic event retention, pruning, or destructive cleanup
policy. Reports describe
the evidence still present in the local database and do not claim complete
lifetime history. `no_activity_observed` and phrases such as `never observed`
mean that no matching event is present in the ingested evidence for the
current inventory; they do not prove that a user or model never used the
capability. A missing, malformed, unsupported, or uninstalled source can make
coverage incomplete.

## Architecture / MVP decisions

```text
Claude Code hooks/files/transcripts ─┐
                                     ├─ runtime adapters ── metadata snapshots/events
Codex hooks/files/transcripts ───────┘                         │
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
- Scan, report, context, stale, doctor, and ingest are read-only against
  harness sources and write only local state or output. The opt-in hook
  manager is the sole configuration writer and edits only its exact owned
  entries. `usage` and `hooks test` are also read-only. Command lookup is used
  only to report whether the stable executable appears resolvable; no
  configured command is started. There is no daemon and no automatic
  retention deletion.

The project is macOS-first and Linux-compatible. It uses Go filesystem paths,
the host `os.UserConfigDir`, and local runtime conventions; other operating
systems may require path/layout validation. The current MVP intentionally
supports only Claude Code and Codex adapters.

## Known limitations

Known limitations include best-effort discovery and transcript import, no
direct-hook source timestamp in the current runtime payloads, no Claude/Codex
telemetry parity, no proof of runtime trust or UI review decisions, and no
proof that a missing event means non-use. The local store is intentionally
daemonless: there is no server, scheduler, remote service, restore workflow,
or automatic retention/pruning policy.

## Verification

Run the repository checks:

```sh
git diff --check
gofmt -l .
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go build -trimpath ./cmd/harness-lint
```

The following practical smoke exercises every required command in one fresh
temporary tree and uses only temporary HOME/config/SQLite paths:

```sh
smoke_binary_root="$(mktemp -d)"
trap 'rm -rf "$smoke_binary_root"' EXIT
go build -trimpath -o "$smoke_binary_root/harness-lint" ./cmd/harness-lint
./scripts/isolated-smoke.sh "$smoke_binary_root/harness-lint"
```

The script runs help, scan, hooks status/test, usage (normal, custom days,
monthly, and JSON), stale (normal and JSON), context, doctor, and database
status/check/backup. It must receive the built binary path; it creates and
removes its own disposable tree and never targets live Claude or Codex
configuration.

## License

Apache-2.0. `LICENSE` is the canonical Apache License, Version 2.0 text.
