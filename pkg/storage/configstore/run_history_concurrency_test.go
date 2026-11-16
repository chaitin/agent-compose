package configstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/apierror"
	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runhistory"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
)

type observedHistoryArtifacts struct {
	runhistory.PreparedArtifacts
	beforeRemove func(context.Context) error
}

func (p observedHistoryArtifacts) Remove(ctx context.Context) error {
	if err := p.beforeRemove(ctx); err != nil {
		return err
	}
	return p.PreparedArtifacts.Remove(ctx)
}

func newDiskHistoryFixture(t *testing.T, connections int) (runHistoryFixture, string) {
	t.Helper()
	root := t.TempDir()
	db, err := storagesqlite.OpenWithMaxOpenConns(filepath.Join(root, "data.db"), 100*time.Millisecond, connections)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	f := newRunHistoryFixture(t, FromDB(db.DB()))
	path := filepath.Join(root, "schedulers", f.scheduler.SchedulerID, "runs", f.scheduler.RunID)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "private.json"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f, root
}

func TestRunHistoryCleanupAllowsUnrelatedWrites(t *testing.T) {
	for _, connections := range []int{1, 4} {
		t.Run(fmt.Sprint(connections), func(t *testing.T) {
			f, root := newDiskHistoryFixture(t, connections)
			a := runhistory.FSArtifacts{DataRoot: root}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			preparations := 0
			err := f.store.RemoveRunHistory(ctx, f.scheduler, func(scope runhistory.Scope) (runhistory.PreparedArtifacts, error) {
				if n := f.store.db.Stats().InUse; n != 0 {
					return nil, fmt.Errorf("preparation retained %d database connections", n)
				}
				preparations++
				if preparations == 1 {
					if _, err := f.store.db.ExecContext(ctx, "UPDATE project_run SET output='preparation write' WHERE run_id=?", f.standalone.RunID); err != nil {
						return nil, err
					}
				}
				p, err := a.Prepare(ctx, scope)
				return observedHistoryArtifacts{PreparedArtifacts: p, beforeRemove: func(ctx context.Context) error {
					if n := f.store.db.Stats().InUse; n != 0 {
						return fmt.Errorf("cleanup retained %d database connections", n)
					}
					if _, err := f.store.db.ExecContext(ctx, "UPDATE project_run SET output='unrelated write' WHERE run_id=?", f.standalone.RunID); err != nil {
						return err
					}
					_, _, err := f.store.AppendProjectRunEvent(ctx, domain.ProjectRunEventRecord{ID: "unrelated-event", RunID: f.standalone.RunID, Kind: domain.ProjectRunEventKindAgentMessage, Text: "still writable"})
					return err
				}}, err
			})
			if err != nil {
				t.Fatal(err)
			}
			run, err := f.store.GetProjectRun(ctx, f.standalone.RunID)
			if err != nil || run.Output != "unrelated write" {
				t.Fatalf("unrelated write was lost: %+v, %v", run, err)
			}
			assertPruneTestRowCount(t, f.store, pruneTestRowCountCheck{Table: "project_run_event", Where: "id='unrelated-event'", Want: 1})
		})
	}
}

func TestRunHistorySnapshotRejectsConcurrentReferenceBeforeDetach(t *testing.T) {
	f, root := newDiskHistoryFixture(t, 4)
	ctx := context.Background()
	sandboxID := identity.NewID(identity.ResourceSandbox, t.Name())
	if _, err := f.store.db.Exec("UPDATE project_run SET sandbox_id=? WHERE run_id=?", sandboxID, f.childID); err != nil {
		t.Fatal(err)
	}
	a := runhistory.FSArtifacts{DataRoot: root, SandboxRoot: filepath.Join(root, "sandboxes"), SandboxDirs: historySandboxDirs(filepath.Join(root, "sandboxes"))}
	err := f.store.RemoveRunHistory(ctx, f.scheduler, func(scope runhistory.Scope) (runhistory.PreparedArtifacts, error) {
		// Another scheduler commits a reference after the snapshot was read.
		if err := f.store.AddEventSandboxLink(ctx, domain.EventSandboxLink{EventID: "other-event", SandboxID: sandboxID, SchedulerID: "other-scheduler", RunID: "other-run", Relation: "used"}); err != nil {
			return nil, err
		}
		return a.Prepare(ctx, scope)
	})
	if !errors.Is(err, domain.ErrReferenced) || !apierror.IsNotApplied(err) {
		t.Fatalf("stale snapshot was accepted: %v", err)
	}
	path := filepath.Join(root, "schedulers", f.scheduler.SchedulerID, "runs", f.scheduler.RunID, "private.json")
	if data, err := os.ReadFile(path); err != nil || string(data) != "private" {
		t.Fatalf("stale snapshot changed private material: %q, %v", data, err)
	}
	assertPruneTestRowCount(t, f.store, pruneTestRowCountCheck{Table: "project_run", Where: "run_id='" + f.childID + "'", Want: 1})
}

type historySandboxDirs string

func (r historySandboxDirs) SandboxDir(id string) string { return filepath.Join(string(r), id) }

func TestRunHistoryRechecksScopeAfterCleanup(t *testing.T) {
	for _, change := range []string{"new_child", "shared_path", "recreated_directory"} {
		t.Run(change, func(t *testing.T) {
			f, root := newDiskHistoryFixture(t, 4)
			ctx := context.Background()
			a := runhistory.FSArtifacts{DataRoot: root}
			path := filepath.Join(root, "schedulers", f.scheduler.SchedulerID, "runs", f.scheduler.RunID)
			err := f.store.RemoveRunHistory(ctx, f.scheduler, func(scope runhistory.Scope) (runhistory.PreparedArtifacts, error) {
				p, err := a.Prepare(ctx, scope)
				return observedHistoryArtifacts{PreparedArtifacts: p, beforeRemove: func(ctx context.Context) error {
					switch change {
					case "new_child":
						run, err := f.store.GetProjectRun(ctx, f.childID)
						if err != nil {
							return err
						}
						run.RunID = identity.NewID(identity.ResourceRun, "late-child")
						_, err = f.store.CreateProjectRun(ctx, run)
						return err
					case "shared_path":
						_, err := f.store.db.ExecContext(ctx, "UPDATE project_run SET logs_path=? WHERE run_id=?", filepath.Join(path, "private.json"), f.standalone.RunID)
						return err
					default:
						return os.MkdirAll(path, 0o755)
					}
				}}, err
			})
			if err == nil || apierror.IsNotApplied(err) {
				t.Fatalf("changed cleanup scope was accepted or claimed NOT_APPLIED: %v", err)
			}
			assertPruneTestRowCount(t, f.store, pruneTestRowCountCheck{Table: "project_run", Where: "run_id='" + f.childID + "'", Want: 1})
		})
	}
}

func TestRunHistoryFinalSnapshotRejectsConcurrentReference(t *testing.T) {
	f, root := newDiskHistoryFixture(t, 4)
	ctx := context.Background()
	a := runhistory.FSArtifacts{DataRoot: root}
	preparations := 0
	err := f.store.RemoveRunHistory(ctx, f.scheduler, func(scope runhistory.Scope) (runhistory.PreparedArtifacts, error) {
		preparations++
		if preparations == 2 {
			path := filepath.Join(root, "schedulers", f.scheduler.SchedulerID, "runs", f.scheduler.RunID, "private.json")
			if _, err := f.store.db.ExecContext(ctx, "UPDATE project_run SET logs_path=? WHERE run_id=?", path, f.standalone.RunID); err != nil {
				return nil, err
			}
		}
		return a.Prepare(ctx, scope)
	})
	if !errors.Is(err, domain.ErrReferenced) || apierror.IsNotApplied(err) {
		t.Fatalf("stale final validation was accepted or claimed NOT_APPLIED: %v", err)
	}
	assertPruneTestRowCount(t, f.store, pruneTestRowCountCheck{Table: "project_run", Where: "run_id='" + f.childID + "'", Want: 1})
	if _, err := f.store.db.ExecContext(ctx, "UPDATE project_run SET logs_path='' WHERE run_id=?", f.standalone.RunID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RemoveRunHistory(ctx, f.scheduler, func(scope runhistory.Scope) (runhistory.PreparedArtifacts, error) {
		return a.Prepare(ctx, scope)
	}); err != nil {
		t.Fatalf("retry after concurrent commit: %v", err)
	}
}

func TestRunHistoryVersionFenceRejectsConcurrentCommit(t *testing.T) {
	f, _ := newDiskHistoryFixture(t, 4)
	ctx := context.Background()
	conn, err := f.store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, version, err := readRunHistorySnapshot(ctx, conn, f.scheduler)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(ctx, "UPDATE project_run SET output='concurrent' WHERE run_id=?", f.standalone.RunID); err != nil {
		t.Fatal(err)
	}
	tx, err := fenceRunHistorySnapshot(ctx, conn, f.scheduler, version)
	if tx != nil {
		_ = tx.Rollback()
	}
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("concurrent commit was not fenced: %v", err)
	}
}
