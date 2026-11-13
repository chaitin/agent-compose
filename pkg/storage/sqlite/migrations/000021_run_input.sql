CREATE TABLE project_run_input (
    run_id TEXT PRIMARY KEY REFERENCES project_run(run_id) ON DELETE CASCADE,
    preparation_json TEXT NOT NULL
);
