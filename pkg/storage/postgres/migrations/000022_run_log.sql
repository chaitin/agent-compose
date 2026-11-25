-- Run log timeline: an empty log_format marks a run whose logs_path is a legacy
-- text transcript; summary_json is folded from the run log when the run
-- completes; log_seq points a conversation message at its entry in the run log
-- (0 for messages written before run logs existed).
ALTER TABLE project_run ADD COLUMN log_format TEXT COLLATE "C" NOT NULL DEFAULT '';
ALTER TABLE project_run ADD COLUMN summary_json TEXT COLLATE "C" NOT NULL DEFAULT '';
ALTER TABLE project_run_event ADD COLUMN log_seq BIGINT NOT NULL DEFAULT 0;
