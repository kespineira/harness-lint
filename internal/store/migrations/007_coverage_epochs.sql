-- Confirmed runtime capture and capability-presence intervals. These tables
-- intentionally do not derive intervals from the flattened Milestone 3
-- first_seen/last_seen values; only successful post-migration observations
-- may create epoch rows.
CREATE TABLE capture_epochs (
    id INTEGER PRIMARY KEY,
    runtime TEXT NOT NULL CHECK (runtime IN ('claude-code', 'codex', 'cursor')),
    started_at TEXT NOT NULL,
    ended_at TEXT,
    start_reason TEXT NOT NULL CHECK (start_reason = 'confirmed_direct_delivery'),
    end_reason TEXT CHECK (end_reason IS NULL OR end_reason IN ('confirmed_capture_failure', 'managed_hook_uninstall')),
    CHECK ((ended_at IS NULL AND end_reason IS NULL) OR (ended_at IS NOT NULL AND end_reason IS NOT NULL)),
    CHECK (ended_at IS NULL OR ended_at > started_at)
);

CREATE UNIQUE INDEX capture_epochs_one_open_runtime_idx
ON capture_epochs(runtime) WHERE ended_at IS NULL;

CREATE INDEX capture_epochs_runtime_time_idx
ON capture_epochs(runtime, started_at, ended_at);

CREATE TABLE capability_presence_epochs (
    id INTEGER PRIMARY KEY,
    runtime TEXT NOT NULL CHECK (runtime IN ('claude-code', 'codex', 'cursor')),
    capability_type TEXT NOT NULL,
    capability_name TEXT NOT NULL,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    CHECK (ended_at IS NULL OR ended_at > started_at)
);

CREATE UNIQUE INDEX capability_presence_epochs_one_open_key_idx
ON capability_presence_epochs(runtime, capability_type, capability_name)
WHERE ended_at IS NULL;

CREATE INDEX capability_presence_epochs_key_time_idx
ON capability_presence_epochs(runtime, capability_type, capability_name, started_at, ended_at);
