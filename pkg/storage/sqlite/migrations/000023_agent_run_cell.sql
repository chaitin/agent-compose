-- Execution identity must survive changes to a run's log location. Older
-- text runs still resolve their cell from logs_path at the recovery boundary.
ALTER TABLE project_run ADD COLUMN agent_cell_id TEXT NOT NULL DEFAULT '';
