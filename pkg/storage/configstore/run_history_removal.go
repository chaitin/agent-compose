package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"slices"

	"github.com/chaitin/agent-compose/pkg/apierror"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runhistory"
)

func (s *ConfigStore) RemoveRunHistory(ctx context.Context, id runhistory.Identity, prepare func(runhistory.Scope) (runhistory.PreparedArtifacts, error)) error {
	if err := id.Validate(); err != nil {
		return apierror.NotApplied(err)
	}
	if prepare == nil {
		return apierror.NotApplied(domain.ClassifyError(domain.ErrFailedPrecondition, "artifact preparer is required", nil))
	}
	scope, artifacts, err := s.detachRunHistory(ctx, id, prepare)
	if err != nil {
		return err
	}
	defer func() { _ = artifacts.Close() }() // Descriptor close does not change cleanup outcome.
	// Neither a database transaction nor a checked-out connection is held while
	// recursively deleting private material. History remains the retry identity.
	if err := artifacts.Remove(ctx); err != nil {
		return fmt.Errorf("remove isolated run history artifacts (retry with the same identity): %w", err)
	}
	return s.finishRunHistory(ctx, id, scope, prepare)
}

func (s *ConfigStore) runHistorySnapshot(ctx context.Context, id runhistory.Identity) (runhistory.Scope, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return runhistory.Scope{}, err
	}
	defer func() { _ = tx.Rollback() }()
	scope, err := loadRunHistoryScope(ctx, tx, id)
	if err != nil {
		return scope, err
	}
	return scope, tx.Commit()
}

func (s *ConfigStore) detachRunHistory(ctx context.Context, id runhistory.Identity, prepare func(runhistory.Scope) (runhistory.PreparedArtifacts, error)) (scope runhistory.Scope, artifacts runhistory.PreparedArtifacts, err error) {
	scope, err = s.runHistorySnapshot(ctx, id)
	if err != nil {
		return scope, nil, apierror.NotApplied(err)
	}
	// Filesystem validation runs outside any transaction or reserved connection.
	artifacts, err = prepare(scope)
	if err != nil {
		return scope, nil, apierror.NotApplied(err)
	}
	defer func() {
		if err != nil {
			_ = artifacts.Close()
		}
	}()
	started := false
	err = s.withRunHistoryFence(ctx, id, scope, func(*sql.Tx) error {
		// Detach refuses before its atomic rename, and a failed rename leaves
		// the directory in place, so only a successful Detach has an effect.
		err := artifacts.Detach(ctx)
		started = err == nil
		return err
	})
	if err != nil && !started {
		err = apierror.NotApplied(err)
	}
	return scope, artifacts, err
}

// withRunHistoryFence takes the writer lock, re-reads this history's scope in
// the same transaction and runs action only if the scope still equals the one
// the filesystem work validated. Every scope query is indexed and bounded by
// this history, so reading under the lock is cheap, and commits elsewhere in
// the database never invalidate the request.
func (s *ConfigStore) withRunHistoryFence(ctx context.Context, id runhistory.Identity, expected runhistory.Scope, action func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if lock := s.dialect.LockRunHistoryTablesStatement(); lock != "" {
		if _, err := tx.ExecContext(ctx, lock); err != nil {
			return fmt.Errorf("lock run history tables: %w", err)
		}
	}
	// Write first: a deferred SQLite transaction that reads before its first
	// write can fail to upgrade once another writer commits. On PostgreSQL the
	// table lock above supplies the same exclusion; the project row update is
	// still a harmless write fence for the project itself.
	if _, err := tx.ExecContext(ctx, "UPDATE project SET updated_at=updated_at WHERE id=?", id.ProjectID); err != nil {
		return err
	}
	current, err := loadRunHistoryScope(ctx, tx, id)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, expected) {
		return domain.ClassifyError(domain.ErrFailedPrecondition, "run history changed during validation; retry with the same identity", nil)
	}
	if err := action(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ConfigStore) finishRunHistory(ctx context.Context, id runhistory.Identity, previous runhistory.Scope, prepare func(runhistory.Scope) (runhistory.PreparedArtifacts, error)) error {
	scope, err := s.runHistorySnapshot(ctx, id)
	if err != nil {
		return err
	}
	// Concurrent exact removals may already have removed all rows. Newly added
	// children or changed ownership must never be swept into a set-based delete.
	if len(scope.Runs) > 0 && !reflect.DeepEqual(scope.Runs, previous.Runs) {
		return domain.ClassifyError(domain.ErrFailedPrecondition, "run history scope changed during cleanup; retry with the same identity", nil)
	}
	for _, sandboxID := range scope.SandboxIDs {
		if !slices.Contains(previous.SandboxIDs, sandboxID) {
			return domain.ClassifyError(domain.ErrFailedPrecondition, "sandbox links changed during cleanup; retry with the same identity", nil)
		}
	}
	if scope.SchedulerRun != nil && scope.SchedulerRun.Status != "" && !reflect.DeepEqual(scope.SchedulerRun, previous.SchedulerRun) {
		return domain.ClassifyError(domain.ErrFailedPrecondition, "scheduler history changed during cleanup; retry with the same identity", nil)
	}
	artifacts, err := prepare(scope)
	if err != nil {
		return err
	}
	defer func() { _ = artifacts.Close() }()
	if err := artifacts.VerifyRemoved(ctx); err != nil {
		return err
	}
	return s.withRunHistoryFence(ctx, id, scope, func(tx *sql.Tx) error {
		query, args := "DELETE FROM project_run WHERE run_id=? AND project_id=?", []any{id.RunID, id.ProjectID}
		if id.Kind == runhistory.SchedulerRun {
			query, args = "DELETE FROM project_run WHERE scheduler_run_id=? AND scheduler_id=? AND project_id=?", []any{id.RunID, id.SchedulerID, id.ProjectID}
		}
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("delete project run history: %w", err)
		}
		if id.Kind == runhistory.SchedulerRun {
			for _, table := range []string{"event_sandbox_link", "event_delivery", "scheduler_event", "scheduler_run"} {
				key := "scheduler_run_id"
				if table == "scheduler_run" {
					key = "run_id"
				}
				if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE scheduler_id=? AND "+key+"=?", id.SchedulerID, id.RunID); err != nil {
					return fmt.Errorf("delete scheduler run history %s: %w", table, err)
				}
			}
		}
		return nil
	})
}
