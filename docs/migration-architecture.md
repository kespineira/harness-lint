# SQLite migration architecture

`harness-lint` keeps its local state in SQLite and uses a small custom
forward-only migration runner. SQL files are embedded in the binary with
`go:embed`; there is no external migration service, daemon, network step, or
runtime process involved in opening the database.

## Why the embedded runner is retained

The runner is intentionally narrow because the state file is local and the
CLI is daemonless. Keeping numbered SQL in the binary makes a given binary's
upgrade sequence deterministic and available offline, while the runner can
validate the complete migration set before changing a database. It also keeps
the schema boundary independent from runtime adapters and avoids requiring a
separate migration executable or service.

The runner:

1. Reads `migrations/*.sql` from the embedded filesystem.
2. Requires positive, contiguous numeric prefixes (`001_...sql`, `002_...sql`,
   and so on), rejecting gaps, duplicate versions, directories, and files
   without the `.sql` suffix.
3. Opens one SQLite transaction, creates `schema_meta` if needed, and reads
   the stored `version` (zero for a new database).
4. Rejects a database newer than the latest version supported by the binary.
5. Applies only migrations with a higher version, in numeric order, and writes
   the new version after each successful SQL step.
6. Commits the whole sequence atomically; a failed migration rolls the
   transaction back, leaving the prior schema version and data in place.

Reopening an up-to-date database is a no-op. There is no automatic downgrade.
If rollback of a local state file matters operationally, copy the file before
opening it with a newer binary.

## Current migration sequence

| Version | Purpose | Preservation boundary |
| --- | --- | --- |
| `1` | Baseline `schema_meta`, capability inventory, and metadata-only `usage_events`. | Establishes the initial local schema. |
| `2` | Corrects capability measurements and splits the legacy `mcp` value into `mcp_server`; adds explicit enabled state and metadata/body measurements. | Inventory identity, source, hash, enabled state, and observation bounds are carried forward. Legacy context/input/output measurements are not relabeled as advertised sizes; the new measurements become `unknown` when their meaning cannot be preserved. |
| `3` | Adds `advertisement_state` with an explicit `unknown` default. | Existing definitions remain present; no advertisement evidence is invented. |
| `4` | Adds `inventory_scans` and `current_inventory` markers. | Historical capability rows remain; current membership is tracked separately. |
| `5` | Adds usage-observation columns: authoritative `observed_at`, optional `source_timestamp`, `provenance`, event `schema_version`, `invocation_origin`, and `source_identity`. | The legacy `timestamp` column remains for older readers. Existing timestamps are carried into the new columns as the best available source/observed values; no direct-hook evidence is fabricated. |
| `6` | Adds bounded `capture_delivery_health`, normalized `usage_event_evidence`, and the filtered history index used by aggregate/monthly queries. | Existing usage rows remain unchanged; one evidence relation is backfilled for each v5 usage row. No event or inventory retention cleanup occurs. |

## Automatic v5 to v6 upgrade

Opening a database whose `schema_meta.version` is `5` automatically applies
`006_history_diagnostics.sql` and reaches schema `6`. The migration adds the
capture-health table and evidence relation, backfills each existing usage row
as its existing provenance (v5 rows therefore become `import` evidence when
that is the v5 value), and creates a composed-filter index. It preserves the
legacy usage row, fingerprint, normalized identities, timestamps, event type,
and capability identity; it does not duplicate the canonical event or change
its invocation count.

The v5 `timestamp` column remains available to old readers. Current readers
use `observed_at` and nullable `source_timestamp`, with effective activity time
defined as `COALESCE(source_timestamp, observed_at)`. A v5 row without a source
timestamp remains an observed-time event after upgrade. Reopening or rerunning
the migration is idempotent: the schema remains `6/6`, the existing row and
its one backfilled evidence relation remain intact, and no second evidence row
is added.

## Operational boundaries

- Migration SQL is local and forward-only; it does not contact a remote
  service or start a daemon.
- There is no automatic event retention, pruning, or deletion policy. Reports
  describe evidence still present in the local database and do not claim
  complete lifetime history.
- No broad destructive cleanup is performed. Hook uninstall removes only the
  exact current-version entries owned by the hook manager; it does not delete
  SQLite state, usage history, unrelated configuration, or lookalike handlers.
- A failed migration is rolled back by the transaction. A database with a
  version newer than the binary is rejected rather than rewritten.
- SQLite state and runtime hook configuration are separate. Changing the
  discovery `--home` does not move the database; use `--db` or `--config-dir`
  to select state explicitly.

## Data and privacy boundary

The state schema is metadata-only. It may retain normalized runtime and
capability identity, scope, source metadata, enabled/advertisement state,
content hashes, measurement confidence, UTC observation timestamps,
provenance, schema version, invocation origin, fingerprints, and one-way
SHA-256 identities. It does not retain prompts, responses, source-code text,
tool arguments or inputs, tool outputs, MCP payloads/endpoints/command
arguments, or model output. Migration steps preserve this boundary and do not
copy raw runtime payloads into the new diagnostic tables.

## Upgrade example

For a local state file, the normal upgrade is simply to open it with the new
binary:

```sh
harness-lint report --db "$HOME/Library/Application Support/harness-lint/harness-lint.db" --json
```

The command opens the file, applies any missing embedded migrations, and then
renders the report. To make rollback available, copy the SQLite file first;
the runner itself never performs a downgrade or backup deletion.
