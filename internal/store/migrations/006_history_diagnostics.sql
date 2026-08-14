-- Historical and capture-health foundation. The effective activity authority
-- remains COALESCE(source_timestamp, observed_at); the legacy timestamp column
-- is retained only for older readers. All timestamps are normalized UTC text.

CREATE TABLE capture_delivery_health (
    runtime TEXT PRIMARY KEY NOT NULL CHECK (runtime IN ('claude-code', 'codex', 'cursor')),
    last_successful_delivery TEXT,
    last_failed_delivery TEXT,
    consecutive_failure_count INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failure_count >= 0 AND consecutive_failure_count <= 32),
    last_failure_kind TEXT CHECK (last_failure_kind IS NULL OR last_failure_kind IN (
        'malformed_payload', 'unsupported_event', 'database_busy',
        'database_unavailable', 'schema_error', 'internal_error'
    ))
);

-- One canonical usage row may have multiple normalized evidence paths. Stable
-- invocation identity is represented by the shared fingerprint; provenance is
-- deliberately part of this relation's key so hook and transcript evidence
-- remain queryable without inflating invocation uses.
CREATE TABLE usage_event_evidence (
    fingerprint TEXT NOT NULL,
    provenance TEXT NOT NULL CHECK (provenance IN ('hook', 'transcript', 'import')),
    observed_at TEXT NOT NULL,
    source_timestamp TEXT,
    invocation_origin TEXT NOT NULL CHECK (invocation_origin IN ('unknown', 'model_selected', 'user_explicit')),
    source_identity TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (fingerprint, provenance),
    FOREIGN KEY (fingerprint) REFERENCES usage_events(fingerprint)
);

-- Backfill the v5 canonical source as import evidence. Existing usage rows
-- remain untouched, including their canonical fingerprint and observed time.
INSERT INTO usage_event_evidence (
    fingerprint, provenance, observed_at, source_timestamp,
    invocation_origin, source_identity
)
SELECT fingerprint, provenance, observed_at, source_timestamp,
       invocation_origin, source_identity
FROM usage_events;

-- Primary filtered aggregate path: runtime/type/name filters compose with the
-- closed effective-time interval. The pre-existing effective-time index still
-- serves date-only queries; this index is justified by the filtered aggregate
-- and monthly aggregate plans.
CREATE INDEX usage_events_history_filter_idx
ON usage_events(runtime, capability_type, capability_name,
                COALESCE(source_timestamp, observed_at), fingerprint);
