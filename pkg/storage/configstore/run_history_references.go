package configstore

import (
	"context"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/eventsandboxlink"
	"github.com/chaitin/agent-compose/ent/dao/projectrun"
	"github.com/chaitin/agent-compose/ent/dao/schedulerevent"
	"github.com/chaitin/agent-compose/ent/dao/schedulersandboxbinding"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runhistory"
)

func protectRunHistoryReferences(ctx context.Context, client *dao.Client, id runhistory.Identity, scope *runhistory.Scope) error {
	sandboxes := make(map[string]bool)
	for _, sandboxID := range scope.SandboxIDs {
		sandboxes[sandboxID] = true
	}
	for sandboxID := range sandboxes {
		// Rows may carry the canonical or the legacy "sha256:" form of the
		// same sandbox; match both. Each probe is an existence check on one
		// table, evaluated through the ent client bound to the history fence
		// transaction.
		legacy := "sha256:" + sandboxID
		referenced, err := runHistorySandboxReferenced(ctx, client, id, sandboxID, legacy)
		if err != nil {
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

// runHistorySandboxReferenced probes every table that can keep a sandbox
// identity alive outside the run history being removed.
func runHistorySandboxReferenced(ctx context.Context, client *dao.Client, id runhistory.Identity, sandboxID, legacy string) (bool, error) {
	referenced, err := client.SchedulerEvent.Query().
		Where(
			schedulerevent.LinkedSandboxIDIn(sandboxID, legacy),
			schedulerevent.LinkedSandboxIDNEQ(""),
			schedulerevent.Not(schedulerevent.And(
				schedulerevent.IDEQ(id.SchedulerID),
				schedulerevent.SchedulerRunIDEQ(id.RunID),
			)),
		).
		Exist(ctx)
	if err != nil {
		return false, err
	}
	if referenced {
		return true, nil
	}
	referenced, err = client.EventSandboxLink.Query().
		Where(
			eventsandboxlink.SandboxIDIn(sandboxID, legacy),
			eventsandboxlink.Not(eventsandboxlink.And(
				eventsandboxlink.SchedulerIDEQ(id.SchedulerID),
				eventsandboxlink.SchedulerRunIDEQ(id.RunID),
			)),
		).
		Exist(ctx)
	if err != nil {
		return false, err
	}
	if referenced {
		return true, nil
	}
	referenced, err = client.SchedulerSandboxBinding.Query().
		Where(schedulersandboxbinding.SandboxIDIn(sandboxID, legacy)).
		Exist(ctx)
	if err != nil {
		return false, err
	}
	if referenced {
		return true, nil
	}
	query := client.ProjectRun.Query().
		Where(
			projectrun.SandboxIDIn(sandboxID, legacy),
			projectrun.IDNEQ(id.RunID),
		)
	if id.Kind == runhistory.SchedulerRun {
		query = query.Where(projectrun.Not(projectrun.And(
			projectrun.SchedulerRunIDEQ(id.RunID),
			projectrun.SchedulerIDEQ(id.SchedulerID),
		)))
	}
	return query.Exist(ctx)
}
