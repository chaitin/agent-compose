package schedulers

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestSchedulerRunSupervisorStartCarriesRequestTrustedHeaders(t *testing.T) {
	store := newSupervisorRunStore()
	executed := make(chan []domain.TrustedHeader, 1)
	supervisor := newSchedulerRunSupervisor(schedulerRunSupervisorDependencies{
		RootCtx: context.Background(),
		Store:   store,
		LoadSchedulerForRun: func(context.Context, string, string) (domain.Scheduler, *domain.SchedulerTrigger, error) {
			return domain.Scheduler{Summary: domain.SchedulerSummary{ID: "scheduler-1"}}, nil, nil
		},
		Prepare: func(_ context.Context, req RunTriggerRequest) (PreparedRun, error) {
			if !req.StartedByRequest {
				t.Error("manually started run is not marked as started by a request")
			}
			return PreparedRun{Scheduler: req.Scheduler, Run: domain.SchedulerRunSummary{ID: "run-1", SchedulerID: "scheduler-1", Status: domain.SchedulerRunStatusRunning}}, nil
		},
		Execute: func(ctx context.Context, prepared PreparedRun) (domain.SchedulerRunSummary, error) {
			executed <- domain.TrustedHeadersFromContext(ctx)
			return prepared.Run, nil
		},
	})

	trusted := []domain.TrustedHeader{{Name: "x-mpi-user-id", Value: "user-1"}}
	requestCtx, cancelRequest := context.WithCancel(domain.NewContextWithTrustedHeaders(context.Background(), trusted))
	if _, err := supervisor.StartSchedulerRun(requestCtx, SchedulerRunRequest{SchedulerID: "scheduler-1", TriggerID: "trigger-1"}); err != nil {
		t.Fatalf("StartSchedulerRun returned error: %v", err)
	}
	// The run outlives the request that started it.
	cancelRequest()
	if got := <-executed; !reflect.DeepEqual(got, trusted) {
		t.Fatalf("executed trusted headers = %#v, want %#v", got, trusted)
	}
}

type trustedHeadersRecordingEngine struct {
	executed []domain.TrustedHeader
}

func (e *trustedHeadersRecordingEngine) Validate(context.Context, string, string) (SchedulerValidationResult, error) {
	return SchedulerValidationResult{}, nil
}

func (e *trustedHeadersRecordingEngine) Execute(ctx context.Context, _ SchedulerExecutionRequest, _ SchedulerHost) (SchedulerExecutionResult, error) {
	e.executed = domain.TrustedHeadersFromContext(ctx)
	return SchedulerExecutionResult{}, nil
}

func TestRunExecutorRunActsAsTrustedIdentity(t *testing.T) {
	applier := []domain.TrustedHeader{{Name: "x-mpi-user-id", Value: "applier"}}
	requester := []domain.TrustedHeader{{Name: "x-mpi-user-id", Value: "requester"}}
	lookupErr := errors.New("store unavailable")
	projectScheduler := domain.Scheduler{Summary: domain.SchedulerSummary{ID: "scheduler-1", ProjectID: "project-1"}}
	tests := []struct {
		name      string
		scheduler domain.Scheduler
		// request is nil for runs with no request of their own.
		request *[]domain.TrustedHeader
		lookup  func(context.Context, string) ([]domain.TrustedHeader, error)
		want    []domain.TrustedHeader
		wantErr error
	}{
		{
			name:      "run without a request acts as the project applier",
			scheduler: projectScheduler,
			lookup: func(_ context.Context, projectID string) ([]domain.TrustedHeader, error) {
				if projectID != "project-1" {
					t.Errorf("lookup project = %q", projectID)
				}
				return applier, nil
			},
			want: applier,
		},
		{
			name:      "run started by a request acts as the requester",
			scheduler: projectScheduler,
			request:   &requester,
			lookup: func(context.Context, string) ([]domain.TrustedHeader, error) {
				t.Error("request identity must not be replaced by the applier")
				return applier, nil
			},
			want: requester,
		},
		{
			name:      "run started by a request without trusted headers does not borrow the applier",
			scheduler: projectScheduler,
			request:   &[]domain.TrustedHeader{},
			lookup: func(context.Context, string) ([]domain.TrustedHeader, error) {
				t.Error("a request without trusted headers must not act as the applier")
				return applier, nil
			},
		},
		{
			name:      "scheduler without a project has no identity",
			scheduler: domain.Scheduler{Summary: domain.SchedulerSummary{ID: "scheduler-1"}},
			lookup: func(context.Context, string) ([]domain.TrustedHeader, error) {
				t.Error("scheduler without a project must not look up an applier")
				return applier, nil
			},
		},
		{
			name:      "lookup failure fails the run before it is recorded",
			scheduler: projectScheduler,
			lookup: func(context.Context, string) ([]domain.TrustedHeader, error) {
				return nil, lookupErr
			},
			wantErr: lookupErr,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &cancelRunStore{}
			engine := &trustedHeadersRecordingEngine{}
			artifactsDir := t.TempDir()
			executor := NewRunExecutor(RunExecutorDependencies{
				Store:                      store,
				Engine:                     engine,
				HostFactory:                func(domain.Scheduler, RuntimeExecutionContext, TriggerEventMetadata) RunHost { return nil },
				ArtifactsDir:               func(schedulerID, runID string) string { return filepath.Join(artifactsDir, schedulerID, runID) },
				WriteArtifact:              func(string, string, string) error { return nil },
				ProjectApplyTrustedHeaders: tt.lookup,
			})
			ctx := context.Background()
			request := RunTriggerRequest{Scheduler: tt.scheduler, PayloadJSON: `{}`, Source: "cron"}
			if tt.request != nil {
				ctx = domain.NewContextWithTrustedHeaders(ctx, *tt.request)
				request.Source, request.StartedByRequest = "manual", true
			}
			_, err := executor.Run(ctx, request)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Run error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if len(store.created) != 0 {
					t.Fatalf("failed identity lookup recorded runs %#v", store.created)
				}
				return
			}
			if !reflect.DeepEqual(engine.executed, tt.want) {
				t.Fatalf("executed trusted headers = %#v, want %#v", engine.executed, tt.want)
			}
		})
	}
}

func TestControllerRunNowActsAsRequesterNotApplier(t *testing.T) {
	ctx := context.Background()
	store := newControllerTestStore()
	scheduler := domain.Scheduler{
		Summary:  domain.SchedulerSummary{ID: "scheduler-1", Name: "Scheduler", Runtime: domain.SchedulerRuntimeScheduler, Enabled: true, ProjectID: "project-1"},
		Script:   "function main(){}",
		Triggers: []domain.SchedulerTrigger{{SchedulerID: "scheduler-1", ID: "trigger-1", Kind: domain.SchedulerTriggerKindEvent, Topic: "topic.test", Enabled: true}},
	}
	store.schedulers[scheduler.Summary.ID] = scheduler
	engine := &trustedHeadersRecordingEngine{}
	controller := NewController(ControllerDependencies{
		Store:       store,
		Engine:      engine,
		HostFactory: func(domain.Scheduler, RuntimeExecutionContext, TriggerEventMetadata) RunHost { return nil },
		Artifacts:   FSArtifacts{DataRoot: t.TempDir()},
		RunTimeout:  func(time.Duration) time.Duration { return time.Second },
		ProjectApplyTrustedHeaders: func(context.Context, string) ([]domain.TrustedHeader, error) {
			return []domain.TrustedHeader{{Name: "x-mpi-user-id", Value: "applier"}}, nil
		},
	})
	if err := controller.Refresh(ctx); err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}

	// A request that reaches the daemon without trusted headers stays without
	// an identity instead of acting as whoever last applied the Project.
	if _, err := controller.RunNow(ctx, RunNowRequest{SchedulerID: "scheduler-1", TriggerID: "trigger-1", PayloadJSON: `{}`}); err != nil {
		t.Fatalf("RunNow returned error: %v", err)
	}
	if engine.executed != nil {
		t.Fatalf("anonymous RunNow executed as %#v", engine.executed)
	}

	requester := []domain.TrustedHeader{{Name: "x-mpi-user-id", Value: "requester"}}
	if _, err := controller.RunNow(domain.NewContextWithTrustedHeaders(ctx, requester), RunNowRequest{SchedulerID: "scheduler-1", TriggerID: "trigger-1", PayloadJSON: `{}`}); err != nil {
		t.Fatalf("RunNow returned error: %v", err)
	}
	if !reflect.DeepEqual(engine.executed, requester) {
		t.Fatalf("RunNow executed as %#v, want %#v", engine.executed, requester)
	}
}
