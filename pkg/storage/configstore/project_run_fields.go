package configstore

import (
	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/projectrun"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// applyProjectRunCreateFields sets the columns shared by every run creation
// path. includeLifecycle is false for the legacy create-with-events path, which
// intentionally leaves the detach/execution columns to their database defaults;
// the ordinary create path writes them explicitly.
func applyProjectRunCreateFields(create *dao.ProjectRunCreate, run ProjectRunRecord, includeLifecycle bool) *dao.ProjectRunCreate {
	create = create.
		SetID(run.RunID).
		SetProjectID(run.ProjectID).
		SetProjectName(run.ProjectName).
		SetProjectRevision(run.ProjectRevision).
		SetAgentName(run.AgentName).
		SetAgentID(run.AgentID).
		SetSource(run.Source).
		SetSchedulerID(run.SchedulerID).
		SetNillableSchedulerRunID(schedulerRunIDPtr(run.SchedulerRunID)).
		SetTriggerID(run.TriggerID).
		SetStatus(run.Status).
		SetSandboxID(run.SandboxID).
		SetExitCode(run.ExitCode).
		SetError(run.Error).
		SetErrorStack(run.ErrorStack).
		SetPrompt(run.Prompt).
		SetOutput(run.Output).
		SetResultJSON(run.ResultJSON).
		SetLogsPath(run.LogsPath).
		SetArtifactsDir(run.ArtifactsDir).
		SetCleanupError(run.CleanupError).
		SetCleanupPolicy(run.CleanupPolicy).
		SetSandboxCreated(BoolToInt(run.SandboxCreated)).
		SetDriver(run.Driver).
		SetImageRef(run.ImageRef).
		SetStartedAt(domain.NonZeroTimeUnixMilli(run.StartedAt)).
		SetCompletedAt(domain.NonZeroTimeUnixMilli(run.CompletedAt)).
		SetDurationMs(run.DurationMs).
		SetCreatedAt(run.CreatedAt.Unix()).
		SetUpdatedAt(run.UpdatedAt.Unix()).
		SetLogFormat(run.LogFormat).
		SetSummaryJSON(run.SummaryJSON).
		SetAgentCellID(run.AgentCellID).
		SetLogIncomplete(BoolToInt(run.LogIncomplete)).
		SetAgentProvider(run.AgentProvider)
	if includeLifecycle {
		create = create.
			SetExecutionID(run.ExecutionID).
			SetDetach(BoolToInt(run.Detach)).
			SetDeadline(domain.NonZeroTimeUnixMilli(run.Deadline)).
			SetDetachKind(run.DetachKind)
	}
	return create
}

// applyProjectRunUpdateFields sets the columns shared by every run update path.
// The caller supplies the update timestamp because the completion finalizer and
// the ordinary update observe time at different points, and includeLifecycle
// mirrors the create helper for the paths that must not touch detach fields.
func applyProjectRunUpdateFields(update *dao.ProjectRunUpdate, run ProjectRunRecord, updatedAt int64, includeLifecycle bool) *dao.ProjectRunUpdate {
	update = update.
		Where(projectrun.IDEQ(run.RunID)).
		SetProjectID(run.ProjectID).
		SetProjectName(run.ProjectName).
		SetProjectRevision(run.ProjectRevision).
		SetAgentName(run.AgentName).
		SetAgentID(run.AgentID).
		SetSource(run.Source).
		SetSchedulerID(run.SchedulerID).
		SetTriggerID(run.TriggerID).
		SetStatus(run.Status).
		SetSandboxID(run.SandboxID).
		SetExitCode(run.ExitCode).
		SetError(run.Error).
		SetErrorStack(run.ErrorStack).
		SetPrompt(run.Prompt).
		SetOutput(run.Output).
		SetResultJSON(run.ResultJSON).
		SetLogsPath(run.LogsPath).
		SetArtifactsDir(run.ArtifactsDir).
		SetCleanupError(run.CleanupError).
		SetCleanupPolicy(run.CleanupPolicy).
		SetSandboxCreated(BoolToInt(run.SandboxCreated)).
		SetDriver(run.Driver).
		SetImageRef(run.ImageRef).
		SetStartedAt(domain.NonZeroTimeUnixMilli(run.StartedAt)).
		SetCompletedAt(domain.NonZeroTimeUnixMilli(run.CompletedAt)).
		SetDurationMs(run.DurationMs).
		SetUpdatedAt(updatedAt).
		SetLogFormat(run.LogFormat).
		SetSummaryJSON(run.SummaryJSON).
		SetAgentCellID(run.AgentCellID)
	// log_incomplete is sticky: a log fault is only ever raised, so an update
	// carrying a stale false must not clear it.
	if run.LogIncomplete {
		update = update.SetLogIncomplete(1)
	}
	if ptr := schedulerRunIDPtr(run.SchedulerRunID); ptr != nil {
		update = update.SetSchedulerRunID(*ptr)
	} else {
		update = update.ClearSchedulerRunID()
	}
	if includeLifecycle {
		update = update.
			SetExecutionID(run.ExecutionID).
			SetDetach(BoolToInt(run.Detach)).
			SetDeadline(domain.NonZeroTimeUnixMilli(run.Deadline)).
			SetDetachKind(run.DetachKind)
	}
	return update
}
