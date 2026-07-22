CREATE TABLE loader_binding_with_config_hash (
    loader_id TEXT NOT NULL,
    trigger_id TEXT NOT NULL DEFAULT '',
    sandbox_id TEXT NOT NULL,
    sandbox_config_hash TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    PRIMARY KEY(loader_id, trigger_id)
);

INSERT INTO loader_binding_with_config_hash(
    loader_id, trigger_id, sandbox_id, created_at, updated_at
)
SELECT loader_id, trigger_id, sandbox_id, created_at, updated_at
FROM loader_binding;

DROP TABLE loader_binding;
ALTER TABLE loader_binding_with_config_hash RENAME TO loader_binding;
