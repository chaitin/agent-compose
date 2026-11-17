package configstore

import (
	"context"
	"database/sql"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runhistory"
)

func protectRunHistoryReferences(ctx context.Context, tx *sql.Tx, id runhistory.Identity, scope *runhistory.Scope) error {
	sandboxes := make(map[string]bool)
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
	// Writers only accept paths inside a scheduler run's directory for that
	// run itself (validateHistoryArtifactPaths), so another record can point
	// into this directory only through rows of the same scheduler. Check those
	// lexically through the scheduler indexes: the cost is bounded by this
	// scheduler, not by all history. Removal never follows symlinks, so an
	// alias elsewhere cannot make it delete anything outside this directory.
	// instr on the run id narrows candidates cheaply; exact segment matching
	// then decides whether a path is inside this run's or its pending directory.
	rows, err := tx.QueryContext(ctx, `SELECT logs_path, artifacts_dir FROM project_run WHERE project_id=? AND scheduler_id=? AND COALESCE(scheduler_run_id,'')<>?
			AND (instr(logs_path, ?)>0 OR instr(artifacts_dir, ?)>0)
		UNION ALL SELECT '', artifacts_dir FROM scheduler_run WHERE scheduler_id=? AND run_id<>? AND instr(artifacts_dir, ?)>0`,
		id.ProjectID, id.SchedulerID, id.RunID, id.RunID, id.RunID, id.SchedulerID, id.RunID, id.RunID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	referenced := false
	for rows.Next() && !referenced {
		var logsPath, artifactsDir string
		if err := rows.Scan(&logsPath, &artifactsDir); err != nil {
			return err
		}
		referenced = insideSchedulerRunDir(logsPath, id.SchedulerID, id.RunID) || insideSchedulerRunDir(artifactsDir, id.SchedulerID, id.RunID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if referenced {
		return domain.ClassifyError(domain.ErrReferenced, "scheduler artifacts are referenced by another run", nil)
	}
	return nil
}
