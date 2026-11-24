package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/chaitin/agent-compose/ent/dao/projectrun"
	"github.com/chaitin/agent-compose/internal/projects"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// CreateProjectRunWithEvents creates a run and appends its first events in one
// transaction. The event append keeps its own SQL (sequence allocation and the
// parent-row lock), so the transaction is opened on the shared pool and the ent
// client is bound to it: typed writes and the raw append commit atomically.
func (s *projectStore) CreateProjectRunWithEvents(ctx context.Context, run ProjectRunRecord, events []domain.ProjectRunEventRecord) (ProjectRunRecord, error) {
	run, err := projects.NormalizeRunRecord(run)
	if err != nil {
		return ProjectRunRecord{}, err
	}
	if err := validateHistoryArtifactPaths(nil, run.LogsPath, run.ArtifactsDir); err != nil {
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
	client := newEntTxClient(s.dialect, tx)
	err = client.ProjectRun.Create().
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
		OnConflict(entsql.ConflictColumns(projectrun.FieldID), entsql.DoNothing()).
		Exec(ctx)
	switch {
	case err == nil:
		if err := insertProjectRunLabels(ctx, client, run.RunID, run.Labels); err != nil {
			return ProjectRunRecord{}, err
		}
	case errors.Is(err, sql.ErrNoRows):
		// ON CONFLICT DO NOTHING hit an existing run; keep that row and still
		// append this batch's events exactly as the released path did.
		existing, loadErr := getProjectRunByClient(ctx, client, run.RunID)
		if loadErr != nil {
			return ProjectRunRecord{}, fmt.Errorf("load existing project run %s: %w", run.RunID, loadErr)
		}
		run = existing
	default:
		return ProjectRunRecord{}, fmt.Errorf("insert project run %s: %w", run.RunID, err)
	}
	if _, _, err := appendProjectRunEventsTx(ctx, tx, s.dialect, events); err != nil {
		return ProjectRunRecord{}, fmt.Errorf("append create project run events: %w", err)
	}
	stored, err := getProjectRunByClient(ctx, client, run.RunID)
	if err != nil {
		return ProjectRunRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProjectRunRecord{}, fmt.Errorf("commit create project run transaction: %w", err)
	}
	return stored, nil
}

// UpdateProjectRunWithEvents updates a run and appends events in one
// transaction, using the same ent-over-raw-transaction client as the create
// path.
func (s *projectStore) UpdateProjectRunWithEvents(ctx context.Context, run ProjectRunRecord, events []domain.ProjectRunEventRecord) (ProjectRunRecord, error) {
	run, err := projects.NormalizeRunRecord(run)
	if err != nil {
		return ProjectRunRecord{}, err
	}
	if err := validateHistoryArtifactPaths(nil, run.LogsPath, run.ArtifactsDir); err != nil {
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
	client := newEntTxClient(s.dialect, tx)
	update := client.ProjectRun.Update().
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
		SetUpdatedAt(now.Unix())
	if ptr := schedulerRunIDPtr(run.SchedulerRunID); ptr != nil {
		update.SetSchedulerRunID(*ptr)
	} else {
		update.ClearSchedulerRunID()
	}
	affected, err := update.Save(ctx)
	if err != nil {
		return ProjectRunRecord{}, fmt.Errorf("update project run %s: %w", run.RunID, err)
	}
	if affected == 0 {
		return ProjectRunRecord{}, domain.ResourceError(domain.ErrNotFound, "project run", run.RunID, fmt.Sprintf("project run %s not found", run.RunID), nil)
	}
	if _, _, err := appendProjectRunEventsTx(ctx, tx, s.dialect, events); err != nil {
		return ProjectRunRecord{}, fmt.Errorf("append update project run events: %w", err)
	}
	updated, err := getProjectRunByClient(ctx, client, run.RunID)
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
