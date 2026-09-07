CREATE TABLE project_run_label (
    run_id TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    FOREIGN KEY(run_id) REFERENCES project_run(run_id) ON DELETE CASCADE,
    UNIQUE(run_id, key)
);

-- No explicit indexes: the implicit UNIQUE(run_id, key) index already serves
-- both query shapes. The label filter searches it on (run_id, key), and
-- loadProjectRunLabels searches it on the run_id prefix, so a separate
-- run_id index would only duplicate that prefix and add write amplification.
