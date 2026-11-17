package configstore

import (
	"context"
	"database/sql"
	"errors"
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
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return runhistory.Scope{}, err
	}
	defer func() { _ = conn.Close() }()
	scope, _, err := readRunHistorySnapshot(ctx, conn, id)
	return scope, err
}

func (s *ConfigStore) detachRunHistory(ctx context.Context, id runhistory.Identity, prepare func(runhistory.Scope) (runhistory.PreparedArtifacts, error)) (scope runhistory.Scope, artifacts runhistory.PreparedArtifacts, err error) {
	scope, err = s.runHistorySnapshot(ctx, id)
	if err != nil {
		return scope, nil, apierror.NotApplied(err)
	}
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
	err = s.withRunHistoryFence(ctx, id, scope, func(current runhistory.Scope) error {
		p, err := prepare(current)
		if err != nil {
			return err
		}
		return p.Close()
	}, func(*sql.Tx) error {
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

// Re-read the relevant scope after slow filesystem preparation, then fence
// that fresh snapshot. Unrelated commits during preparation need no retry.
func (s *ConfigStore) withRunHistoryFence(ctx context.Context, id runhistory.Identity, expected runhistory.Scope, refresh func(runhistory.Scope) error, action func(*sql.Tx) error) error {
	// Bound retries when unrelated runs keep publishing new artifact paths.
	// A path-set change needs fresh filesystem checks, not immediate refusal.
	for attempt := 0; attempt < 3; attempt++ {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return err
		}
		current, version, err := readRunHistorySnapshot(ctx, conn, id)
		if err != nil {
			_ = conn.Close()
			return err
		}
		if afterRunHistorySnapshot != nil {
			afterRunHistorySnapshot()
		}
		oldOwners, newOwners := expected, current
		oldOwners.ProtectedPaths, newOwners.ProtectedPaths = nil, nil
		if !reflect.DeepEqual(oldOwners, newOwners) {
			_ = conn.Close()
			return domain.ClassifyError(domain.ErrFailedPrecondition, "history ownership changed during validation; retry with the same identity", nil)
		}
		if !slices.Equal(current.ProtectedPaths, expected.ProtectedPaths) {
			_ = conn.Close() // Filesystem validation must not occupy the pool.
			if err := refresh(current); err != nil {
				return err
			}
			expected = current
			continue
		}
		err = commitRunHistoryFence(ctx, conn, id, version, action)
		_ = conn.Close() // Return the reserved connection on every exit.
		if errors.Is(err, errRunHistorySnapshotStale) {
			// data_version moves on any commit to any table. Re-read and
			// compare ownership again instead of refusing the request.
			continue
		}
		return err
	}
	return domain.ClassifyError(domain.ErrFailedPrecondition, "history paths or database kept changing; retry with the same identity", nil)
}

func commitRunHistoryFence(ctx context.Context, conn *sql.Conn, id runhistory.Identity, version int64, action func(*sql.Tx) error) error {
	tx, err := fenceRunHistorySnapshot(ctx, conn, id, version)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := action(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func readRunHistorySnapshot(ctx context.Context, conn *sql.Conn, id runhistory.Identity) (runhistory.Scope, int64, error) {
	var version int64
	// data_version is connection-local and changes on commits by other
	// connections. Reserving this connection makes the later comparison valid.
	if err := conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&version); err != nil {
		return runhistory.Scope{}, 0, err
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return runhistory.Scope{}, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	scope, err := loadRunHistoryScope(ctx, tx, id)
	if err != nil {
		return scope, 0, err
	}
	return scope, version, tx.Commit()
}

// afterRunHistorySnapshot lets tests commit inside the snapshot/fence window.
var afterRunHistorySnapshot func()

// errRunHistorySnapshotStale reports that some commit landed between the
// snapshot read and the fence; withRunHistoryFence re-reads and retries.
var errRunHistorySnapshotStale = domain.ClassifyError(domain.ErrFailedPrecondition, "database changed during history validation; retry with the same identity", nil)

func fenceRunHistorySnapshot(ctx context.Context, conn *sql.Conn, id runhistory.Identity, version int64) (*sql.Tx, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	// Take the writer lock first, then verify no commit invalidated the snapshot.
	// Only an atomic rename or indexed deletes follow; never scans or RemoveAll.
	_, err = tx.ExecContext(ctx, "UPDATE project SET updated_at=updated_at WHERE id=?", id.ProjectID)
	var current int64
	if err == nil {
		err = tx.QueryRowContext(ctx, "PRAGMA data_version").Scan(&current)
	}
	if err == nil && current != version {
		err = errRunHistorySnapshotStale
	}
	if err != nil {
		_ = tx.Rollback() // Preserve the validation error.
		return nil, err
	}
	return tx, nil
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
	return s.withRunHistoryFence(ctx, id, scope, func(current runhistory.Scope) error {
		p, err := prepare(current)
		if err != nil {
			return err
		}
		defer func() { _ = p.Close() }()
		return p.VerifyRemoved(ctx)
	}, func(tx *sql.Tx) error {
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
