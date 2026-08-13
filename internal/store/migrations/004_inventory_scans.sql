CREATE TABLE inventory_scans (
    runtime TEXT PRIMARY KEY NOT NULL CHECK (runtime IN ('claude-code', 'codex', 'cursor')),
    observed_at TEXT NOT NULL
);

CREATE TABLE current_inventory (
    runtime TEXT NOT NULL CHECK (runtime IN ('claude-code', 'codex', 'cursor')),
    capability_type TEXT NOT NULL,
    name TEXT NOT NULL,
    scope TEXT NOT NULL,
    source TEXT NOT NULL,
    PRIMARY KEY (runtime, capability_type, name, scope, source)
);
