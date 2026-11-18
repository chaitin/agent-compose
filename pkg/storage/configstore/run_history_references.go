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
		// Rows may carry the canonical or the legacy "sha256:" form of the
		// same sandbox; match both. The scheduler_event predicate repeats the
		// partial index condition so SQLite can use it inside the write fence.
		// scheduler_sandbox_binding holds one row per trigger and stays small.
		legacy := "sha256:" + sandboxID
		var referenced bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM scheduler_event WHERE linked_sandbox_id IN (?, ?) AND linked_sandbox_id<>'' AND NOT(scheduler_id=? AND scheduler_run_id=?))
			OR EXISTS(SELECT 1 FROM event_sandbox_link WHERE sandbox_id IN (?, ?) AND NOT(scheduler_id=? AND scheduler_run_id=?))
			OR EXISTS(SELECT 1 FROM scheduler_sandbox_binding WHERE sandbox_id IN (?, ?))
			OR EXISTS(SELECT 1 FROM project_run WHERE sandbox_id IN (?, ?) AND run_id<>? AND NOT(?='scheduler_run' AND COALESCE(scheduler_run_id,'')=? AND scheduler_id=?))`,
			sandboxID, legacy, id.SchedulerID, id.RunID,
			sandboxID, legacy, id.SchedulerID, id.RunID,
			sandboxID, legacy,
			sandboxID, legacy, id.RunID, id.Kind, id.RunID, id.SchedulerID).Scan(&referenced); err != nil {
			return err
		}
		if referenced {
			return domain.ClassifyError(domain.ErrReferenced, "sandbox has references outside this run history", nil)
		}
	}
	// No other record can point into the scheduler run directory: every
	// history writer rejects paths inside another scheduler run's directory
	// (validateHistoryArtifactPaths), so no path scan is needed here.
	return nil
}
