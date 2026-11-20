-- See docs/design/run-log-timeline.zh-CN.md. An empty log_format marks a run
-- whose logs_path is a legacy text transcript; summary_json is folded from the
-- run log when the run completes; log_seq points a conversation message at its
-- entry in the run log (0 for messages written before run logs existed).
ALTER TABLE project_run ADD COLUMN log_format TEXT NOT NULL DEFAULT '';
ALTER TABLE project_run ADD COLUMN summary_json TEXT NOT NULL DEFAULT '';
ALTER TABLE project_run_event ADD COLUMN log_seq INTEGER NOT NULL DEFAULT 0;
