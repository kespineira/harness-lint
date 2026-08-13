CREATE TABLE capabilities_v2 (
    runtime TEXT NOT NULL,
    capability_type TEXT NOT NULL,
    name TEXT NOT NULL,
    scope TEXT NOT NULL,
    source TEXT NOT NULL,
    enabled_state TEXT NOT NULL CHECK (enabled_state IN ('enabled', 'disabled', 'unknown')),
    hash TEXT NOT NULL,
    metadata_tokens_value INTEGER NOT NULL,
    metadata_tokens_confidence TEXT NOT NULL,
    metadata_tokens_basis TEXT NOT NULL,
    body_tokens_value INTEGER NOT NULL,
    body_tokens_confidence TEXT NOT NULL,
    body_tokens_basis TEXT NOT NULL,
    first_seen TEXT NOT NULL,
    last_seen TEXT NOT NULL,
    PRIMARY KEY (runtime, capability_type, name, scope, source)
);

-- Legacy context/input/output measurements cannot be represented as
-- advertised metadata/body sizes without making an unsupported runtime-cost
-- claim. Preserve the inventory row and mark both new measurements unknown.
INSERT INTO capabilities_v2 (
    runtime, capability_type, name, scope, source, enabled_state, hash,
    metadata_tokens_value, metadata_tokens_confidence, metadata_tokens_basis,
    body_tokens_value, body_tokens_confidence, body_tokens_basis,
    first_seen, last_seen
)
SELECT
    runtime,
    CASE capability_type WHEN 'mcp' THEN 'mcp_server' ELSE capability_type END,
    name,
    scope,
    source,
    CASE enabled WHEN 1 THEN 'enabled' ELSE 'disabled' END,
    hash,
    0, 'unknown', '',
    0, 'unknown', '',
    first_seen,
    last_seen
FROM capabilities;

DROP TABLE capabilities;
ALTER TABLE capabilities_v2 RENAME TO capabilities;

-- Usage events are metadata-only too, and the old unsplit MCP value has the
-- same server meaning when upgrading an existing database.
UPDATE usage_events SET capability_type = 'mcp_server' WHERE capability_type = 'mcp';
