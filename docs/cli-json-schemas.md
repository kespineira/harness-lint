# Public CLI JSON schemas

The JSON commands are small, versioned, metadata-only interfaces. The
currently emitted versions are report/stale `2`, usage `2`, hook status `1`,
and database status/check `1`. This page describes the JSON emitted by those
commands; the Go structs behind them are implementation details and are not
the public contract. `db backup` intentionally has no JSON mode because its
output is a bounded destination/size confirmation.

All timestamps in these documents are UTC strings formatted with
`time.RFC3339Nano`. A timestamp is `null` when the corresponding observation
does not exist. Counts are evidence counts, not runtime billing or exact
context-cost calculations.

## Compatibility before 1.0

This project is pre-1.0. Consumers should check `schema_version`, ignore
unknown object members, and tolerate nullable or omitted optional members.
Additive fields are expected to be the normal compatibility direction. A
consumer must not assume that a pre-1.0 field set, ordering, or wording is
permanent; a changed meaning or incompatible shape will be called out with a
schema-version update and release notes.

Every collection is emitted as an array. Empty collections are `[]`, not
`null`, unless a field is explicitly documented as a nullable object or
scalar below.

## `report --json`

The top-level object is:

| Field | Type | Meaning |
| --- | --- | --- |
| `schema_version` | integer | Current report schema, `2`. |
| `generated_at` | string | Analysis clock, in RFC3339Nano UTC form. |
| `stale_after_days` | integer | Positive stale-policy threshold; default `60`. |
| `runtimes` | array | One aggregate row for each supported runtime. |
| `capabilities` | array | Current-inventory definitions with evidence. |
| `usage_only` | array | Observed usage keys that do not match current inventory. |
| `findings` | array | Safe deterministic diagnostics, such as duplicate names. |

`runtimes[]` contains:

| Field | Meaning |
| --- | --- |
| `runtime` | Normalized runtime name, currently `claude-code` or `codex`. |
| `installed` | Count of definitions in the latest successful current inventory. |
| `advertised` | Count of observed `advertised` events in stored history. |
| `loaded` | Count of observed `loaded` events in stored history. |
| `invoked` | Count of observed `invoked` events in stored history. |
| `configured_advertised` | Current definitions whose configured advertisement is `fully_advertised` or `name_only`; this is not an event count. |
| `invoked_last_30d` | Installed definitions with invocation evidence in the closed last-30-day window ending at `generated_at`. |
| `no_activity_observed` | Installed definitions with no loaded or invoked evidence in stored history; it does not prove lifetime non-use. |
| `usage_events` | Sum of the stored advertised, loaded, and invoked evidence counts. |

`capabilities[]` contains one current definition. The identity and inventory
fields are `runtime`, `type`, `name`, `installed`, `scope`,
`installed_scopes` (omitted when empty), `enabled`, and `advertisement`.
`metadata_exposure` and `loaded_body_footprint` are measurement objects:

```json
{
  "tokens": null,
  "confidence": "unknown",
  "basis": ""
}
```

`tokens` is an integer only when the source supplied a known measurement; it
is `null` for `unknown`. Even `exact` or `estimated` measurements describe
the measured field, never an exact runtime context cost.

The remaining capability fields are:

| Field | Meaning |
| --- | --- |
| `status` | Policy label: `KEEP`, `REVIEW`, or `STALE` in current analysis. `DEAD` is reserved for a future completeness signal and is not inferred from missing events. |
| `confidence` | Confidence in the policy finding, separate from coverage. |
| `coverage_confidence` | Confidence in lifetime observation coverage; currently `unknown` when no completeness signal exists. |
| `basis` | Privacy-safe explanation of the policy label. |
| `evidence` | Privacy-safe explanation of what the evidence can establish. |
| `evidence_sources` | Sorted distinct provenance names such as `hook`, `transcript`, and `import`. |
| `advertised` | Observed advertised-event count for this key. |
| `observed_advertised_sessions` | Distinct sessions for explicit advertised events. Omitted when no advertised-event evidence exists; it is not `0` in that case. |
| `invoked_in_advertised_sessions` | Distinct invoked sessions that intersect explicit advertised sessions. Nullable for the same reason as `observed_advertised_sessions`; this is an evidence intersection, not a percentage or cost metric. |
| `loaded` | Observed loaded-event count, independent of advertised and invoked counts. |
| `invocation_count` | Observed invoked-event count only. |
| `distinct_sessions` | Distinct normalized session identities among invoked events only. |
| `first_observed_at`, `last_observed_at` | Nullable invocation observation-time bounds. |
| `first_effective_activity_at`, `last_effective_activity_at` | Nullable invocation effective-time bounds. Effective time uses a recognized source timestamp, otherwise local `observed_at`. |
| `first_invocation_observed_at`, `last_invocation_observed_at` | Nullable invocation receive/import-time bounds. |
| `first_invocation_effective_at`, `last_invocation_effective_at` | Nullable invocation effective-time bounds. |
| `last_invocation_age` | Nullable duration string relative to `generated_at`; null when no invocation exists. |
| `last_invocation_in_future` | Whether the latest effective invocation was after the analysis clock. |
| `coverage` | Optional observation-window object; see [observation coverage](#observation-coverage). |
| `effective_coverage` | Modeled capture/presence intersection with `status` and nullable `covered_duration`; it is `unknown` without confirmed intersection. |

`advertised`, `loaded`, and `invocation_count` are intentionally independent.
An advertised observation does not become a load or invocation; a loaded
observation does not become an invocation. `configured_advertised` and the
per-capability `advertisement` state are configured exposure, not proof that a
model received or used the definition.

`invoked_in_advertised_sessions` is the observation-efficiency intersection:
the number of distinct invocation sessions that also have explicit advertised
evidence. It is not a percentage, conversion rate, token count, or runtime
cost metric. A known zero means advertised sessions were observed but none
contained an invocation; `null` means the advertised-session denominator was
not observed.

`DEAD` is intentionally absent from current policy output. A definition can
be absent from one scan, have no observed event, or fall outside the local
capture epoch without being proven dead; malformed or unavailable sources
produce the same absence. A future completeness signal would need to prove
that the relevant source was continuously observed before a terminal label
could be justified.

`usage_only[]` has the same aggregate evidence fields as a capability where
applicable: `runtime`, `type`, `name`, `advertised`,
`observed_advertised_sessions`, `invoked_in_advertised_sessions`, `loaded`, `invocation_count`,
`distinct_sessions`, `evidence_sources`, all eight nullable timestamp fields,
and optional `coverage`. It has no installed/configuration fields because its
definition is not in the current inventory. This is an explicit observation,
not invented inventory.

`findings[]` contains `runtime`, `type`, `name`, `code`, `severity`,
`confidence`, `definitions`, and `message`. `definitions` is a count; the
JSON contract never embeds source-bearing definitions, paths, or hashes.

## `stale --json`

`stale --json` uses the same `schema_version`, `generated_at`,
`stale_after_days`, `runtimes`, `capabilities`, and `findings` shapes as
`report --json`. It intentionally omits `usage_only` because stale policy
evaluates current installed definitions only. It emits JSON only: terminal
headings such as `as-of=` and `capabilities:` are not mixed into the JSON
stream.

The stale boundary is strict: an invocation exactly `N` days old is still
within the threshold; only an older invocation is `STALE`. A definition with
no loaded or invoked evidence is described as `never observed` with
insufficient lifetime activity coverage. That wording means no matching
event is present in the ingested evidence, not that a user or model never
used the capability.

## `usage --json`

The usage command queries stored history without reading live runtime
configuration. Its top-level shape is:

| Field | Type | Meaning |
| --- | --- | --- |
| `schema_version` | integer | Current usage schema, `2`. |
| `generated_at` | string | Query end/analysis clock, RFC3339Nano UTC. |
| `period` | object | Closed UTC interval. |
| `filters` | object | Normalized runtime/type filters; members are nullable. |
| `capabilities` | array | Runtime/type/name history aggregates. |

`period` contains `days`, `start`, `end`, and `inclusive`. The query interval
is exactly `[start, end]`: both boundaries are included. `start` is
`generated_at - days*24h`, and `end` is `generated_at`. Filtering compares
effective activity time (`source_timestamp` when recognized, otherwise
`observed_at`) in UTC.

`filters` always contains `runtime` and `type`; either is a string or `null`.
`claude` is normalized to `claude-code`. `type=mcp` is retained as `mcp` in
the output while matching both `mcp_server` and `mcp_tool`. Other accepted
types are `skill`, `tool`, and `agent`.

Each `capabilities[]` row contains:

| Field | Meaning |
| --- | --- |
| `runtime`, `type`, `name` | Normalized capability identity. |
| `installed` | Whether the key exists in current inventory; usage-only rows are `false`. |
| `installed_scopes` | Sorted current scopes; `[]` for usage-only rows. |
| `uses` | Invoked-event count only. |
| `distinct_sessions` | Distinct normalized sessions among invocations. |
| `first_observed_at`, `last_observed_at` | Nullable invocation observation-time bounds. |
| `first_effective_activity_at`, `last_effective_activity_at` | Nullable invocation effective-time bounds. |
| `provenance` | `{ "hook": N, "transcript": N, "import": N, "sources": [...] }`; source subtotals can each include the same stable invocation when evidence arrived through multiple paths, while `uses` remains deduplicated. |
| `advertised_observations` | Advertised-event count, independent of `uses`. |
| `advertised_sessions` | Nullable distinct advertised-event session count. `null` means no explicit advertised-event evidence, not zero sessions. |
| `invoked_in_advertised_sessions` | Nullable distinct-session intersection between invocation and advertised evidence; an observation-efficiency count, never a percentage. |
| `loaded_observations` | Loaded-event count, independent of advertised and invoked counts. |
| `observation_only_coverage` | Nullable observation-window object; it is not a completeness or continuity claim. |
| `effective_coverage` | Modeled capture/presence intersection with `status` and nullable `covered_duration`; it is not implied by observation-only windows. |
| `monthly` | Omitted unless `--monthly` is supplied; then it contains UTC calendar-month buckets. |

With `--monthly`, every month touched by the period is present, including
zero-use months. A monthly row contains `month` (the first instant of the
month in RFC3339Nano UTC), `uses`, and `distinct_sessions`. Monthly rows are
subtotals of invocations only; they do not turn advertised or loaded evidence
into usage.

### Observation coverage

When present, a coverage object has these nullable fields:

```json
{
  "first_inventory_observed_at": null,
  "last_inventory_observed_at": null,
  "first_usage_observed_at": null,
  "last_usage_observed_at": null,
  "first_direct_hook_observed_at": null,
  "last_direct_hook_observed_at": null
}
```

Report/stale coverage may omit null members inside their optional `coverage`
object. Usage coverage always uses the six members above and may set each to
`null`. These windows say when this local store has evidence from each path;
they do not prove continuous collection, complete lifetime history, or true
runtime delivery. Missing, malformed, unsupported, or uninstalled sources
can make coverage incomplete. `effective_coverage` is a separate modeled
field: it is the intersection of confirmed direct-capture epochs and
capability-presence epochs, and is `unknown` when no positive intersection is
proven. An observation window never upgrades coverage confidence by itself.

## `hooks status --json`

`hooks status --json` emits:

| Field | Type | Meaning |
| --- | --- | --- |
| `schema_version` | integer | Current hook-status schema, `1`. |
| `generated_at` | string | Status clock, RFC3339Nano UTC. |
| `runtimes` | array | One object for each selected runtime. |

Each `runtimes[]` object contains `runtime`, `status`, `config_path`,
`config_exists`, `managed`, `managed_entries`, `binary`, `inline_hooks`,
`trust_review`, and `warnings`.

The runtime-level fields have these meanings:

| Field | Meaning |
| --- | --- |
| `runtime` | Normalized selected runtime name. |
| `status` | `installed`, `not installed`, `partially installed`, `malformed`, `configuration not found`, or `unsupported`. |
| `config_path` | Runtime-owned configuration path inspected for status. |
| `config_exists` | Whether that configuration path exists. |
| `managed` | Aggregate owned-entry state: `installed`, `not installed`, `partial`, or `stale`. |
| `inline_hooks` | Whether separately detected Codex inline hooks are present. |
| `warnings` | Safe warning strings; emitted as `[]` when empty. |

`managed_entries[]` contains `event`, `state`, `exact_handlers`,
`partial_handlers`, and `lookalike_handlers`. `binary` contains `name`,
`resolved`, `resolved_path`, and `error`. The generated hook command always
uses the stable PATH name `harness-lint`; `resolved_path` is diagnostic only.
When the executable is unavailable, `resolved` is `false` and install refuses
to mutate configuration.

`managed_entries[].state` is one of `installed`, `not installed`, `partial`,
or `stale`. Handler counts distinguish exact owned handlers from partial or
lookalike handlers; lookalikes are never removed by the manager. `binary.name`
is normally `harness-lint`; `resolved_path` and `error` are empty strings when
there is no corresponding diagnostic.

`trust_review` contains `required` and `limitation`. In particular, writing
Codex `hooks.json` cannot grant the trust/review decision made by the Codex
UI. `inline_hooks` reports separately detected Codex inline hooks; the manager
does not enable or execute them. `warnings` is an array and is empty when no
warning was produced.

`hooks test` deliberately has no JSON mode. It is a read-only, human-facing
health check that reports bounded component states and capture delivery state.
Its synthetic self-test proves local ingest/SQLite behavior, but not true
runtime delivery when no activity has occurred.

## `db status --json` and `db check --json`

Database diagnostics are local-file operations and do not inspect runtime
configuration. Both schemas are currently version `1`.

`db status --json` contains:

| Field | Type | Meaning |
| --- | --- | --- |
| `schema_version` | integer | Database-status schema, `1`. |
| `generated_at` | string | Diagnostic clock, RFC3339Nano UTC. |
| `path` | string | Selected SQLite path; operator-visible diagnostic metadata. |
| `schema` | object | `{ "current": N, "latest": N }` migration versions. |
| `size_bytes` | integer or null | Main database file size; null for in-memory databases. |
| `usage_event_count` | integer | Retained canonical usage-event rows. |
| `oldest_observed_at`, `latest_observed_at` | string or null | Local receive/observation bounds, not source occurrence bounds. |
| `integrity_checked` | boolean | Always `false` for status; use `db check` for integrity diagnostics. |

`db check --json` contains `schema_version`, `generated_at`, `healthy`,
`quick_check`, `foreign_key_check`, `schema`, and `issues`. Each check is one
of `ok`, `issues`, or `unavailable`; each issue contains only an allow-listed
check name. The check is read-only and never migrates, repairs, checkpoints,
restores, or deletes the database. A failed check exits non-zero without
echoing SQLite details or local payloads.

`db backup` has no JSON representation. It uses SQLite's Online Backup API to
write a consistent, exclusive destination and prints only the destination and
size. It never overwrites an existing destination and does not implement
restore or pruning.

## Compatibility diagnostics

`doctor` and `hooks test` expose bounded human-readable compatibility lines,
not a JSON contract. A detected runtime version is evaluated against the
version facts in the conformance metadata; because this repository does not
validate a live runtime version, the current validation basis yields
`status=unknown`. Missing executables, command failures, and unparsable output
are separate bounded detection states. Raw command output and errors are not
persisted or emitted.

## Ingest input and success behavior

`ingest` consumes exactly one runtime-specific metadata-only hook document on
stdin and emits no output on success. Its normalized event contract records
only the allow-listed identity and event metadata needed for one observation;
session/project/source identities are one-way normalized, and prompts,
arguments, results, and arbitrary hook fields are not retained. Malformed or
unsupported input produces a bounded error without echoing stdin. The
runtime-specific accepted envelope and evidence boundaries are defined by the
[accepted runtime conformance matrix](runtime-conformance.md), which does not
claim Claude/Codex telemetry parity.

## Privacy boundary

Report, stale, and usage JSON contain normalized capability names, counts,
safe policy wording, timestamps, measurement confidence, and bounded
provenance. They do not expose prompts, model responses, source-code text,
tool arguments or inputs, tool outputs, MCP payloads/endpoints/command
arguments, raw session or project identities, source paths, or fingerprints.
`hooks status --json` intentionally includes its local `config_path` and
diagnostic `resolved_path`/`error` fields for operator troubleshooting, but
does not include configuration contents or run the configured command. A
local report is evidence from this store, not a data export or a runtime
billing calculation.

Database status/check intentionally expose the selected local `path` and
bounded migration/count metadata for operator diagnostics; they do not expose
configuration contents, inventory source paths, raw event identities, or
command output. A backup is a local copy chosen by the operator, not an
upload or automatic retention mechanism.
