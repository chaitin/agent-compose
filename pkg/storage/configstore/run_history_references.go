package configstore

import (
	"context"
	"database/sql"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runhistory"
)

func protectRunHistoryReferences(ctx context.Context, tx *sql.Tx, id runhistory.Identity, scope *runhistory.Scope) error {
	owned := make(map[string]bool, len(scope.Runs))
	sandboxes := make(map[string]bool)
	for _, run := range scope.Runs {
		owned[run.RunID] = true
	}
	for _, sandboxID := range scope.SandboxIDs {
		sandboxes[sandboxID] = true
	}
	for sandboxID := range sandboxes {
		var referenced bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM scheduler_event WHERE linked_sandbox_id=? AND NOT(scheduler_id=? AND scheduler_run_id=?))
			OR EXISTS(SELECT 1 FROM event_sandbox_link WHERE sandbox_id=? AND NOT(scheduler_id=? AND scheduler_run_id=?))
			OR EXISTS(SELECT 1 FROM scheduler_sandbox_binding WHERE sandbox_id=?)
			OR EXISTS(SELECT 1 FROM project_run WHERE sandbox_id=? AND run_id<>? AND NOT(?='scheduler_run' AND COALESCE(scheduler_run_id,'')=? AND scheduler_id=?))`,
			sandboxID, id.SchedulerID, id.RunID, sandboxID, id.SchedulerID, id.RunID, sandboxID, sandboxID, id.RunID, id.Kind, id.RunID, id.SchedulerID).Scan(&referenced); err != nil {
			return err
		}
		if referenced {
			return domain.ClassifyError(domain.ErrReferenced, "sandbox has references outside this run history", nil)
		}
	}
	if id.Kind != runhistory.SchedulerRun {
		return nil // Standalone runs do not remove a scheduler artifact directory.
	}
	rows, err := tx.QueryContext(ctx, "SELECT run_id, logs_path, artifacts_dir FROM project_run WHERE logs_path<>'' OR artifacts_dir<>''")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var runID, logsPath, artifactsDir string
		if err := rows.Scan(&runID, &logsPath, &artifactsDir); err != nil {
			return err
		}
		if owned[runID] {
			continue
		}
		scope.ProtectedPaths = append(scope.ProtectedPaths, logsPath, artifactsDir)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, "SELECT scheduler_id, run_id, artifacts_dir FROM scheduler_run WHERE artifacts_dir<>''")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var schedulerID, runID, path string
		if err := rows.Scan(&schedulerID, &runID, &path); err != nil {
			return err
		}
		if id.Kind != runhistory.SchedulerRun || schedulerID != id.SchedulerID || runID != id.RunID {
			scope.ProtectedPaths = append(scope.ProtectedPaths, path)
		}
	}
	err = rows.Err()
	_ = rows.Close()
	return err
}
