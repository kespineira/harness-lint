-- Usage-event contract evolution. The legacy timestamp column is retained
-- for forward compatibility with MVP readers; new code uses observed_at and
-- source_timestamp explicitly.
ALTER TABLE usage_events
ADD COLUMN observed_at TEXT NOT NULL DEFAULT '';

ALTER TABLE usage_events
ADD COLUMN source_timestamp TEXT;

ALTER TABLE usage_events
ADD COLUMN provenance TEXT NOT NULL DEFAULT 'import'
CHECK (provenance IN ('hook', 'transcript', 'import'));

ALTER TABLE usage_events
ADD COLUMN schema_version INTEGER NOT NULL DEFAULT 1
CHECK (schema_version > 0);

ALTER TABLE usage_events
ADD COLUMN invocation_origin TEXT NOT NULL DEFAULT 'unknown'
CHECK (invocation_origin IN ('unknown', 'model_selected', 'user_explicit'));

ALTER TABLE usage_events
ADD COLUMN source_identity TEXT NOT NULL DEFAULT '';

-- A v4 row has no local receive marker or provenance. Preserve its usable
-- legacy timestamp as source occurrence time and also use it as the only
-- unavoidable observed_at surrogate; this is not a true receipt time, and it
-- never invents direct-hook evidence or identity.
UPDATE usage_events
SET observed_at = timestamp,
    source_timestamp = timestamp
WHERE observed_at = '';

CREATE INDEX usage_events_effective_time_idx
ON usage_events(COALESCE(source_timestamp, observed_at), fingerprint);

CREATE INDEX usage_events_runtime_capability_event_idx
ON usage_events(runtime, capability_type, capability_name, event_type, session_id);
