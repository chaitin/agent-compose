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
	// Recorded artifact paths are derived from identities: project runs live
	// in their sandbox and a scheduler run owns exactly
	// <root>/schedulers/<scheduler>/runs/<run>, which artifact validation
	// checks before removal. No writer records one run's artifacts inside
	// another run's directory, so no path scan is needed here.
	return nil
}
