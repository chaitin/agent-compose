ALTER TABLE project_run ADD COLUMN log_incomplete INTEGER NOT NULL DEFAULT 0;
-- The agent CLI a run uses instead of its definition's provider, so a re-attach
-- after a restart parses the agent it started. Empty uses the definition.
ALTER TABLE project_run ADD COLUMN agent_provider TEXT NOT NULL DEFAULT '';
