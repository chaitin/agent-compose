ALTER TABLE project_run ADD COLUMN log_incomplete INTEGER NOT NULL DEFAULT 0;
ALTER TABLE project_run ADD COLUMN execution_checkpoint_json TEXT NOT NULL DEFAULT '';
