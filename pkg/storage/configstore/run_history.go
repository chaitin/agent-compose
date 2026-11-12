package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/chaitin/agent-compose/pkg/apierror"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runhistory"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

func (s *ConfigStore) RemoveRunHistory(ctx context.Context, id runhistory.Identity, removeArtifacts func(runhistory.Scope) error) error {
	if err := id.Validate(); err != nil {
		return apierror.NotApplied(err)
	}
	if removeArtifacts == nil {
		return apierror.NotApplied(domain.ClassifyError(domain.ErrFailedPrecondition, "artifact remover is required", nil))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return apierror.NotApplied(err)
	}
	defer func() { _ = tx.Rollback() }()
	// Acquire SQLite's writer lock before reading any candidate. Concurrent
	// completion, child creation, and history removals cannot change this scope.
	if _, err := tx.ExecContext(ctx, "UPDATE project SET updated_at=updated_at WHERE id=?", id.ProjectID); err != nil {
		return apierror.NotApplied(err)
	}
	scope, err := loadRunHistoryScope(ctx, tx, id)
	if err != nil {
		return apierror.NotApplied(err)
	}
	if err := removeArtifacts(scope); err != nil {
		// Files may already be partially removed. Never claim NOT_APPLIED or
		// delete the identity/path records on this path.
		return err
	}
	for _, run := range scope.Runs {
		if _, err := tx.ExecContext(ctx, "DELETE FROM project_run WHERE run_id=? AND project_id=?", run.RunID, id.ProjectID); err != nil {
			return fmt.Errorf("delete project run history: %w", err)
		}
	}
	if id.Kind == runhistory.SchedulerRun {
		for _, table := range []string{"event_sandbox_link", "event_delivery", "scheduler_event"} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE scheduler_id=? AND scheduler_run_id=?", id.SchedulerID, id.RunID); err != nil {
				return fmt.Errorf("delete scheduler run history %s: %w", table, err)
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM scheduler_run WHERE scheduler_id=? AND run_id=?", id.SchedulerID, id.RunID); err != nil {
			return fmt.Errorf("delete scheduler run history: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit run history removal (retry with the same identity): %w", err)
	}
	return nil
}

func loadRunHistoryScope(ctx context.Context, tx *sql.Tx, id runhistory.Identity) (runhistory.Scope, error) {
	scope := runhistory.Scope{}
	var projectID string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM project WHERE id=?", id.ProjectID).Scan(&projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return scope, domain.ClassifyError(domain.ErrNotFound, "project not found", err)
		}
		return scope, err
	}
	switch id.Kind {
	case runhistory.Run:
		// A caller cannot erase a scheduler's child independently and leave
		// the parent history pointing at missing execution evidence.
		var schedulerRunExists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM scheduler_run WHERE run_id=?)", id.RunID).Scan(&schedulerRunExists); err != nil {
			return scope, err
		}
		if schedulerRunExists {
			return scope, domain.ClassifyError(domain.ErrFailedPrecondition, "identity refers to a scheduler run", nil)
		}
	case runhistory.SchedulerRun:
		if err := tx.QueryRowContext(ctx, "SELECT project_id FROM project_scheduler WHERE id=?", id.SchedulerID).Scan(&projectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return scope, domain.ClassifyError(domain.ErrNotFound, "scheduler not found", err)
			}
			return scope, err
		}
		if projectID != id.ProjectID {
			return scope, domain.ClassifyError(domain.ErrNotFound, "scheduler not found in project", nil)
		}
		run := domain.SchedulerRunSummary{ID: id.RunID, SchedulerID: id.SchedulerID}
		var schedulerID, status, artifactsDir string
		err := tx.QueryRowContext(ctx, "SELECT scheduler_id, status, artifacts_dir FROM scheduler_run WHERE run_id=?", id.RunID).Scan(&schedulerID, &status, &artifactsDir)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return scope, err
		}
		if err == nil {
			if schedulerID != id.SchedulerID {
				return scope, domain.ClassifyError(domain.ErrNotFound, "run not found in scheduler", nil)
			}
			if !schedulers.SchedulerRunStatusIsTerminal(status) {
				return scope, domain.ClassifyError(domain.ErrFailedPrecondition, "scheduler run is not terminal", nil)
			}
			run.Status, run.ArtifactsDir = status, artifactsDir
		}
		// Even after a prior prune lost the row, this exact scheduler identity
		// still lets a retry remove its canonical directory and direct links.
		scope.SchedulerRun = &run
	}
	query := "SELECT run_id, project_id, scheduler_id, COALESCE(scheduler_run_id,''), status, sandbox_id, logs_path, artifacts_dir FROM project_run WHERE run_id=?"
	if id.Kind == runhistory.SchedulerRun {
		query += " OR scheduler_run_id=?"
	}
	args := []any{id.RunID}
	if id.Kind == runhistory.SchedulerRun {
		args = append(args, id.RunID)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return scope, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var run domain.ProjectRunRecord
		if err := rows.Scan(&run.RunID, &run.ProjectID, &run.SchedulerID, &run.SchedulerRunID, &run.Status, &run.SandboxID, &run.LogsPath, &run.ArtifactsDir); err != nil {
			return scope, err
		}
		scope.Runs = append(scope.Runs, run)
		if run.SandboxID != "" {
			scope.SandboxIDs = append(scope.SandboxIDs, run.SandboxID)
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return scope, err
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
		var pending bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM project_run_completion WHERE run_id=?)", run.RunID).Scan(&pending); err != nil {
			return scope, err
		}
		if pending {
			return scope, domain.ClassifyError(domain.ErrFailedPrecondition, "run completion is still pending", nil)
		}
	}
	if id.Kind == runhistory.SchedulerRun {
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
			scope.SandboxIDs = append(scope.SandboxIDs, sandboxID)
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
	return scope, nil
}

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
			OR EXISTS(SELECT 1 FROM scheduler_sandbox_binding WHERE sandbox_id=?)`,
			sandboxID, id.SchedulerID, id.RunID, sandboxID, id.SchedulerID, id.RunID, sandboxID).Scan(&referenced); err != nil {
			return err
		}
		if referenced {
			return domain.ClassifyError(domain.ErrReferenced, "sandbox has references outside this run history", nil)
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT run_id, sandbox_id, logs_path, artifacts_dir FROM project_run")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var runID, sandboxID, logsPath, artifactsDir string
		if err := rows.Scan(&runID, &sandboxID, &logsPath, &artifactsDir); err != nil {
			return err
		}
		if owned[runID] {
			continue
		}
		if sandboxes[sandboxID] {
			return domain.ClassifyError(domain.ErrReferenced, "sandbox is referenced by another run", nil)
		}
		scope.ProtectedPaths = append(scope.ProtectedPaths, logsPath, artifactsDir)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, "SELECT scheduler_id, run_id, artifacts_dir FROM scheduler_run")
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
