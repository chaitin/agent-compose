package schedulers

import (
	"context"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// Characterizes how request-scoped trusted headers cross the manual
// RunScheduler/StartSchedulerRun path: Prepare runs on the request context,
// Execute runs on a context derived from RootCtx.
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

	ctx := domain.NewContextWithTrustedHeaders(context.Background(), []domain.TrustedHeader{{Name: "x-mpi-user", Value: "bob"}})
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
	if len(executeHeaders) != 0 {
		t.Fatalf("Execute headers = %#v; request headers unexpectedly reach scheduler execution", executeHeaders)
	}
}
