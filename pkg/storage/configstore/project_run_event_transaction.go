package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
)

func (s *projectStore) CreateProjectRunWithEvents(ctx context.Context, run ProjectRunRecord, events []domain.ProjectRunEventRecord) (ProjectRunRecord, error) {
	run, err := projects.NormalizeRunRecord(run)
	if err != nil {
		return ProjectRunRecord{}, err
	}
	now := time.Now().UTC()
	run.CreatedAt = now
	run.UpdatedAt = now
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectRunRecord{}, fmt.Errorf("begin create project run transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateRunEventBatch(run.RunID, events); err != nil {
		return ProjectRunRecord{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO project_run(
		run_id, project_id, project_name, project_revision, agent_name, managed_agent_id, source, scheduler_id, trigger_id, status,
		sandbox_id, exit_code, error, prompt, output, result_json, logs_path, artifacts_dir, cleanup_error, driver, image_ref,
		started_at, completed_at, duration_ms, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(run_id) DO NOTHING`,
		run.RunID, run.ProjectID, run.ProjectName, run.ProjectRevision, run.AgentName, run.ManagedAgentID, run.Source, run.SchedulerID, run.TriggerID, run.Status,
		run.SandboxID, run.ExitCode, run.Error, run.Prompt, run.Output, run.ResultJSON, run.LogsPath, run.ArtifactsDir, run.CleanupError, run.Driver, run.ImageRef,
		domain.NonZeroTimeUnixMilli(run.StartedAt), domain.NonZeroTimeUnixMilli(run.CompletedAt), run.DurationMs, run.CreatedAt.Unix(), run.UpdatedAt.Unix())
	if err != nil {
		return ProjectRunRecord{}, fmt.Errorf("insert project run %s: %w", run.RunID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return ProjectRunRecord{}, fmt.Errorf("inspect project run insert %s: %w", run.RunID, err)
	}
	if rows == 0 {
		existing, loadErr := getProjectRunTx(ctx, tx, run.RunID)
		if loadErr != nil {
			return ProjectRunRecord{}, fmt.Errorf("load existing project run %s: %w", run.RunID, loadErr)
		}
		run = existing
	}
	if _, _, err := appendProjectRunEventsTx(ctx, tx, events); err != nil {
		return ProjectRunRecord{}, fmt.Errorf("append create project run events: %w", err)
	}
	stored, err := getProjectRunTx(ctx, tx, run.RunID)
	if err != nil {
		return ProjectRunRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProjectRunRecord{}, fmt.Errorf("commit create project run transaction: %w", err)
	}
	return stored, nil
}

func (s *projectStore) UpdateProjectRunWithEvents(ctx context.Context, run ProjectRunRecord, events []domain.ProjectRunEventRecord) (ProjectRunRecord, error) {
	run, err := projects.NormalizeRunRecord(run)
	if err != nil {
		return ProjectRunRecord{}, err
	}
	now := time.Now().UTC()
	if err := validateRunEventBatch(run.RunID, events); err != nil {
		return ProjectRunRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectRunRecord{}, fmt.Errorf("begin update project run transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE project_run SET
		project_id = ?, project_name = ?, project_revision = ?, agent_name = ?, managed_agent_id = ?, source = ?, scheduler_id = ?, trigger_id = ?, status = ?,
		sandbox_id = ?, exit_code = ?, error = ?, prompt = ?, output = ?, result_json = ?, logs_path = ?, artifacts_dir = ?, cleanup_error = ?, driver = ?, image_ref = ?,
		started_at = ?, completed_at = ?, duration_ms = ?, updated_at = ? WHERE run_id = ?`,
		run.ProjectID, run.ProjectName, run.ProjectRevision, run.AgentName, run.ManagedAgentID, run.Source, run.SchedulerID, run.TriggerID, run.Status,
		run.SandboxID, run.ExitCode, run.Error, run.Prompt, run.Output, run.ResultJSON, run.LogsPath, run.ArtifactsDir, run.CleanupError, run.Driver, run.ImageRef,
		domain.NonZeroTimeUnixMilli(run.StartedAt), domain.NonZeroTimeUnixMilli(run.CompletedAt), run.DurationMs, now.Unix(), run.RunID)
	if err != nil {
		return ProjectRunRecord{}, fmt.Errorf("update project run %s: %w", run.RunID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return ProjectRunRecord{}, fmt.Errorf("inspect project run update %s: %w", run.RunID, err)
	}
	if rows == 0 {
		return ProjectRunRecord{}, domain.ResourceError(domain.ErrNotFound, "project run", run.RunID, fmt.Sprintf("project run %s not found", run.RunID), nil)
	}
	if _, _, err := appendProjectRunEventsTx(ctx, tx, events); err != nil {
		return ProjectRunRecord{}, fmt.Errorf("append update project run events: %w", err)
	}
	updated, err := getProjectRunTx(ctx, tx, run.RunID)
	if err != nil {
		return ProjectRunRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProjectRunRecord{}, fmt.Errorf("commit update project run transaction: %w", err)
	}
	return updated, nil
}

func validateRunEventBatch(runID string, events []domain.ProjectRunEventRecord) error {
	for _, event := range events {
		if event.RunID != runID {
			return fmt.Errorf("run event run id %q does not match project run %q", event.RunID, runID)
		}
	}
	return nil
}

func getProjectRunTx(ctx context.Context, tx *sql.Tx, runID string) (ProjectRunRecord, error) {
	return projects.ScanProjectRun(tx.QueryRowContext(ctx, projects.SelectProjectRunSQL()+` WHERE run_id = ?`, runID).Scan)
}
