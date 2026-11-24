package configstore

import (
	"context"
	"database/sql"
	"slices"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/project"
	"github.com/chaitin/agent-compose/ent/dao/projectrun"
	"github.com/chaitin/agent-compose/ent/dao/projectruncompletion"
	"github.com/chaitin/agent-compose/ent/dao/projectscheduler"
	"github.com/chaitin/agent-compose/ent/dao/schedulerrun"
	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runhistory"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

// loadRunHistoryScope reads the history scope inside the caller's run-history
// fence transaction. The single-table reads go through the ent client bound to
// that transaction; the locking, the no-op write fence, and the multi-table
// reference probes stay raw SQL in the callers/helpers.
func loadRunHistoryScope(ctx context.Context, client *dao.Client, tx *sql.Tx, id runhistory.Identity) (runhistory.Scope, error) {
	scope := runhistory.Scope{}
	projectExists, err := client.Project.Query().Where(project.IDEQ(id.ProjectID)).Exist(ctx)
	if err != nil {
		return scope, err
	}
	if !projectExists {
		return scope, domain.ClassifyError(domain.ErrNotFound, "project not found", sql.ErrNoRows)
	}
	switch id.Kind {
	case runhistory.Run:
		// A caller cannot erase a scheduler's child independently and leave
		// the parent history pointing at missing execution evidence.
		schedulerRunExists, err := client.SchedulerRun.Query().Where(schedulerrun.IDEQ(id.RunID)).Exist(ctx)
		if err != nil {
			return scope, err
		}
		if schedulerRunExists {
			return scope, domain.ClassifyError(domain.ErrFailedPrecondition, "identity refers to a scheduler run", nil)
		}
	case runhistory.SchedulerRun:
		scheduler, err := client.ProjectScheduler.Query().
			Select(projectscheduler.FieldProjectID).
			Where(projectscheduler.IDEQ(id.SchedulerID)).
			Only(ctx)
		if err != nil {
			if dao.IsNotFound(err) {
				return scope, domain.ClassifyError(domain.ErrNotFound, "scheduler not found", sql.ErrNoRows)
			}
			return scope, err
		}
		if scheduler.ProjectID != id.ProjectID {
			return scope, domain.ClassifyError(domain.ErrNotFound, "scheduler not found in project", nil)
		}
		run := domain.SchedulerRunSummary{ID: id.RunID, SchedulerID: id.SchedulerID}
		schedulerRun, err := client.SchedulerRun.Query().
			Select(schedulerrun.FieldSchedulerID, schedulerrun.FieldStatus, schedulerrun.FieldArtifactsDir).
			Where(schedulerrun.IDEQ(id.RunID)).
			Only(ctx)
		if err != nil && !dao.IsNotFound(err) {
			return scope, err
		}
		if err == nil {
			if schedulerRun.SchedulerID != id.SchedulerID {
				return scope, domain.ClassifyError(domain.ErrNotFound, "run not found in scheduler", nil)
			}
			if !schedulers.SchedulerRunStatusIsTerminal(schedulerRun.Status) {
				return scope, domain.ClassifyError(domain.ErrFailedPrecondition, "scheduler run is not terminal", nil)
			}
			run.Status, run.ArtifactsDir = schedulerRun.Status, schedulerRun.ArtifactsDir
		}
		// Even after a prior prune lost the row, this exact scheduler identity
		// still lets a retry remove its canonical directory and direct links.
		scope.SchedulerRun = &run
	}
	query := client.ProjectRun.Query()
	if id.Kind == runhistory.SchedulerRun {
		query = query.Where(projectrun.Or(projectrun.IDEQ(id.RunID), projectrun.SchedulerRunIDEQ(id.RunID)))
	} else {
		query = query.Where(projectrun.IDEQ(id.RunID))
	}
	entities, err := query.
		Select(
			projectrun.FieldID,
			projectrun.FieldProjectID,
			projectrun.FieldSchedulerID,
			projectrun.FieldSchedulerRunID,
			projectrun.FieldStatus,
			projectrun.FieldSandboxID,
			projectrun.FieldLogsPath,
			projectrun.FieldArtifactsDir,
		).
		Order(projectrun.ByID()).
		All(ctx)
	if err != nil {
		return scope, err
	}
	for _, entity := range entities {
		run := domain.ProjectRunRecord{
			RunID:        entity.ID,
			ProjectID:    entity.ProjectID,
			SchedulerID:  entity.SchedulerID,
			Status:       entity.Status,
			SandboxID:    entity.SandboxID,
			LogsPath:     entity.LogsPath,
			ArtifactsDir: entity.ArtifactsDir,
		}
		if entity.SchedulerRunID != nil {
			run.SchedulerRunID = *entity.SchedulerRunID
		}
		scope.Runs = append(scope.Runs, run)
		if run.SandboxID != "" {
			scope.SandboxIDs = append(scope.SandboxIDs, canonicalSandboxID(run.SandboxID))
		}
	}
	for _, run := range scope.Runs {
		if run.ProjectID != id.ProjectID {
			return scope, domain.ClassifyError(domain.ErrNotFound, "run not found in project", nil)
		}
		if id.Kind == runhistory.Run && run.SchedulerRunID != "" {
			return scope, domain.ClassifyError(domain.ErrReferenced, "remove the owning scheduler run history instead", nil)
		}
		if id.Kind == runhistory.SchedulerRun && (run.SchedulerRunID != id.RunID || run.SchedulerID != id.SchedulerID) {
			return scope, domain.ClassifyError(domain.ErrReferenced, "run identity has an inconsistent scheduler owner", nil)
		}
		if !projectRunStatusTerminal(run.Status) {
			return scope, domain.ClassifyError(domain.ErrFailedPrecondition, "project run is not terminal", nil)
		}
		pending, err := client.ProjectRunCompletion.Query().Where(projectruncompletion.IDEQ(run.RunID)).Exist(ctx)
		if err != nil {
			return scope, err
		}
		if pending {
			return scope, domain.ClassifyError(domain.ErrFailedPrecondition, "run completion is still pending", nil)
		}
	}
	if id.Kind == runhistory.SchedulerRun {
		// Kept as SQL: UNION of scheduler_event and event_sandbox_link
		// sandbox links that ent cannot express as one read.
		links, err := tx.QueryContext(ctx, "SELECT linked_sandbox_id FROM scheduler_event WHERE scheduler_id=? AND scheduler_run_id=? AND linked_sandbox_id<>'' UNION SELECT sandbox_id FROM event_sandbox_link WHERE scheduler_id=? AND scheduler_run_id=? AND sandbox_id<>''", id.SchedulerID, id.RunID, id.SchedulerID, id.RunID)
		if err != nil {
			return scope, err
		}
		defer func() { _ = links.Close() }()
		for links.Next() {
			var sandboxID string
			if err := links.Scan(&sandboxID); err != nil {
				return scope, err
			}
			scope.SandboxIDs = append(scope.SandboxIDs, canonicalSandboxID(sandboxID))
		}
		err = links.Err()
		_ = links.Close()
		if err != nil {
			return scope, err
		}
	}
	if err := protectRunHistoryReferences(ctx, tx, id, &scope); err != nil {
		return scope, err
	}
	slices.Sort(scope.SandboxIDs)
	scope.SandboxIDs = slices.Compact(scope.SandboxIDs)
	return scope, nil
}

// canonicalSandboxID folds the legacy "sha256:" form so directory, archive,
// and reference checks all see the identity the sandbox store uses on disk.
func canonicalSandboxID(id string) string {
	if hash, err := identity.Hash(id); err == nil {
		return hash
	}
	return id
}
