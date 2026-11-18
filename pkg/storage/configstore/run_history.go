package configstore

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runhistory"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

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
	rows, err := tx.QueryContext(ctx, query+" ORDER BY run_id", args...)
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
			scope.SandboxIDs = append(scope.SandboxIDs, canonicalSandboxID(run.SandboxID))
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
