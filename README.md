# harness-lint

Local context hygiene and usage analyzer for coding agent harnesses.

[![CI](https://github.com/kespineira/harness-lint/actions/workflows/ci.yml/badge.svg)](https://github.com/kespineira/harness-lint/actions/workflows/ci.yml)
[![Release](https://github.com/kespineira/harness-lint/actions/workflows/release.yml/badge.svg)](https://github.com/kespineira/harness-lint/actions/workflows/release.yml)
[![Latest release](https://img.shields.io/github/v/release/kespineira/harness-lint?sort=semver)](https://github.com/kespineira/harness-lint/releases/latest)
[![npm](https://img.shields.io/npm/v/harness-lint)](https://www.npmjs.com/package/harness-lint)
[![Go Reference](https://pkg.go.dev/badge/github.com/kespineira/harness-lint.svg)](https://pkg.go.dev/github.com/kespineira/harness-lint)
[![Go Report Card](https://goreportcard.com/badge/github.com/kespineira/harness-lint)](https://goreportcard.com/report/github.com/kespineira/harness-lint)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![macOS](https://img.shields.io/badge/platform-macOS-lightgrey)](#install)
[![Linux](https://img.shields.io/badge/platform-Linux-lightgrey)](#install)

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

## Install

Supported release targets are macOS and Linux on amd64 and arm64. The
commands below install the latest release unless a version is pinned.

### Homebrew cask (macOS)

```sh
brew install --cask kespineira/tap/harness-lint
```

The cask currently has no Apple signing or notarization and does not remove
the `com.apple.quarantine` attribute. If your policy permits it, review the
release archive and clear that attribute manually before running the binary;
otherwise retain quarantine and use a notarized distribution.

The current GoReleaser/Homebrew Cask output also cannot carry a Cask
`license` stanza in this setup, so the generated cask may omit that packaging
metadata. The project license is not absent: it is Apache-2.0, remains in
the repository, and is included in release archives and Linux packages.

### npm (global)

The npm distribution provides the same CLI through one cross-platform launcher
and platform-specific native packages. Node.js is required only when using npm
or npx; Homebrew, `install.sh`, Go, and GitHub Release installs run the native
binary directly and do not require Node.js.

The launcher keeps consumer compatibility at Node.js `>=18.0.0`. Node.js 18
is EOL; use a supported LTS release for new installations.

With Node.js 18 or newer installed, install the latest npm release globally:

```sh
npm install --global harness-lint@latest
```

The package installs the `harness-lint` command and resolves the matching
macOS or Linux amd64/arm64 native package through npm's normal dependency
resolution. It does not download a binary or run an install hook at runtime.

### npx

To run the latest npm release without a global install (Node.js 18 or newer):

```sh
npx --yes harness-lint@latest --help
```

Pin a published version when reproducibility matters, for example
`npx --yes harness-lint@X.Y.Z --version`.

### install.sh (macOS/Linux)

The convenient latest-release form is:

```sh
curl -fsSL https://raw.githubusercontent.com/kespineira/harness-lint/main/scripts/install.sh | sh
```

Download the installer, review it, and run it as your user:

```sh
curl -fsSL https://raw.githubusercontent.com/kespineira/harness-lint/main/scripts/install.sh \
  -o /tmp/harness-lint-install.sh
less /tmp/harness-lint-install.sh
sh /tmp/harness-lint-install.sh
```

The installer selects the latest GitHub release, verifies the selected
archive against its unique SHA-256 entry in `checksums.txt`, and installs to
`$HOME/.local/bin` by default. Pin a release and choose a custom directory
with environment variables:

```sh
HARNESS_LINT_VERSION=vX.Y.Z \
HARNESS_LINT_INSTALL_DIR="$HOME/.local/bin" \
  sh /tmp/harness-lint-install.sh
```

If `cosign` is installed, the installer also downloads the current Cosign v3
bundle (`checksums.txt.sigstore.json`) and verifies it. In that mode, a
missing bundle or failed verification stops installation (fail closed). If
Cosign is not installed, the installer still requires the SHA-256 check but
warns that checksum authenticity was not verified. The exact identity and
issuer are documented in [Release and publishing](docs/releasing.md).

### `go install`

For a source build managed by Go, install the latest module version with:

```sh
go install github.com/kespineira/harness-lint/cmd/harness-lint@latest
```

For a reproducible install, pin the module version explicitly:

```sh
go install github.com/kespineira/harness-lint/cmd/harness-lint@vX.Y.Z
```

This requires Go 1.26 or newer and puts the binary in Go's usual bin
directory. `go install` is not the signed GitHub release archive path; use
the curl installer or a release artifact when you need the release checksum
and Cosign verification flow.

### GitHub releases and Linux packages

Download archives and `checksums.txt` from the
[latest GitHub release](https://github.com/kespineira/harness-lint/releases/latest).
Archives are available for macOS/Linux amd64/arm64. Linux releases also
include `.deb`, `.rpm`, and `.apk` packages; package managers, upgrade
policies, and service integration are intentionally not configured by this
project. Packages install the executable as `/usr/bin/harness-lint` and do
not install or enable a daemon.

## Supply-chain verification

GitHub Release archives have complementary integrity controls: verify the
archive's SHA-256 entry, then verify the signed checksum manifest with Cosign.
For release `vX.Y.Z`, replace `X.Y.Z` in the commands below and run them from
the directory containing the downloaded assets:

```sh
archive=harness-lint_X.Y.Z_linux_amd64.tar.gz
grep "  $archive$" checksums.txt | sha256sum -c -

cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/kespineira/harness-lint/.github/workflows/release.yml@refs/tags/vX.Y.Z" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The release workflow also publishes GitHub build provenance for each archive
and an SPDX 2.3 SBOM attestation whose subject is that exact archive. Verify
both attestations with GitHub CLI; the SBOM file must be the matching
`<archive>.spdx.json` asset:

```sh
workflow=kespineira/harness-lint/.github/workflows/release.yml
gh attestation verify "$archive" \
  --repo kespineira/harness-lint \
  --signer-workflow "$workflow" \
  --source-ref refs/tags/vX.Y.Z \
  --predicate-type https://slsa.dev/provenance/v1

sbom="$archive.spdx.json"
jq -e '.spdxVersion == "SPDX-2.3" and .SPDXID == "SPDXRef-DOCUMENT"' "$sbom"
gh attestation verify "$archive" \
  --repo kespineira/harness-lint \
  --signer-workflow "$workflow" \
  --source-ref refs/tags/vX.Y.Z \
  --predicate-type https://spdx.dev/Document/v2.3
```

Public npm packages use Trusted Publishing through GitHub OIDC and receive
npm provenance. To audit a consumer installation, use an empty temporary
directory and the reviewed npm command:

```sh
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
cd "$tmpdir"
npm init --yes >/dev/null
npm install --ignore-scripts --no-audit --no-fund --package-lock=true \
  --save-exact --registry https://registry.npmjs.org harness-lint@X.Y.Z
npm audit signatures --json --include-attestations \
  --registry https://registry.npmjs.org
```

The npm audit must show the exact package/version as verified with a SLSA
provenance attestation. A real published release is required before any
attestation result or provenance status can be claimed for a version.

## Quick start

From the project you want to inspect, refresh the local inventory and usage
evidence, then read the report:

```sh
harness-lint scan --project "$PWD"
harness-lint report
```

The scan and report use local metadata only. A representative human report
is shown below; capability names and counts are synthetic:

```text
$ harness-lint scan --project "$PWD"
Scan complete

  Runtime      Capabilities  Events  Findings
  Claude Code  1             0       0
  Codex        5             4       0

6 capabilities discovered · 4 observations imported

Run `harness-lint report` to review usage and attention items.

$ harness-lint report
Harness report

Stale threshold: 60 days

Overview
  Runtime      Installed  Used  Review  Stale
  Claude Code  1          0     1       0
  Codex        5          3     2       1

Observation
  Only local observations are shown. Missing observations do not prove non-use.
  Lifetime coverage remains unknown unless a positive capture/presence
  intersection is shown.
  Advertisement is unknown for 2 capabilities; no exposure is inferred.
  Per-session advertisement evidence is not available for every capability.

Needs attention
  Status    Runtime      Type   Name              Scope    Last used
  ! Stale   Codex        Skill  stale             project  Jun 20, 12:00
  ! Review  Claude Code  Skill  unknown-coverage  user     not observed
  ! Review  Codex        Skill  high-coverage     user     not observed
  ! Review  Codex        Skill  review-never      project  not observed

Most used
  Runtime  Type   Name          Scope    Uses  Last used
  Codex    Skill  keep          user     2     1h ago
  Codex    Skill  low-coverage  user     1     30m ago
  Codex    Skill  stale         project  1     Jun 20, 12:00

Totals
  6 installed · 3 used · 4 total observations
  0 advertised · 0 loaded · 4 invoked
  1 stale · 3 review · 2 keep

Explore
  Use `harness-lint report --all` to inspect every current capability.
  Use `harness-lint explain <name>` for evidence and rationale for one
  capability.
```

The report considers retained local evidence; its `--days` value is the stale
classification threshold, not a history-query window. Use `usage --days N`
when you need a closed recent-usage period.

The default report surfaces attention items and the most-used capabilities.
Use the navigation under [Progressive reports](#progressive-reports) to open
the full inventory, inspect one capability, or switch to the machine-facing
JSON contract.

## First run

Hook management is opt-in. Install and test hooks only for the runtime you
use, then scan and read the local report:

```sh
harness-lint hooks install claude   # or: codex
harness-lint hooks test claude
harness-lint scan --project "$PWD"
harness-lint report
```

`hooks install` makes a narrowly scoped edit to the selected runtime's local
configuration. `scan`, `report`, and hook tests read local files and write
only the local SQLite database or terminal output. The CLI is metadata-only:
it does not retain prompts, responses, tool arguments, tool results, MCP
payloads, or source text, and it does not send telemetry or contact a remote
service. The curl installer necessarily contacts GitHub to obtain the
requested release, checksums, and (when applicable) signature bundle.

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

The implemented commands are `scan`, `usage`, `report`, `explain`, `context`,
`stale`, `doctor`, `hooks`, `db`, and `ingest`. Run
`harness-lint <command> --help` for the same options shown by the binary. The
primary command forms are:

```text
harness-lint scan [options]
harness-lint usage [--json] [options]
harness-lint report [--all] [--verbose] [--json] [options]
harness-lint explain <name> [--runtime RUNTIME] [--type TYPE] [--scope SCOPE] [--verbose] [options]
harness-lint context [options]
harness-lint stale [--verbose] [--json] [options]
harness-lint doctor [--verbose] [options]
harness-lint hooks <status|test|install|uninstall> [claude|codex] [options]
harness-lint db <status|check|backup> [options]
harness-lint ingest --runtime <claude|codex> [options] < one JSON document on stdin
```

| Command | Human-facing purpose |
| --- | --- |
| `scan` | Discover capabilities and import local usage evidence. |
| `report` | Summarize usage and cleanup candidates; use `--all` to expand the inventory. |
| `explain <name>` | Explain one current capability's evidence and classification. |
| `context` | Estimate configured and on-load context footprint. |
| `usage` | Rank observed usage over a closed UTC period; `--monthly` switches to compact month totals. |
| `stale` | Show capabilities classified `STALE` by observed recency. |
| `doctor` | Find runtime configuration and discovery problems. |
| `hooks ...` | Inspect or manage runtime hook configuration. |
| `db ...` | Inspect, check, or back up the local database. |
| `ingest` | Receive one bounded metadata-only hook document on stdin. |

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
- `--runtime` and `--type` filter `usage` and disambiguate `explain` names;
  `--monthly` switches the human usage view to compact UTC month totals and
  adds monthly evidence to usage JSON. `--type mcp` includes both MCP servers
  and MCP tools while the emitted filter remains `mcp`.
- `--all` is `report`-only and includes every current capability instead of
  the default attention-focused view.
- `--verbose` is available on `scan`, `report`, `explain`, `stale`, `doctor`,
  and the `hooks`/`db` diagnostics. It adds detailed evidence or diagnostic
  sections to human output; it does not change the JSON contracts.
- `--color auto|always|never` controls ANSI status styling for human output.
  `auto` styles only a terminal, `always` forces styling, and `never` forces
  plain text. In `auto` mode, the presence of `NO_COLOR` disables styling.
  JSON output is never styled.
- `--now RFC3339` sets the observation/analysis clock, useful for reproducible
  reports and tests.

### Progressive reports

Human report output is intentionally progressive:

```sh
harness-lint report                 # attention items and most-used evidence
harness-lint report --all           # every current capability
harness-lint explain alpha          # evidence and rationale for one name
harness-lint report --json          # stable machine-readable contract
```

The default `report` keeps the first screen focused on `REVIEW` and `STALE`
items, then shows the most-used current capabilities. `report --all` expands
the capability table. `explain <name>` accepts `--runtime`, `--type`, and
`--scope` when a name is ambiguous, and `--verbose` adds exact evidence and
coverage details. The example name above is synthetic.

Human output is a presentation for people, not a stable API: headings,
wording, table layout, symbols, and wrapping may evolve. Machine users should
use `--json` where it is supported (`report`, `stale`, `usage`, `hooks status`,
`db status`, and `db check`) and consume the versioned schema described in
[Public CLI JSON schemas](docs/cli-json-schemas.md). `ingest` remains silent on
success, and `db backup` intentionally reports a bounded human destination and
size confirmation without a JSON mode.

By default `--color auto` emits plain text when stdout is redirected or is not
a TTY. Set `--color never` for an explicit plain stream, `--color always` for
ANSI styling even when redirected, or export `NO_COLOR` to disable styling in
auto mode. These controls apply to human output; JSON is plain JSON regardless
of terminal or color settings.

The default human `usage` view shows the 20 highest-use capabilities and says
when more rows exist. Narrow it with `--runtime` or `--type`, use `--monthly`
for a compact UTC month table, or use `usage --json` for the complete
machine-readable result.

### Contextual navigation

The human views point to the next bounded diagnostic when one is useful:

- Scan findings point to `harness-lint doctor`.
- Report output points to `report --all` and `explain <name>`.
- A stale view with no current inventory points back to `harness-lint scan`.
- `hooks status` shows managed entries and executable resolution; use
  `hooks test` for read-only end-to-end local health, or `hooks install` and
  `hooks uninstall` only when changing hook configuration is intended.
- `db status` shows path and bounded metadata; its next step is `db check`,
  while `db backup` creates an exclusive local copy.

The hook commands are normally invoked by an operator during setup or
diagnosis. For automation, use `hooks status --json`; the other human hook
views are presentation-only. `ingest` is normally invoked by an installed
runtime hook and is intentionally quiet on success:

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
- `report` prints per-runtime counts, attention classifications, most-used
  evidence, and usage-only observations; `--all` adds every current
  capability.
- `explain` expands one current capability's usage, exposure, coverage,
  classification rationale, and interpretation. It is human-only by design.
- `usage` queries stored invocation history over a closed UTC interval. Its
  human view ranks at most 20 capabilities, can filter by runtime/type, and
  switches to compact UTC month totals with `--monthly`, all without reading
  live runtime configuration. JSON remains complete.
- `context` prints configured baseline and on-load/body measurement summaries,
  keeping metadata and body semantics separate.
- `stale` lists only capabilities classified `STALE`; when none qualify, it
  distinguishes insufficient-evidence `REVIEW` results from stale results. A
  definition is stale only when its last observed invocation is older than the
  threshold; loaded-only observations do not establish recency, and equality
  is still `KEEP`. Stale wording is about the last observed invocation in this
  store only; it never loads a capability or claims lifetime non-use. `DEAD`
  is reserved for a future completeness signal: an absent event, source, or
  scan cannot prove terminal non-use.
- `doctor` performs discovery and prints malformed, duplicate, broken-path,
  and unresolved-command findings without opening or creating the SQLite
  database.
- `db status`, `db check`, and `db backup` operate only on the selected local
  SQLite file; they do not inspect runtime configuration or start commands.

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
gate the agent on report generation. Claude's handler is asynchronous
(`async: true` with the manager's ten-second timeout), while Codex's handler
omits `async` so it uses the default synchronous delivery supported by Codex
0.147.0 and newer command-hook releases. Codex first added asynchronous
command hooks in 0.148.0. In both runtimes `harness-lint` only records metadata
and does not turn hook delivery into a policy gate. Installing also migrates the
harness-lint-owned current-v1 Codex shape that previously included
`async: true`; user, lookalike, and stale-version entries are preserved.

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

With inventory and observations, `report`/`stale` use headings and compact
tables rather than legacy machine-oriented records. `report` keeps the default
view focused on attention items and most-used evidence; `report --all` adds
every current definition, and `explain <name>` expands one synthetic
capability into usage, exposure, coverage, rationale, and interpretation.
Values remain evidence, not a runtime billing calculation; the
[Quick start](#quick-start) shows the human shape.

In `report`, the `advertised`, `loaded`, and `invoked` totals are counts of
observed usage events. The configured-advertised total counts current installed
definitions whose configured `Advertisement` state is advertised; it is not
an observed-event count. Per-capability exposure is the configured exposure
state, so these values must not be collapsed into a single notion of “used”.

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
`null` in JSON) when no explicit advertised event was observed. The JSON field
`observed_advertised_sessions` is always emitted and is `null` when unknown;
it is never omitted. It is not reported as zero: zero would assert that an
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

The opt-in 100,000-row metadata-only storage check is:

```sh
go test -tags storage_scale ./internal/store -run '^TestStorageScale100kMetadataOnly$' -count=1 -v
```

Approximate evidence from one accepted local run was insertion `~7.3s`, usage
history `~516ms`, monthly usage `~194ms`, effective coverage `<1ms`, quick
check `~365ms`, backup `~216ms`, and a database of `~114MB`. These are local,
non-guaranteed measurements for orientation, not performance promises.

## JSON contracts and examples

These versioned JSON documents are the machine-facing interface. Human report
headings, tables, wording, symbols, and wrapping are intentionally not a
stable API; use `report --json`, `stale --json`, `usage --json`, `hooks status
--json`, `db status --json`, or `db check --json` for automation.

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
  "capabilities": [{"runtime":"codex","type":"skill","name":"alpha","scope":"user","enabled":"enabled","advertisement":"fully_advertised","status":"KEEP","confidence":"observed","coverage_confidence":"unknown","evidence_sources":["hook"],"invocation_count":1,"distinct_sessions":1}],
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
- Scan, report, context, stale, doctor, ingest, and database diagnostics are
  read-only against
  harness sources and write only local state or output. The opt-in hook
  manager is the sole configuration writer and edits only its exact owned
  entries. `usage` and `hooks test` are also read-only. Command lookup is used
  only to report whether the stable executable appears resolvable; no
  configured command is started. There is no daemon and no automatic
  retention deletion.

The project is macOS-first and Linux-compatible. It uses Go filesystem paths,
the host `os.UserConfigDir`, and local runtime conventions. The current MVP
intentionally supports only Claude Code and Codex adapters.

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

The script runs help, scan, hooks install/status/test (including verbose
diagnostics), usage (default, 90 days, runtime/type filters, monthly, and
JSON), report/stale (default, `--all`, verbose, and JSON), context, doctor
(default and verbose), and database status/check (default, verbose, and JSON).
It also checks both default and explicit database backups are nonempty. It
must receive the built binary path; it creates and removes its own disposable
tree and never targets live Claude or Codex configuration.

## License

Apache-2.0. `LICENSE` is the canonical Apache License, Version 2.0 text.
