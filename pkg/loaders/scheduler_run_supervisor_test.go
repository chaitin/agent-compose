package loaders

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	domain "agent-compose/pkg/model"
)

func TestRunExecutorCancellationWritesCanceledTerminalState(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	store := &cancelRunStore{}
	engine := &cancelRunEngine{started: make(chan struct{})}
	var events []string
	artifactsDir := t.TempDir()
	executor := NewRunExecutor(RunExecutorDependencies{
		Store:       store,
		Engine:      engine,
		HostFactory: func(domain.Loader, RuntimeExecutionContext, TriggerEventMetadata) RunHost { return nil },
		ArtifactsDir: func(loaderID, runID string) string {
			return filepath.Join(artifactsDir, loaderID, runID)
		},
		WriteArtifact: func(string, string, string) error { return nil },
		AddLoaderEvent: func(_ context.Context, _, _, _, eventType, _, _ string, _ any, _, _, _ string) error {
			events = append(events, eventType)
			return nil
		},
	})
	result := make(chan domain.LoaderRunSummary, 1)
	errResult := make(chan error, 1)
	go func() {
		run, err := executor.Run(ctx, domain.Loader{
			Summary: domain.LoaderSummary{ID: "loader-1", Runtime: domain.LoaderRuntimeScheduler},
			Script:  "function main() {}",
		}, nil, `{}`, "manual", RunOptions{})
		result <- run
		errResult <- err
	}()

	<-engine.started
	cancel(errors.New("user stop"))
	run := <-result
	if err := <-errResult; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if run.Status != domain.LoaderRunStatusCanceled || run.Error != "user stop" {
		t.Fatalf("run = %#v", run)
	}
	if len(store.updated) != 1 || store.updated[0].Status != domain.LoaderRunStatusCanceled {
		t.Fatalf("updated runs = %#v", store.updated)
	}
	if !slices.Contains(events, "loader.run.canceled") || slices.Contains(events, "loader.run.failed") {
		t.Fatalf("events = %#v", events)
	}
}

func TestSchedulerRunSupervisorRunReturnsFinalResult(t *testing.T) {
	store := newSupervisorRunStore()
	supervisor := newSchedulerRunSupervisor(schedulerRunSupervisorDependencies{
		RootCtx: context.Background(),
		Store:   store,
		LoadLoaderForRun: func(context.Context, string, string) (domain.Loader, *domain.LoaderTrigger, error) {
			return domain.Loader{Summary: domain.LoaderSummary{ID: "loader-1"}}, nil, nil
		},
		Prepare: func(_ context.Context, loader domain.Loader, _ *domain.LoaderTrigger, _, _ string, _ RunOptions) (PreparedRun, error) {
			return PreparedRun{Loader: loader, Run: domain.LoaderRunSummary{ID: "run-success", LoaderID: loader.Summary.ID, Status: domain.LoaderRunStatusRunning}}, nil
		},
		Execute: func(_ context.Context, prepared PreparedRun) (domain.LoaderRunSummary, error) {
			run := prepared.Run
			run.Status = domain.LoaderRunStatusSucceeded
			run.ResultJSON = `{"ok":true}`
			store.set(run)
			return run, nil
		},
	})

	run, err := supervisor.Run(context.Background(), SchedulerRunRequest{LoaderID: "loader-1", TriggerID: "trigger-1"})
	if err != nil || run.Status != domain.LoaderRunStatusSucceeded || run.ResultJSON != `{"ok":true}` {
		t.Fatalf("Run run=%#v err=%v", run, err)
	}
}

func TestSchedulerRunSupervisorRejectsEmptyTriggerWithoutPreparingRun(t *testing.T) {
	prepareCalls := 0
	supervisor := newSchedulerRunSupervisor(schedulerRunSupervisorDependencies{
		LoadLoaderForRun: func(context.Context, string, string) (domain.Loader, *domain.LoaderTrigger, error) {
			t.Fatal("empty trigger must be rejected before loading")
			return domain.Loader{}, nil, nil
		},
		Prepare: func(context.Context, domain.Loader, *domain.LoaderTrigger, string, string, RunOptions) (PreparedRun, error) {
			prepareCalls++
			return PreparedRun{}, nil
		},
	})
	if _, err := supervisor.Run(context.Background(), SchedulerRunRequest{LoaderID: "loader-1"}); !errors.Is(err, domain.ErrRequired) || prepareCalls != 0 {
		t.Fatalf("empty trigger err=%v prepareCalls=%d", err, prepareCalls)
	}
}

func TestSchedulerRunSupervisorTimeoutCancelsExecution(t *testing.T) {
	store := newSupervisorRunStore()
	supervisor := newSchedulerRunSupervisor(schedulerRunSupervisorDependencies{
		RootCtx: context.Background(),
		Store:   store,
		LoadLoaderForRun: func(context.Context, string, string) (domain.Loader, *domain.LoaderTrigger, error) {
			return domain.Loader{Summary: domain.LoaderSummary{ID: "loader-1"}}, nil, nil
		},
		Prepare: func(_ context.Context, loader domain.Loader, _ *domain.LoaderTrigger, _, _ string, _ RunOptions) (PreparedRun, error) {
			return PreparedRun{Loader: loader, Run: domain.LoaderRunSummary{ID: "run-timeout", LoaderID: loader.Summary.ID, Status: domain.LoaderRunStatusRunning}}, nil
		},
		Execute: func(ctx context.Context, prepared PreparedRun) (domain.LoaderRunSummary, error) {
			<-ctx.Done()
			run := prepared.Run
			run.Status = domain.LoaderRunStatusCanceled
			run.Error = context.Cause(ctx).Error()
			store.set(run)
			return run, nil
		},
	})

	run, err := supervisor.Run(context.Background(), SchedulerRunRequest{LoaderID: "loader-1", TriggerID: "trigger-1", Timeout: 10 * time.Millisecond})
	if err != nil || run.Status != domain.LoaderRunStatusCanceled || run.Error != errSchedulerRunTimedOut.Error() {
		t.Fatalf("Run run=%#v err=%v", run, err)
	}
}

func TestSchedulerRunSupervisorStopWaitsForExecutorTerminalState(t *testing.T) {
	store := newSupervisorRunStore()
	started := make(chan struct{})
	supervisor := newSchedulerRunSupervisor(schedulerRunSupervisorDependencies{
		RootCtx: context.Background(),
		Store:   store,
		LoadLoaderForRun: func(context.Context, string, string) (domain.Loader, *domain.LoaderTrigger, error) {
			return domain.Loader{Summary: domain.LoaderSummary{ID: "loader-1"}}, nil, nil
		},
		Prepare: func(_ context.Context, loader domain.Loader, _ *domain.LoaderTrigger, payloadJSON, source string, _ RunOptions) (PreparedRun, error) {
			run := domain.LoaderRunSummary{ID: "run-1", LoaderID: loader.Summary.ID, Status: domain.LoaderRunStatusRunning, PayloadJSON: payloadJSON, TriggerSource: source}
			store.set(run)
			return PreparedRun{Loader: loader, Run: run, PayloadJSON: payloadJSON}, nil
		},
		Execute: func(ctx context.Context, prepared PreparedRun) (domain.LoaderRunSummary, error) {
			close(started)
			<-ctx.Done()
			run := prepared.Run
			run.Status = domain.LoaderRunStatusCanceled
			run.Error = context.Cause(ctx).Error()
			store.set(run)
			return run, nil
		},
	})

	created, err := supervisor.Start(context.Background(), SchedulerRunRequest{LoaderID: "loader-1", TriggerID: "trigger-1", PayloadJSON: `{"key":true}`})
	if err != nil || created.Status != domain.LoaderRunStatusRunning {
		t.Fatalf("Start run=%#v err=%v", created, err)
	}
	<-started
	stopped, requested, err := supervisor.Stop(context.Background(), "loader-1", created.ID, "user stop")
	if err != nil || !requested || stopped.Status != domain.LoaderRunStatusCanceled || stopped.Error != "user stop" {
		t.Fatalf("Stop run=%#v requested=%v err=%v", stopped, requested, err)
	}
	current, requested, err := supervisor.Stop(context.Background(), "loader-1", created.ID, "stop again")
	if err != nil || requested || current.Status != domain.LoaderRunStatusCanceled || current.Error != "user stop" {
		t.Fatalf("second Stop run=%#v requested=%v err=%v", current, requested, err)
	}
	runs, err := supervisor.List(context.Background(), "loader-1", 10)
	if err != nil || len(runs) != 1 || runs[0].ID != created.ID {
		t.Fatalf("List runs=%#v err=%v", runs, err)
	}
}

func TestSchedulerRunSupervisorStopsQJSPendingPromise(t *testing.T) {
	store := newSupervisorRunStore()
	host := &pendingPromiseRunHost{
		engineCancellationHost: engineCancellationHost{started: make(chan struct{})},
	}
	var (
		eventMu sync.Mutex
		events  []string
	)
	artifactsDir := t.TempDir()
	executor := NewRunExecutor(RunExecutorDependencies{
		Store:  store,
		Engine: &QJSLoaderEngine{},
		HostFactory: func(domain.Loader, RuntimeExecutionContext, TriggerEventMetadata) RunHost {
			return host
		},
		ArtifactsDir: func(loaderID, runID string) string {
			return filepath.Join(artifactsDir, loaderID, runID)
		},
		WriteArtifact: func(string, string, string) error { return nil },
		AddLoaderEvent: func(_ context.Context, _, _, _, eventType, _, _ string, _ any, _, _, _ string) error {
			eventMu.Lock()
			defer eventMu.Unlock()
			events = append(events, eventType)
			return nil
		},
	})
	loader := domain.Loader{
		Summary: domain.LoaderSummary{ID: "loader-qjs-pending", Runtime: domain.LoaderRuntimeScheduler},
		Script: `
scheduler.interval("pending", async function pending() {
  scheduler.log("callback started");
  await new Promise(function neverResolve() {});
}, 86400000);`,
	}
	trigger := &domain.LoaderTrigger{ID: "pending", Kind: "interval"}
	supervisor := newSchedulerRunSupervisor(schedulerRunSupervisorDependencies{
		RootCtx: context.Background(),
		Store:   store,
		LoadLoaderForRun: func(context.Context, string, string) (domain.Loader, *domain.LoaderTrigger, error) {
			return loader, trigger, nil
		},
		Prepare: executor.Prepare,
		Execute: executor.Execute,
	})

	created, err := supervisor.Start(context.Background(), SchedulerRunRequest{
		LoaderID:  loader.Summary.ID,
		TriggerID: trigger.ID,
	})
	if err != nil || created.Status != domain.LoaderRunStatusRunning {
		t.Fatalf("Start run=%#v err=%v", created, err)
	}
	select {
	case <-host.started:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler callback did not start")
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelStop()
	stopped, requested, err := supervisor.Stop(stopCtx, loader.Summary.ID, created.ID, "operator stop")
	if err != nil || !requested {
		t.Fatalf("Stop run=%#v requested=%v err=%v", stopped, requested, err)
	}
	if stopped.Status != domain.LoaderRunStatusCanceled || stopped.Error != "operator stop" || stopped.CompletedAt.IsZero() {
		t.Fatalf("stopped run = %#v", stopped)
	}
	eventMu.Lock()
	defer eventMu.Unlock()
	if !slices.Equal(events, []string{"loader.run.started", "loader.run.canceled"}) {
		t.Fatalf("events = %#v", events)
	}
}

func TestSchedulerRunSupervisorRootContextStopsBackgroundRun(t *testing.T) {
	root, cancelRoot := context.WithCancel(context.Background())
	store := newSupervisorRunStore()
	started := make(chan struct{})
	completed := make(chan struct{})
	supervisor := newSchedulerRunSupervisor(schedulerRunSupervisorDependencies{
		RootCtx: root,
		Store:   store,
		LoadLoaderForRun: func(context.Context, string, string) (domain.Loader, *domain.LoaderTrigger, error) {
			return domain.Loader{Summary: domain.LoaderSummary{ID: "loader-1"}}, nil, nil
		},
		Prepare: func(_ context.Context, loader domain.Loader, _ *domain.LoaderTrigger, _, _ string, _ RunOptions) (PreparedRun, error) {
			run := domain.LoaderRunSummary{ID: "run-root", LoaderID: loader.Summary.ID, Status: domain.LoaderRunStatusRunning}
			store.set(run)
			return PreparedRun{Loader: loader, Run: run}, nil
		},
		Execute: func(ctx context.Context, prepared PreparedRun) (domain.LoaderRunSummary, error) {
			close(started)
			<-ctx.Done()
			run := prepared.Run
			run.Status = domain.LoaderRunStatusCanceled
			run.Error = context.Cause(ctx).Error()
			store.set(run)
			close(completed)
			return run, nil
		},
	})

	if _, err := supervisor.Start(context.Background(), SchedulerRunRequest{LoaderID: "loader-1", TriggerID: "trigger-1"}); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	<-started
	cancelRoot()
	<-completed
	run, err := supervisor.Get(context.Background(), "loader-1", "run-root")
	if err != nil || run.Status != domain.LoaderRunStatusCanceled || run.Error != context.Canceled.Error() {
		t.Fatalf("Get run=%#v err=%v", run, err)
	}
}

type cancelRunEngine struct {
	started chan struct{}
}

func (e *cancelRunEngine) Validate(context.Context, string, string) (LoaderValidationResult, error) {
	return LoaderValidationResult{}, nil
}

func (e *cancelRunEngine) Execute(ctx context.Context, _ LoaderExecutionRequest, _ LoaderHost) (LoaderExecutionResult, error) {
	close(e.started)
	<-ctx.Done()
	return LoaderExecutionResult{}, ctx.Err()
}

type cancelRunStore struct {
	created   []domain.LoaderRunSummary
	updated   []domain.LoaderRunSummary
	lastError string
}

func (s *cancelRunStore) CreateLoaderRun(_ context.Context, run domain.LoaderRunSummary) error {
	s.created = append(s.created, run)
	return nil
}

func (s *cancelRunStore) UpdateLoaderRun(_ context.Context, run domain.LoaderRunSummary) error {
	s.updated = append(s.updated, run)
	return nil
}

func (s *cancelRunStore) UpdateLoaderLastError(_ context.Context, _ string, lastError string) error {
	s.lastError = lastError
	return nil
}

type supervisorRunStore struct {
	mu   sync.Mutex
	runs map[string]domain.LoaderRunSummary
}

type pendingPromiseRunHost struct {
	engineCancellationHost
}

func (*pendingPromiseRunHost) CleanupCommandSessions(context.Context) {}

func newSupervisorRunStore() *supervisorRunStore {
	return &supervisorRunStore{runs: map[string]domain.LoaderRunSummary{}}
}

func (s *supervisorRunStore) set(run domain.LoaderRunSummary) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.LoaderID+"/"+run.ID] = run
}

func (s *supervisorRunStore) CreateLoaderRun(_ context.Context, run domain.LoaderRunSummary) error {
	s.set(run)
	return nil
}

func (s *supervisorRunStore) UpdateLoaderRun(_ context.Context, run domain.LoaderRunSummary) error {
	s.set(run)
	return nil
}

func (*supervisorRunStore) UpdateLoaderLastError(context.Context, string, string) error {
	return nil
}

func (s *supervisorRunStore) GetLoaderRun(_ context.Context, loaderID, runID string) (domain.LoaderRunSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[loaderID+"/"+runID]
	if !ok {
		return domain.LoaderRunSummary{}, domain.ResourceError(domain.ErrNotFound, "scheduler run", loaderID+"/"+runID, "scheduler run not found", nil)
	}
	return run, nil
}

func (s *supervisorRunStore) ListLoaderRuns(_ context.Context, loaderID string, limit int) ([]domain.LoaderRunSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runs := make([]domain.LoaderRunSummary, 0, len(s.runs))
	for _, run := range s.runs {
		if run.LoaderID == loaderID {
			runs = append(runs, run)
		}
	}
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}
