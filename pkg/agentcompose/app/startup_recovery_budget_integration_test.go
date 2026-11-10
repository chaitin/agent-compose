package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

// Reproduce a slow runtime reconciliation exhausting the real startup budget.
// The runtime is a fake; run recovery and its persisted state use real SQLite.
func TestStartupRecoveryBudgetDoesNotStrandInterruptedRunIntegration(t *testing.T) {
	root := t.TempDir()
	config := &appconfig.Config{DataRoot: root, SandboxRoot: filepath.Join(root, "sandboxes"), DbAddr: filepath.Join(root, "data.db")}
	store, sandboxes, err := testutil.OpenStores(t, config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "budget-project", Name: "budget", SourcePath: filepath.Join(root, "compose.yml"), SourceJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{ProjectID: project.ID, AgentName: "worker"}); err != nil {
		t.Fatal(err)
	}
	scheduler, err := store.UpsertProjectScheduler(ctx, domain.ProjectSchedulerRecord{ID: "budget-scheduler", ProjectID: project.ID, AgentName: "worker", SchedulerID: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	run := domain.SchedulerRunSummary{ID: "interrupted-run", SchedulerID: scheduler.ID, TriggerID: "trigger", Status: domain.SchedulerRunStatusRunning, StartedAt: time.Now().Add(-time.Hour)}
	if err := store.CreateSchedulerRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := sandboxes.CreateSandbox(ctx, "slow runtime", "", "docker", "", "", "", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	controller := schedulers.NewController(schedulers.ControllerDependencies{RootCtx: ctx, Store: store})
	schedulerRecovery := &budgetRecoveryScheduler{Controller: controller}
	completion := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: store})
	t.Cleanup(func() {
		shutdown, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		if err := completion.Shutdown(shutdown); err != nil {
			t.Error(err)
		}
	})
	if err := startBackgroundManagers(ctx, backgroundManagersDeps{Sandboxes: sandboxes, Bridge: budgetExhaustingRuntime{}, Schedulers: schedulerRecovery, Completions: completion}); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetSchedulerRun(ctx, scheduler.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, stopErr := controller.SchedulerRuns().StopSchedulerRun(ctx, scheduler.ID, run.ID, "timeout recovery")
	t.Logf("after startup: status=%s recovery_error=%v scheduling_started=%t stop_failed_precondition=%t", current.Status, schedulerRecovery.recoveryErr, schedulerRecovery.started, errors.Is(stopErr, domain.ErrFailedPrecondition))
	if current.Status != domain.SchedulerRunStatusFailed {
		t.Fatalf("interrupted run remains %q after startup; recovery error: %v", current.Status, schedulerRecovery.recoveryErr)
	}
	if stopErr != nil {
		t.Fatalf("recovered terminal run is still unmanageable: %v", stopErr)
	}
	if !schedulerRecovery.started {
		t.Fatal("scheduler did not start after recovering the interrupted run")
	}
}

func TestStartupRecoveryFailureDoesNotStartSchedulersIntegration(t *testing.T) {
	root := t.TempDir()
	config := &appconfig.Config{DataRoot: root, SandboxRoot: filepath.Join(root, "sandboxes"), DbAddr: filepath.Join(root, "data.db")}
	_, sandboxes, err := testutil.OpenStores(t, config)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("recovery store unavailable")
	scheduler := &budgetRecoveryScheduler{failRecovery: want}
	err = startBackgroundManagers(context.Background(), backgroundManagersDeps{Sandboxes: sandboxes, Bridge: budgetExhaustingRuntime{}, Schedulers: scheduler})
	if !errors.Is(err, want) {
		t.Fatalf("startup error = %v, want recovery failure %v", err, want)
	}
	if scheduler.started {
		t.Fatal("scheduler started despite failed run recovery")
	}
}

type budgetExhaustingRuntime struct{}

func (budgetExhaustingRuntime) ReconcileRuntimeState(ctx context.Context, sandbox *domain.Sandbox) (*domain.Sandbox, error) {
	<-ctx.Done()
	return sandbox, ctx.Err()
}

func (budgetExhaustingRuntime) StopSandbox(context.Context, string) (*domain.Sandbox, error) {
	return nil, errors.New("unexpected sandbox stop")
}

type budgetRecoveryScheduler struct {
	*schedulers.Controller
	recoveryErr  error
	failRecovery error
	started      bool
}

func (s *budgetRecoveryScheduler) RecoverInterruptedRuns(ctx context.Context, startedAt time.Time) error {
	if s.failRecovery != nil {
		return s.failRecovery
	}
	s.recoveryErr = s.Controller.RecoverInterruptedRuns(ctx, startedAt)
	return s.recoveryErr
}

func (s *budgetRecoveryScheduler) Start() { s.started = true }
