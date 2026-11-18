package configstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/chaitin/agent-compose/pkg/apierror"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runhistory"
)

func TestRunHistoryAcceptsUnrelatedPathChanges(t *testing.T) {
	for _, phase := range []string{"preparation", "final_validation"} {
		t.Run(phase, func(t *testing.T) {
			f, root := newDiskHistoryFixture(t, 1)
			ctx := context.Background()
			a := runhistory.FSArtifacts{DataRoot: root}
			calls := 0
			err := f.store.RemoveRunHistory(ctx, f.scheduler, func(scope runhistory.Scope) (runhistory.PreparedArtifacts, error) {
				calls++
				if phase == "preparation" && calls == 1 || phase == "final_validation" && calls == 2 {
					_, err := f.store.db.ExecContext(ctx, "UPDATE project_run SET logs_path=? WHERE run_id=?", filepath.Join(root, "unrelated-sandbox", "output.txt"), f.standalone.RunID)
					if err != nil {
						return nil, err
					}
				}
				return a.Prepare(ctx, scope)
			})
			if err != nil {
				t.Fatalf("unrelated path blocked removal: %v", err)
			}
			assertPruneTestRowCount(t, f.store, pruneTestRowCountCheck{Table: "project_run", Where: "run_id='" + f.childID + "'", Want: 0})
			run, err := f.store.GetProjectRun(ctx, f.standalone.RunID)
			if err != nil || run.LogsPath != filepath.Join(root, "unrelated-sandbox", "output.txt") {
				t.Fatalf("unrelated path update lost: %+v, %v", run, err)
			}
		})
	}
}

func TestRunHistoryRetryProgressesWithBothDirectories(t *testing.T) {
	f, root := newDiskHistoryFixture(t, 4)
	ctx := context.Background()
	a := runhistory.FSArtifacts{DataRoot: root}
	canonical := filepath.Join(root, "schedulers", f.scheduler.SchedulerID, "runs", f.scheduler.RunID)
	pending := filepath.Join(filepath.Dir(canonical), ".history-removing-"+f.scheduler.RunID)
	interrupted := errors.New("interrupted after canonical directory recreation")
	err := f.store.RemoveRunHistory(ctx, f.scheduler, func(scope runhistory.Scope) (runhistory.PreparedArtifacts, error) {
		p, err := a.Prepare(ctx, scope)
		return observedHistoryArtifacts{PreparedArtifacts: p, beforeRemove: func(context.Context) error {
			if err := os.MkdirAll(canonical, 0o755); err != nil {
				return err
			}
			return interrupted
		}}, err
	})
	if !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	prepare := func(scope runhistory.Scope) (runhistory.PreparedArtifacts, error) { return a.Prepare(ctx, scope) }
	// The first retry cleans the existing pending tree, preserving the canonical
	// directory and history. The next retry isolates and finishes that remainder.
	if err := f.store.RemoveRunHistory(ctx, f.scheduler, prepare); !errors.Is(err, domain.ErrFailedPrecondition) || apierror.IsNotApplied(err) {
		t.Fatalf("unexpected first retry: %v", err)
	}
	if _, err := os.Stat(pending); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry did not clean pending material: %v", err)
	}
	if _, err := os.Stat(canonical); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RemoveRunHistory(ctx, f.scheduler, prepare); err != nil {
		t.Fatalf("retry did not finish: %v", err)
	}
	assertPruneTestRowCount(t, f.store, pruneTestRowCountCheck{Table: "project_run", Where: "run_id='" + f.childID + "'", Want: 0})
}

func TestRunHistoryRejectsLateReservedReference(t *testing.T) {
	f, root := newDiskHistoryFixture(t, 4)
	ctx := context.Background()
	a := runhistory.FSArtifacts{DataRoot: root}
	err := f.store.RemoveRunHistory(ctx, f.scheduler, func(scope runhistory.Scope) (runhistory.PreparedArtifacts, error) {
		p, err := a.Prepare(ctx, scope)
		return observedHistoryArtifacts{PreparedArtifacts: p, beforeRemove: func(ctx context.Context) error {
			run, err := f.store.GetProjectRun(ctx, f.standalone.RunID)
			if err != nil {
				return err
			}
			run.LogsPath = filepath.Join(root, "schedulers", f.scheduler.SchedulerID, "runs", ".history-removing-"+f.scheduler.RunID, "private.json")
			if _, err := f.store.UpdateProjectRun(ctx, run); !errors.Is(err, domain.ErrInvalidArgument) {
				return errors.New("reserved pending reference was not rejected")
			}
			return nil
		}}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.store.GetProjectRun(ctx, f.standalone.RunID)
	if err != nil || run.LogsPath != "" {
		t.Fatalf("rejected reference was persisted: %+v, %v", run, err)
	}
}
