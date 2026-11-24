package configstore

import (
	"context"
	"database/sql"
	"slices"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/eventsandboxlink"
	"github.com/chaitin/agent-compose/ent/dao/project"
	"github.com/chaitin/agent-compose/ent/dao/projectrun"
	"github.com/chaitin/agent-compose/ent/dao/projectruncompletion"
	"github.com/chaitin/agent-compose/ent/dao/projectscheduler"
	"github.com/chaitin/agent-compose/ent/dao/schedulerevent"
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
func loadRunHistoryScope(ctx context.Context, client *dao.Client, id runhistory.Identity) (runhistory.Scope, error) {
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
		// Both link tables feed the same sandbox set; the caller canonicalizes
		// and de-duplicates afterwards, so the two typed reads are equivalent to
		// the released UNION.
		eventLinks, err := client.SchedulerEvent.Query().
			Select(schedulerevent.FieldLinkedSandboxID).
			Where(
				schedulerevent.IDEQ(id.SchedulerID),
				schedulerevent.SchedulerRunIDEQ(id.RunID),
				schedulerevent.LinkedSandboxIDNEQ(""),
			).
			All(ctx)
		if err != nil {
			return scope, err
		}
		for _, event := range eventLinks {
			scope.SandboxIDs = append(scope.SandboxIDs, canonicalSandboxID(event.LinkedSandboxID))
		}
		linkedSandboxes, err := client.EventSandboxLink.Query().
			Select(eventsandboxlink.FieldSandboxID).
			Where(
				eventsandboxlink.SchedulerIDEQ(id.SchedulerID),
				eventsandboxlink.SchedulerRunIDEQ(id.RunID),
				eventsandboxlink.SandboxIDNEQ(""),
			).
			All(ctx)
		if err != nil {
			return scope, err
		}
		for _, link := range linkedSandboxes {
			scope.SandboxIDs = append(scope.SandboxIDs, canonicalSandboxID(link.SandboxID))
		}
	}
	if err := protectRunHistoryReferences(ctx, client, id, &scope); err != nil {
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
