ALTER TABLE project_run ADD COLUMN log_incomplete BIGINT NOT NULL DEFAULT 0;
ALTER TABLE project_run ADD COLUMN execution_checkpoint_json TEXT COLLATE "C" NOT NULL DEFAULT '';
