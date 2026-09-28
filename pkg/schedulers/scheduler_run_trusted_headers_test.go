package schedulers

import (
	"context"
	"reflect"
	"testing"

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
