package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
)

func TestIntegrationRunSupervisorStopChecksInactiveCompletionOwnership(t *testing.T) {
	ctx := context.Background()
	store, base := newInactiveCompletionRun(t)
	manager := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: store})
	supervisor := &RunSupervisor{store: store, completions: manager}
	if stopped, err := supervisor.StopActiveRun(ctx, base.RunID, "stop orphan"); stopped || !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("StopActiveRun on an orphan = %v, %v; want identifiable ownership error", stopped, err)
	}
	if err := manager.StageInterrupted(ctx, base, "recovered interruption"); err != nil {
		t.Fatal(err)
	}
	if stopped, err := supervisor.StopActiveRun(ctx, base.RunID, "late stop"); stopped || err != nil {
		t.Fatalf("StopActiveRun while completion is staged = %v, %v", stopped, err)
	}
	completed, err := manager.Complete(ctx, runs.TransitionRequest{RunID: base.RunID, Status: domain.ProjectRunStatusCanceled})
	if err != nil || completed.Status != domain.ProjectRunStatusFailed {
		t.Fatalf("original staged interruption was replaced: %#v, %v", completed, err)
	}
	if stopped, err := supervisor.StopActiveRun(ctx, base.RunID, "already terminal"); stopped || err != nil {
		t.Fatalf("StopActiveRun on a terminal run = %v, %v", stopped, err)
	}
}

func TestIntegrationRunCompletionRestartRequiresPersistedResult(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("commit_before_error=%v", committed), func(t *testing.T) {
			ctx := context.Background()
			store, run := newInactiveCompletionRun(t)
			fault := &supervisorCompletionFaultStore{CompletionStore: store, commitBeforeError: committed}
			oldManager := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: fault})
			if _, err := oldManager.Complete(ctx, runs.TransitionRequest{
				RunID: run.RunID, Status: domain.ProjectRunStatusSucceeded, Output: "original successful result",
			}); err == nil {
				t.Fatal("first completion write unexpectedly reported success")
			}
			if err := oldManager.Shutdown(ctx); err == nil {
				t.Fatal("shutdown silently ignored an unconfirmed completion write")
			}
			// Recreate the manager with only the persisted store, as after a
			// restart. The old manager's retained result must not be available.
			newManager := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: store})
			pending, err := newManager.Pending(ctx, run.RunID)
			if err != nil || pending != committed {
				t.Fatalf("restart pending=%v, err=%v; want persisted ownership=%v", pending, err, committed)
			}
			if err := reconcilePersistedProjectRuns(ctx, projectRunReconcileContext{
				ConfigDB: store, Completions: newManager, RootCtx: ctx, StartedAt: run.CreatedAt.Add(time.Second),
			}); err != nil {
				t.Fatal(err)
			}
			// Drive the completion staged by startup recovery. First stored
			// result wins, including when its original commit returned an error.
			completed, err := newManager.Complete(ctx, runs.TransitionRequest{RunID: run.RunID, Status: domain.ProjectRunStatusCanceled})
			if err != nil {
				t.Fatal(err)
			}
			if committed {
				if completed.Status != domain.ProjectRunStatusSucceeded || completed.Output != "original successful result" {
					t.Fatalf("persisted success was not recovered: %#v", completed)
				}
			} else if completed.Status != domain.ProjectRunStatusFailed || completed.Error != staleProjectRunError || completed.Output != "" {
				t.Fatalf("restart invented an unpersisted result instead of marking interruption: %#v", completed)
			}
		})
	}
}

func newInactiveCompletionRun(t *testing.T) (*configstore.ConfigStore, domain.ProjectRunRecord) {
	t.Helper()
	ctx := context.Background()
	store := newRunSupervisorTestConfigStore(t)
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-1", Name: "project", SourcePath: "/project", SourceJSON: `{}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{ID: "agent-1", ProjectID: "project-1", AgentName: "worker"}); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID: "inactive-run", ProjectID: "project-1", AgentID: "agent-1", AgentName: "worker", Status: domain.ProjectRunStatusRunning,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, run
}

func TestIntegrationRunSupervisorCompletionSurvivesExecutorExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := newRunSupervisorTestConfigStore(t)
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-1", Name: "project", SourcePath: "/project", SourceJSON: `{}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{ID: "agent-1", ProjectID: "project-1", AgentName: "worker"}); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID: "completion-run", ProjectID: "project-1", AgentID: "agent-1", AgentName: "worker", Status: domain.ProjectRunStatusPending,
	})
	if err != nil {
		t.Fatal(err)
	}
	faultStore := &supervisorCompletionFaultStore{CompletionStore: store, finalized: make(chan domain.ProjectRunRecord, 1)}
	manager := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: faultStore})
	controller := &supervisorCompletionController{run: run, store: store, manager: manager}
	supervisor := &RunSupervisor{root: ctx, controller: controller, completions: manager, store: store, active: make(map[string]*activeRun)}
	if _, err := supervisor.StartRun(ctx, runs.RunAgentRequest{}); err != nil {
		t.Fatal(err)
	}
	// Shutdown waits for the execution goroutine to exit and unregister. The
	// completion worker is started afterward so this boundary is deterministic.
	if err := supervisor.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetProjectRunCompletion(ctx, run.RunID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("first failed write unexpectedly persisted completion: %v", err)
	}
	current, err := store.GetProjectRun(ctx, run.RunID)
	if err != nil || current.Status != domain.ProjectRunStatusRunning {
		t.Fatalf("run after execution exit = %#v, %v; want running before recovery", current, err)
	}
	if pending, err := manager.Pending(ctx, run.RunID); err != nil || !pending {
		t.Fatalf("executor exited without a completion owner: pending=%v err=%v", pending, err)
	}
	if stopped, err := supervisor.StopActiveRun(ctx, run.RunID, "late stop"); err != nil || stopped {
		t.Fatalf("StopActiveRun after executor exit = %v, %v", stopped, err)
	}
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(shutdown); err != nil {
			t.Error(err)
		}
	})
	select {
	case completed := <-faultStore.finalized:
		if completed.Status != domain.ProjectRunStatusSucceeded || completed.Output != "original successful result" {
			t.Fatalf("recovered run = %#v", completed)
		}
	case <-ctx.Done():
		t.Fatal("completion did not recover after executor exit")
	}
	if controller.executed.Load() != 1 {
		t.Fatalf("agent executed %d times, want exactly once", controller.executed.Load())
	}
}

type supervisorCompletionController struct {
	runSupervisorController
	run      domain.ProjectRunRecord
	store    *configstore.ConfigStore
	manager  *runs.CompletionManager
	executed atomic.Int32
}

func (c *supervisorCompletionController) StartProjectRun(context.Context, runs.RunAgentRequest) (runs.StartedProjectRun, error) {
	return runs.StartedProjectRun{
		Run: c.run,
		Execute: func(ctx context.Context, _ *runs.StreamSink) (domain.ProjectRunRecord, error, error) {
			c.executed.Add(1)
			started := c.run
			started.Status = domain.ProjectRunStatusRunning
			if _, err := c.store.UpdateProjectRun(ctx, started); err != nil {
				return started, nil, err
			}
			run, err := c.manager.Complete(ctx, runs.TransitionRequest{
				RunID: c.run.RunID, Status: domain.ProjectRunStatusSucceeded, Output: "original successful result",
			})
			return run, nil, err
		},
	}, nil
}

type supervisorCompletionFaultStore struct {
	runs.CompletionStore
	attempts          atomic.Int32
	finalized         chan domain.ProjectRunRecord
	commitBeforeError bool
}

func (s *supervisorCompletionFaultStore) StageProjectRunCompletion(ctx context.Context, record domain.ProjectRunCompletionRecord, events []domain.ProjectRunEventRecord) (domain.ProjectRunCompletionRecord, bool, error) {
	if s.attempts.Add(1) == 1 {
		if s.commitBeforeError {
			if _, _, err := s.CompletionStore.StageProjectRunCompletion(ctx, record, events); err != nil {
				return domain.ProjectRunCompletionRecord{}, false, err
			}
		}
		return domain.ProjectRunCompletionRecord{}, false, errors.New("injected first completion write failure")
	}
	return s.CompletionStore.StageProjectRunCompletion(ctx, record, events)
}

func (s *supervisorCompletionFaultStore) FinalizeProjectRunCompletion(ctx context.Context, run domain.ProjectRunRecord, event domain.ProjectRunEventRecord) (domain.ProjectRunRecord, error) {
	completed, err := s.CompletionStore.FinalizeProjectRunCompletion(ctx, run, event)
	if err == nil {
		select {
		case s.finalized <- completed:
		default:
		}
	}
	return completed, err
}
