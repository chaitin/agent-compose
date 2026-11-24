package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/projectrun"
	"github.com/chaitin/agent-compose/ent/dao/projectruncompletion"
	"github.com/chaitin/agent-compose/internal/projects"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func (s *projectStore) StageProjectRunCompletion(ctx context.Context, completion domain.ProjectRunCompletionRecord, events []domain.ProjectRunEventRecord) (domain.ProjectRunCompletionRecord, bool, error) {
	completion.RunID = strings.TrimSpace(completion.RunID)
	if completion.RunID == "" || strings.TrimSpace(completion.TargetStatus) == "" || strings.TrimSpace(completion.TransitionJSON) == "" {
		return domain.ProjectRunCompletionRecord{}, false, fmt.Errorf("run completion id, target status, and transition are required")
	}
	if err := validateRunEventBatch(completion.RunID, events); err != nil {
		return domain.ProjectRunCompletionRecord{}, false, err
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ProjectRunCompletionRecord{}, false, fmt.Errorf("begin stage project run completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	client := newEntTxClient(s.dialect, tx)
	// A run that already reached a terminal state is not staged again: a
	// second writer that read the run while it was still active (e.g. a
	// re-attach racing the completion of the first writer's row) would
	// otherwise finalize over the first writer's result.
	var currentStatus string
	runEntity, err := client.ProjectRun.Query().
		Select(projectrun.FieldStatus).
		Where(projectrun.IDEQ(completion.RunID)).
		Only(ctx)
	if err != nil && !dao.IsNotFound(err) {
		return domain.ProjectRunCompletionRecord{}, false, fmt.Errorf("load project run %s for completion: %w", completion.RunID, err)
	}
	if err == nil {
		currentStatus = runEntity.Status
	}
	if projectRunStatusTerminal(currentStatus) {
		return domain.ProjectRunCompletionRecord{RunID: completion.RunID}, false, nil
	}
	// The INSERT reports whether this writer created the row through ent's
	// DO NOTHING upsert: a conflict yields sql.ErrNoRows because no row is
	// returned, exactly like RowsAffected() == 0 in the released statement.
	createErr := client.ProjectRunCompletion.Create().
		SetID(completion.RunID).
		SetTargetStatus(completion.TargetStatus).
		SetTransitionJSON(completion.TransitionJSON).
		SetCleanupAction(completion.CleanupAction).
		SetAttempt(0).
		SetLastError("").
		SetNextAttemptAt(0).
		SetCreatedAt(now.UnixMilli()).
		SetUpdatedAt(now.UnixMilli()).
		OnConflict(entsql.ConflictColumns(projectruncompletion.FieldID), entsql.DoNothing()).
		Exec(ctx)
	created := createErr == nil
	if createErr != nil && !errors.Is(createErr, sql.ErrNoRows) {
		return domain.ProjectRunCompletionRecord{}, false, fmt.Errorf("insert project run completion %s: %w", completion.RunID, createErr)
	}
	if created {
		if _, _, err := appendProjectRunEventsTx(ctx, tx, s.dialect, events); err != nil {
			return domain.ProjectRunCompletionRecord{}, false, fmt.Errorf("append staged project run completion events: %w", err)
		}
		// Kept as SQL: the update guards started_at with CASE over the existing
		// row, which ent's update setters cannot express.
		if _, err := tx.ExecContext(ctx, `UPDATE project_run SET status = 'running', cleanup_error = '',
			started_at = CASE WHEN started_at = 0 THEN ? ELSE started_at END, updated_at = ?
			WHERE run_id = ? AND status IN ('pending', 'running')`, now.UnixMilli(), now.Unix(), completion.RunID); err != nil {
			return domain.ProjectRunCompletionRecord{}, false, fmt.Errorf("keep project run %s active during completion: %w", completion.RunID, err)
		}
	}
	stored, err := getProjectRunCompletionByClient(ctx, client, completion.RunID)
	if err != nil {
		return domain.ProjectRunCompletionRecord{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ProjectRunCompletionRecord{}, false, fmt.Errorf("commit staged project run completion: %w", err)
	}
	return stored, created, nil
}

func projectRunStatusTerminal(status string) bool {
	switch strings.TrimSpace(status) {
	case domain.ProjectRunStatusSucceeded, domain.ProjectRunStatusFailed, domain.ProjectRunStatusCanceled:
		return true
	default:
		return false
	}
}

func (s *projectStore) GetProjectRunCompletion(ctx context.Context, runID string) (domain.ProjectRunCompletionRecord, error) {
	entity, err := s.client.ProjectRunCompletion.Query().
		Where(projectruncompletion.IDEQ(strings.TrimSpace(runID))).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return domain.ProjectRunCompletionRecord{}, sql.ErrNoRows
		}
		return domain.ProjectRunCompletionRecord{}, err
	}
	return projectRunCompletionFromEntity(entity), nil
}

func (s *projectStore) ListDueProjectRunCompletions(ctx context.Context, now time.Time, limit int) ([]domain.ProjectRunCompletionRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	entities, err := s.client.ProjectRunCompletion.Query().
		Where(projectruncompletion.NextAttemptAtLTE(now.UTC().UnixMilli())).
		Order(projectruncompletion.ByNextAttemptAt(), projectruncompletion.ByCreatedAt()).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query due project run completions: %w", err)
	}
	var items []domain.ProjectRunCompletionRecord
	for _, entity := range entities {
		items = append(items, projectRunCompletionFromEntity(entity))
	}
	return items, nil
}

func (s *projectStore) RecordProjectRunCompletionFailure(ctx context.Context, failure domain.ProjectRunCompletionFailure) error {
	runID, message, attempt, next := failure.RunID, failure.Message, failure.Attempt, failure.NextAttemptAt
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin record project run completion failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	if _, err := client.ProjectRunCompletion.Update().
		Where(projectruncompletion.IDEQ(runID)).
		SetAttempt(attempt).
		SetLastError(message).
		SetNextAttemptAt(next.UTC().UnixMilli()).
		SetUpdatedAt(time.Now().UTC().UnixMilli()).
		Save(ctx); err != nil {
		return err
	}
	if _, err := client.ProjectRun.Update().
		Where(projectrun.IDEQ(runID)).
		SetCleanupError(message).
		SetUpdatedAt(time.Now().UTC().Unix()).
		Save(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *projectStore) FinalizeProjectRunCompletion(ctx context.Context, run domain.ProjectRunRecord, statusEvent domain.ProjectRunEventRecord) (domain.ProjectRunRecord, error) {
	return s.finalizeProjectRunCompletion(ctx, run, statusEvent, "")
}

// AbandonProjectRunCompletion finalizes a run whose sandbox cleanup never
// succeeded. Unlike FinalizeProjectRunCompletion it keeps cleanupError on the
// run, so a permanent cleanup failure stays visible after the run converges
// instead of being silently cleared.
func (s *projectStore) AbandonProjectRunCompletion(ctx context.Context, run domain.ProjectRunRecord, statusEvent domain.ProjectRunEventRecord, cleanupError string) (domain.ProjectRunRecord, error) {
	return s.finalizeProjectRunCompletion(ctx, run, statusEvent, cleanupError)
}

func (s *projectStore) finalizeProjectRunCompletion(ctx context.Context, run domain.ProjectRunRecord, statusEvent domain.ProjectRunEventRecord, cleanupError string) (domain.ProjectRunRecord, error) {
	run, err := projects.NormalizeRunRecord(run)
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	if err := validateHistoryArtifactPaths(nil, run.LogsPath, run.ArtifactsDir); err != nil {
		return domain.ProjectRunRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.ProjectRunRecord{}, fmt.Errorf("begin finalize project run completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	client := newEntTxClient(s.dialect, tx)
	if _, err := getProjectRunCompletionByClient(ctx, client, run.RunID); err != nil {
		return domain.ProjectRunRecord{}, err
	}
	run.CleanupError = cleanupError
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
		SetUpdatedAt(time.Now().UTC().Unix())
	if ptr := schedulerRunIDPtr(run.SchedulerRunID); ptr != nil {
		update.SetSchedulerRunID(*ptr)
	} else {
		update.ClearSchedulerRunID()
	}
	affected, err := update.Save(ctx)
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	if affected == 0 {
		return domain.ProjectRunRecord{}, domain.ResourceError(domain.ErrNotFound, "project run", run.RunID, "project run not found", nil)
	}
	if _, _, err := appendProjectRunEventsTx(ctx, tx, s.dialect, []domain.ProjectRunEventRecord{statusEvent}); err != nil {
		return domain.ProjectRunRecord{}, fmt.Errorf("append terminal project run status event: %w", err)
	}
	if _, err := client.ProjectRunCompletion.Delete().
		Where(projectruncompletion.IDEQ(run.RunID)).
		Exec(ctx); err != nil {
		return domain.ProjectRunRecord{}, err
	}
	stored, err := getProjectRunByClient(ctx, client, run.RunID)
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.ProjectRunRecord{}, fmt.Errorf("commit finalized project run completion: %w", err)
	}
	return stored, nil
}

func (s *projectStore) ProjectRunCompletionForSandbox(ctx context.Context, sandboxID string) (string, bool, error) {
	var runID string
	// Kept as SQL: joins project_run_completion with project_run.
	err := s.db.QueryRowContext(ctx, `SELECT c.run_id FROM project_run_completion c JOIN project_run r ON r.run_id=c.run_id WHERE r.sandbox_id=? LIMIT 1`, strings.TrimSpace(sandboxID)).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return runID, err == nil, err
}

// getProjectRunCompletionByClient reads one staged completion through the
// caller's ent client, which may be bound to a raw transaction.
func getProjectRunCompletionByClient(ctx context.Context, client *dao.Client, runID string) (domain.ProjectRunCompletionRecord, error) {
	entity, err := client.ProjectRunCompletion.Query().
		Where(projectruncompletion.IDEQ(runID)).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return domain.ProjectRunCompletionRecord{}, sql.ErrNoRows
		}
		return domain.ProjectRunCompletionRecord{}, err
	}
	return projectRunCompletionFromEntity(entity), nil
}

// projectRunCompletionFromEntity maps a completion row exactly like the
// released row scanner.
func projectRunCompletionFromEntity(entity *dao.ProjectRunCompletion) domain.ProjectRunCompletionRecord {
	item := domain.ProjectRunCompletionRecord{
		RunID:          entity.ID,
		TargetStatus:   entity.TargetStatus,
		TransitionJSON: entity.TransitionJSON,
		CleanupAction:  entity.CleanupAction,
		Attempt:        entity.Attempt,
		LastError:      entity.LastError,
		CreatedAt:      time.UnixMilli(entity.CreatedAt).UTC(),
		UpdatedAt:      time.UnixMilli(entity.UpdatedAt).UTC(),
	}
	if entity.NextAttemptAt > 0 {
		item.NextAttemptAt = time.UnixMilli(entity.NextAttemptAt).UTC()
	}
	return item
}
