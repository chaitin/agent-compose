package configstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/apierror"
	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runhistory"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
)

type runHistoryFixture struct {
	store      *ConfigStore
	standalone runhistory.Identity
	scheduler  runhistory.Identity
	childID    string
}

func newRunHistoryFixture(t *testing.T, store *ConfigStore) runHistoryFixture {
	t.Helper()
	ctx := context.Background()
	if store == nil {
		store = FromDB(newMemoryDB(t))
		if err := store.initSchema(ctx); err != nil {
			t.Fatal(err)
		}
	}
	projectID := identity.NewID(identity.ResourceProject, t.Name())
	schedulerID := identity.NewID(identity.ResourceScheduler, projectID)
	if _, err := upsertNativeTestScheduler(ctx, store, domain.Scheduler{
		Summary: domain.SchedulerSummary{ID: schedulerID, ProjectID: projectID, AgentName: "worker", Runtime: domain.SchedulerRuntimeScheduler},
		Script:  "function main() {}",
	}); err != nil {
		t.Fatal(err)
	}
	agentID := createRunEventTestAgent(t, runEventTestAgentSpec{Ctx: ctx, Store: store, ProjectID: projectID, AgentName: "worker"})
	f := runHistoryFixture{store: store,
		standalone: runhistory.Identity{ProjectID: projectID, Kind: runhistory.Run, RunID: identity.NewID(identity.ResourceRun, "standalone", projectID)},
		scheduler:  runhistory.Identity{ProjectID: projectID, Kind: runhistory.SchedulerRun, SchedulerID: schedulerID, RunID: identity.NewID(identity.ResourceRun, "scheduler", projectID)},
		childID:    identity.NewID(identity.ResourceRun, "child", projectID),
	}
	if err := store.CreateSchedulerRun(ctx, domain.SchedulerRunSummary{ID: f.scheduler.RunID, SchedulerID: schedulerID, Status: domain.SchedulerRunStatusSucceeded, StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, run := range []domain.ProjectRunRecord{
		{RunID: f.standalone.RunID, ProjectID: projectID, AgentName: "worker", AgentID: agentID, Status: domain.ProjectRunStatusSucceeded, Labels: map[string]string{"owner": "standalone"}},
		{RunID: f.childID, ProjectID: projectID, AgentName: "worker", AgentID: agentID, SchedulerID: schedulerID, SchedulerRunID: f.scheduler.RunID, Status: domain.ProjectRunStatusSucceeded, Labels: map[string]string{"owner": "child"}},
	} {
		if _, err := store.CreateProjectRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.AppendProjectRunEvent(ctx, domain.ProjectRunEventRecord{ID: "event-" + run.RunID, RunID: run.RunID, Kind: domain.ProjectRunEventKindAgentMessage, Text: "private output"}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func TestRunHistoryStandaloneExactAndIdempotent(t *testing.T) {
	f := newRunHistoryFixture(t, nil)
	ctx := context.Background()
	for range 2 {
		if err := f.store.RemoveRunHistory(ctx, f.standalone, func(scope runhistory.Scope) error {
			if scope.SchedulerRun != nil || len(scope.Runs) > 1 {
				t.Fatalf("unexpected scope: %+v", scope)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"project_run", "project_run_event", "project_run_label", "project_run_completion"} {
		key := "run_id"
		assertPruneTestRowCount(t, f.store, pruneTestRowCountCheck{Table: table, Where: key + "='" + f.standalone.RunID + "'", Want: 0})
	}
	if _, err := f.store.GetProjectRun(ctx, f.childID); err != nil {
		t.Fatal("unrelated child was changed:", err)
	}
	if _, err := f.store.GetSchedulerRun(ctx, f.scheduler.SchedulerID, f.scheduler.RunID); err != nil {
		t.Fatal(err)
	}
}

func TestRunHistorySchedulerDeletesOnlyExactRelationsAndChildren(t *testing.T) {
	f := newRunHistoryFixture(t, nil)
	ctx := context.Background()
	addPruneTestRelations(t, f.store, pruneTestRelations{SchedulerID: f.scheduler.SchedulerID, RunID: f.scheduler.RunID, TriggerID: "trigger"})
	if _, err := f.store.CreateEvent(ctx, domain.TopicEventRecord{ID: "shared-topic-event", Topic: "test.topic", Source: domain.TopicEventSourceSystem, PayloadJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(ctx, "INSERT INTO event_delivery(event_id,scheduler_id,trigger_id,scheduler_run_id,status,created_at,updated_at) VALUES('shared-topic-event',?,'other','other-run','run_succeeded',1,1)", f.scheduler.SchedulerID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RemoveRunHistory(ctx, f.scheduler, func(scope runhistory.Scope) error {
		if len(scope.Runs) != 1 || scope.Runs[0].RunID != f.childID || scope.SchedulerRun.ID != f.scheduler.RunID {
			t.Fatalf("scope: %+v", scope)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"project_run", "project_run_event", "project_run_label"} {
		assertPruneTestRowCount(t, f.store, pruneTestRowCountCheck{Table: table, Where: "run_id='" + f.childID + "'", Want: 0})
	}
	for _, table := range []string{"scheduler_event", "event_delivery", "event_sandbox_link"} {
		assertPruneTestRowCount(t, f.store, pruneTestRowCountCheck{Table: table, Where: "scheduler_run_id='" + f.scheduler.RunID + "'", Want: 0})
	}
	if _, err := f.store.GetProjectRun(ctx, f.standalone.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.GetScheduler(ctx, f.scheduler.SchedulerID); err != nil {
		t.Fatal(err)
	}
	assertPruneTestRowCount(t, f.store, pruneTestRowCountCheck{Table: "event", Where: "id='shared-topic-event'", Want: 1})
	assertPruneTestRowCount(t, f.store, pruneTestRowCountCheck{Table: "event_delivery", Where: "scheduler_run_id='other-run'", Want: 1})
}

func TestRunHistoryRejectsUnsafeScopeBeforeArtifacts(t *testing.T) {
	for _, name := range []string{"running", "child_running", "completion_pending", "wrong_project", "wrong_scheduler", "wrong_kind", "child_alone", "shared_sandbox", "shared_link", "binding"} {
		t.Run(name, func(t *testing.T) {
			f := newRunHistoryFixture(t, nil)
			id := f.scheduler
			ctx := context.Background()
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := f.store.db.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "running":
				exec("UPDATE scheduler_run SET status='running' WHERE run_id=?", id.RunID)
			case "child_running":
				exec("UPDATE project_run SET status='running' WHERE run_id=?", f.childID)
			case "completion_pending":
				exec("INSERT INTO project_run_completion(run_id,target_status,transition_json,cleanup_action,created_at,updated_at) VALUES(?,'succeeded','{}','none',1,1)", f.childID)
			case "wrong_project":
				id.ProjectID = identity.NewID(identity.ResourceProject, "wrong")
			case "wrong_scheduler":
				id.SchedulerID = identity.NewID(identity.ResourceScheduler, "wrong")
			case "wrong_kind":
				id.Kind = runhistory.Run
				id.SchedulerID = ""
			case "child_alone":
				id = f.standalone
				id.RunID = f.childID
			case "shared_sandbox":
				exec("UPDATE project_run SET sandbox_id='shared'")
			case "shared_link":
				exec("UPDATE project_run SET sandbox_id='shared' WHERE run_id=?", f.childID)
				exec("INSERT INTO event_sandbox_link(event_id,sandbox_id,relation,scheduler_id,scheduler_run_id,created_at) VALUES('shared-event','shared','used',?,'other-run',1)", id.SchedulerID)
			case "binding":
				exec("UPDATE project_run SET sandbox_id='shared' WHERE run_id=?", f.childID)
				exec("INSERT INTO scheduler_sandbox_binding(scheduler_id,trigger_id,sandbox_id,sandbox_config_hash,created_at,updated_at) VALUES(?,'trigger','shared','hash',1,1)", id.SchedulerID)
			}
			called := false
			err := f.store.RemoveRunHistory(ctx, id, func(runhistory.Scope) error { called = true; return nil })
			if err == nil || called || !apierror.IsNotApplied(err) {
				t.Fatalf("err=%v called=%v", err, called)
			}
			if _, err := f.store.GetProjectRun(ctx, f.childID); err != nil {
				t.Fatal("refusal changed records:", err)
			}
		})
	}
}

func TestRunHistoryArtifactAndDatabaseFailuresRemainRetryableAfterRestart(t *testing.T) {
	for _, phase := range []string{"artifacts", "database"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			dbPath := filepath.Join(root, "data.db")
			db, err := storagesqlite.Open(dbPath, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			f := newRunHistoryFixture(t, FromDB(db.DB()))
			id := f.scheduler
			artifactDir := filepath.Join(root, "schedulers", id.SchedulerID, "runs", id.RunID)
			if err := os.MkdirAll(artifactDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(artifactDir, "payload.json"), []byte("private input"), 0o600); err != nil {
				t.Fatal(err)
			}
			remove := runhistory.FSArtifacts{DataRoot: root}
			if phase == "database" {
				if _, err := f.store.db.Exec("CREATE TRIGGER block_history BEFORE DELETE ON scheduler_run BEGIN SELECT RAISE(ABORT,'blocked'); END"); err != nil {
					t.Fatal(err)
				}
			}
			err = f.store.RemoveRunHistory(context.Background(), id, func(scope runhistory.Scope) error {
				if err := remove.Remove(context.Background(), scope); err != nil {
					return err
				}
				if phase == "artifacts" {
					return errors.New("simulated interruption after partial cleanup")
				}
				return nil
			})
			if err == nil || apierror.IsNotApplied(err) {
				t.Fatalf("err=%v", err)
			}
			if _, err := f.store.GetProjectRun(context.Background(), f.childID); err != nil {
				t.Fatal("lost retry identity:", err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = storagesqlite.Open(dbPath, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			}()
			store := FromDB(db.DB())
			if phase == "database" {
				if _, err := store.db.Exec("DROP TRIGGER block_history"); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.RemoveRunHistory(context.Background(), id, func(scope runhistory.Scope) error { return remove.Remove(context.Background(), scope) }); err != nil {
				t.Fatal(err)
			}
			if _, err := store.GetProjectRun(context.Background(), f.childID); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("child remains: %v", err)
			}
			if _, err := os.Stat(artifactDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("artifacts remain: %v", err)
			}
		})
	}
}

func TestRunHistoryConcurrentDeleteAndMissingSchedulerRow(t *testing.T) {
	root := t.TempDir()
	db, err := storagesqlite.Open(filepath.Join(root, "data.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	f := newRunHistoryFixture(t, FromDB(db.DB()))
	id := f.scheduler
	path := filepath.Join(root, "schedulers", id.SchedulerID, "runs", id.RunID)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "result.json"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Covers residues left by older prune implementations that removed the row first.
	if _, err := f.store.db.Exec("DELETE FROM scheduler_run WHERE run_id=?", id.RunID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			artifacts := runhistory.FSArtifacts{DataRoot: root}
			errs <- FromDB(db.DB()).RemoveRunHistory(context.Background(), id, func(scope runhistory.Scope) error { return artifacts.Remove(context.Background(), scope) })
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan directory remains: %v", err)
	}
}
