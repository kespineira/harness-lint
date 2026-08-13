CREATE TABLE IF NOT EXISTS schema_meta (
    key TEXT PRIMARY KEY NOT NULL,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS capabilities (
    runtime TEXT NOT NULL,
    capability_type TEXT NOT NULL,
    name TEXT NOT NULL,
    scope TEXT NOT NULL,
    source TEXT NOT NULL,
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    hash TEXT NOT NULL,
    context_value INTEGER NOT NULL,
    context_confidence TEXT NOT NULL,
    context_basis TEXT NOT NULL,
    input_tokens_value INTEGER NOT NULL,
    input_tokens_confidence TEXT NOT NULL,
    input_tokens_basis TEXT NOT NULL,
    output_tokens_value INTEGER NOT NULL,
    output_tokens_confidence TEXT NOT NULL,
    output_tokens_basis TEXT NOT NULL,
    first_seen TEXT NOT NULL,
    last_seen TEXT NOT NULL,
    PRIMARY KEY (runtime, capability_type, name, scope)
);

CREATE TABLE IF NOT EXISTS usage_events (
    timestamp TEXT NOT NULL,
    runtime TEXT NOT NULL,
    -- Values are pre-normalized stable identifiers or one-way hashes. Raw
    -- paths, prompts, responses, and conversation data do not belong here.
    session_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    capability_type TEXT NOT NULL,
    capability_name TEXT NOT NULL,
    event_type TEXT NOT NULL,
    fingerprint TEXT PRIMARY KEY NOT NULL
);

CREATE INDEX IF NOT EXISTS usage_events_time_idx ON usage_events(timestamp, fingerprint);
