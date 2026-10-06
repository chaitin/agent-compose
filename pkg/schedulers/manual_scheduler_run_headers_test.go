package schedulers

import (
	"context"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// recordingExecuteEngine records the context a scheduler script executes on, so
// a test can assert which identity the execution reached the runtime with.
type recordingExecuteEngine struct {
	executeCtx context.Context
}

func (e *recordingExecuteEngine) Validate(context.Context, string, string) (SchedulerValidationResult, error) {
	return SchedulerValidationResult{}, nil
}

func (e *recordingExecuteEngine) Execute(ctx context.Context, _ SchedulerExecutionRequest, _ SchedulerHost) (SchedulerExecutionResult, error) {
	e.executeCtx = ctx
	return SchedulerExecutionResult{ResultJSON: `{"ok":true}`}, nil
}

// A manual RunScheduler/StartSchedulerRun is started by a caller, so the
// caller's trusted headers must reach the execution even though it runs on a
// context derived from RootCtx rather than on the request context.
func TestSchedulerRunSupervisorTrustedHeadersAcrossManualRun(t *testing.T) {
	for name, start := range map[string]func(*SchedulerRunSupervisor, context.Context, SchedulerRunRequest) error{
		"RunScheduler": func(s *SchedulerRunSupervisor, ctx context.Context, req SchedulerRunRequest) error {
			_, err := s.RunScheduler(ctx, req)
			return err
		},
		"StartSchedulerRun": func(s *SchedulerRunSupervisor, ctx context.Context, req SchedulerRunRequest) error {
			_, err := s.StartSchedulerRun(ctx, req)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) { testSchedulerRunSupervisorTrustedHeaders(t, start) })
	}
}

func testSchedulerRunSupervisorTrustedHeaders(t *testing.T, start func(*SchedulerRunSupervisor, context.Context, SchedulerRunRequest) error) {
	store := newSupervisorRunStore()
	var prepareHeaders, executeHeaders []domain.TrustedHeader
	var prepareSource string
	executed := make(chan struct{})
	supervisor := newSchedulerRunSupervisor(schedulerRunSupervisorDependencies{
		RootCtx: context.Background(),
		Store:   store,
		LoadSchedulerForRun: func(context.Context, string, string) (domain.Scheduler, *domain.SchedulerTrigger, error) {
			return domain.Scheduler{Summary: domain.SchedulerSummary{ID: "scheduler-1"}}, nil, nil
		},
		Prepare: func(ctx context.Context, req RunTriggerRequest) (PreparedRun, error) {
			prepareHeaders = domain.TrustedHeadersFromContext(ctx)
			prepareSource = req.Source
			return PreparedRun{Scheduler: req.Scheduler, Run: domain.SchedulerRunSummary{ID: "run-1", SchedulerID: "scheduler-1", Status: domain.SchedulerRunStatusRunning}}, nil
		},
		Execute: func(ctx context.Context, prepared PreparedRun) (domain.SchedulerRunSummary, error) {
			executeHeaders = domain.TrustedHeadersFromContext(ctx)
			run := prepared.Run
			run.Status = domain.SchedulerRunStatusSucceeded
			store.set(run)
			close(executed)
			return run, nil
		},
	})

	trusted := []domain.TrustedHeader{{Name: "x-mpi-user", Value: "bob"}}
	requestCtx, cancelRequest := context.WithCancel(domain.NewContextWithTrustedHeaders(context.Background(), trusted))
	ctx := requestCtx
	if err := start(supervisor, ctx, SchedulerRunRequest{SchedulerID: "scheduler-1", TriggerID: "trigger-1"}); err != nil {
		t.Fatalf("start returned error: %v", err)
	}
	<-executed

	if prepareSource != "manual" {
		t.Fatalf("Prepare source = %q, want manual", prepareSource)
	}
	if len(prepareHeaders) != 1 {
		t.Fatalf("Prepare headers = %#v, want the request headers", prepareHeaders)
	}
	if len(executeHeaders) != 1 || executeHeaders[0] != trusted[0] {
		t.Fatalf("Execute headers = %#v, want the caller's %#v", executeHeaders, trusted)
	}
	cancelRequest()
}

// RunNow is the third manual entry point, and the one a caller reaches without
// the supervisor's own start path. Its execution also belongs to the daemon root
// rather than to the request, so the caller's identity has to be restored onto
// that context explicitly instead of being silently dropped with the transport
// context.
func TestControllerRunNowTrustedHeadersAcrossManualRun(t *testing.T) {
	store := newControllerTestStore()
	scheduler := domain.Scheduler{
		Summary:  domain.SchedulerSummary{ID: "scheduler-1", Name: "Scheduler", Runtime: domain.SchedulerRuntimeScheduler, Enabled: true},
		Script:   "function main(){}",
		Triggers: []domain.SchedulerTrigger{{SchedulerID: "scheduler-1", ID: "trigger-1", Kind: domain.SchedulerTriggerKindEvent, Topic: "topic.test", Enabled: true}},
	}
	store.schedulers[scheduler.Summary.ID] = scheduler
	engine := &recordingExecuteEngine{}
	controller := NewController(ControllerDependencies{
		Store:  store,
		Engine: engine,
		HostFactory: func(domain.Scheduler, RuntimeExecutionContext, TriggerEventMetadata) RunHost {
			return nil
		},
		Notifier:   &controllerTestNotifier{},
		Publisher:  &controllerTestPublisher{},
		Artifacts:  FSArtifacts{DataRoot: t.TempDir()},
		Schedulers: map[string]domain.Scheduler{},
		Running:    map[string]int{},
		RunTimeout: func(time.Duration) time.Duration { return time.Second },
	})

	trusted := []domain.TrustedHeader{{Name: "x-mpi-user", Value: "bob"}}
	ctx := domain.NewContextWithTrustedHeaders(context.Background(), trusted)
	run, err := controller.RunNow(ctx, RunNowRequest{SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-1", PayloadJSON: `{"manual":true}`})
	if err != nil {
		t.Fatalf("RunNow returned error: %v", err)
	}
	if run.Status != domain.SchedulerRunStatusSucceeded {
		t.Fatalf("RunNow run = %#v, want a succeeded run", run)
	}

	got := domain.TrustedHeadersFromContext(engine.executeCtx)
	if len(got) != 1 || got[0] != trusted[0] {
		t.Fatalf("Execute headers = %#v, want the caller's %#v", got, trusted)
	}
}
