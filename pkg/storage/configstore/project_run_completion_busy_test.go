package configstore

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
)

// TestStageProjectRunCompletionWaitsForConcurrentWriter reproduces the SQLite
// contention behind #880: a scheduler write holds the write lock while a run
// finishes, and the completion transaction reads the run before inserting the
// completion row. The transaction must wait for the busy timeout instead of
// failing immediately with SQLITE_BUSY.
func TestStageProjectRunCompletionWaitsForConcurrentWriter(t *testing.T) {
	root := t.TempDir()
	db, err := storagesqlite.OpenWithMaxOpenConns(filepath.Join(root, "data.db"), 2*time.Second, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	store := FromDB(db.DB())
	fixture := newRunHistoryFixture(t, store)
	ctx := context.Background()

	run, err := store.GetProjectRun(ctx, fixture.standalone.RunID)
	if err != nil {
		t.Fatal(err)
	}
	run.RunID = identity.NewID(identity.ResourceRun, "completion-busy", t.Name())
	run.Status = domain.ProjectRunStatusRunning
	run.SandboxID = ""
	run.Labels = map[string]string{"case": "completion-busy"}
	if _, err := store.CreateProjectRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	holder, err := db.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	// An unrelated write holds the lock the way a scheduler run write does.
	if _, err := holder.ExecContext(ctx, `UPDATE project_run SET output=output WHERE run_id=?`, fixture.standalone.RunID); err != nil {
		t.Fatal(err)
	}

	type staged struct {
		created bool
		err     error
	}
	done := make(chan staged, 1)
	go func() {
		_, created, err := store.StageProjectRunCompletion(ctx, domain.ProjectRunCompletionRecord{
			RunID:          run.RunID,
			TargetStatus:   domain.ProjectRunStatusSucceeded,
			TransitionJSON: fmt.Sprintf(`{"run_id":%q,"status":"succeeded"}`, run.RunID),
		}, nil)
		done <- staged{created: created, err: err}
	}()

	select {
	case result := <-done:
		_ = holder.Rollback()
		t.Fatalf("completion staging did not wait for the concurrent writer: %v", result.err)
	case <-time.After(250 * time.Millisecond):
	}
	if err := holder.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("completion staging failed after the writer released the lock: %v", result.err)
		}
		if !result.created {
			t.Fatal("completion staging did not create the completion row")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completion staging did not finish after the writer released the lock")
	}
	if _, err := store.GetProjectRunCompletion(ctx, run.RunID); err != nil {
		t.Fatalf("completion row is missing: %v", err)
	}
}
