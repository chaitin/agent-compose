package schedulers

import (
	"context"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

type invocationContextEngine struct {
	invocationEngineFake
	executeHeaders []domain.TrustedHeader
}

func (e *invocationContextEngine) Execute(ctx context.Context, request SchedulerExecutionRequest, host SchedulerHost) (SchedulerExecutionResult, error) {
	e.executeHeaders = domain.TrustedHeadersFromContext(ctx)
	return e.invocationEngineFake.Execute(ctx, request, host)
}

// Characterizes that InvokeScheduler, unlike the persistent run supervisor,
// executes the scheduler script on the caller's request context, so request
// trusted headers are visible to the script host.
func TestInvocationExecutorPassesRequestTrustedHeadersToExecution(t *testing.T) {
	engine := &invocationContextEngine{}
	executor := NewInvocationExecutor(InvocationExecutorDependencies{
		Engine: engine,
		HostFactory: func(domain.Scheduler, RuntimeExecutionContext, TriggerEventMetadata) RunHost {
			return &invocationHostFake{}
		},
	})
	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{ID: "scheduler-1", Runtime: domain.SchedulerRuntimeScheduler}, Script: "function main() {}"}
	trusted := []domain.TrustedHeader{{Name: "x-mpi-user", Value: "bob"}}

	ctx := domain.NewContextWithTrustedHeaders(context.Background(), trusted)
	if _, err := executor.Invoke(ctx, scheduler, `{}`); err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if len(engine.executeHeaders) != 1 || engine.executeHeaders[0] != trusted[0] {
		t.Fatalf("Execute headers = %#v, want %#v", engine.executeHeaders, trusted)
	}
}
