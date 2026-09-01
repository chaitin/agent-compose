CREATE TABLE project_run_label (
    run_id TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    FOREIGN KEY(run_id) REFERENCES project_run(run_id) ON DELETE CASCADE,
    UNIQUE(run_id, key)
);

CREATE INDEX idx_project_run_label_key_value ON project_run_label(key, value);
CREATE INDEX idx_project_run_label_run_id ON project_run_label(run_id);
