package schedulers_test

import (
	"context"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

const schedulerTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

// TestRuntimeHostProjectAgentCarriesRootTraceContext covers the scheduler half
// of the trace contract: the W3C trace context on the scheduler's run context
// reaches the project-agent runner unchanged, so the run span the runner opens
// continues the same trace.
func TestRuntimeHostProjectAgentCarriesRootTraceContext(t *testing.T) {
	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{
		ID:                 "scheduler-trace",
		ProjectID:          "project-1",
		AgentName:          "reviewer",
		ProjectSchedulerID: "scheduler-1",
	}}
	run := &domain.SchedulerRunSummary{ID: "run-trace", SchedulerID: scheduler.Summary.ID, TriggerID: "trigger-1"}
	projectRunner := &hostProjectAgentRunnerFake{run: domain.ProjectRunRecord{
		RunID:     "project-run",
		ProjectID: "project-1",
		AgentName: "reviewer",
		Status:    domain.ProjectRunStatusSucceeded,
	}}
	host := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{
		Store:              &hostStoreFake{},
		Events:             &hostEventsFake{},
		ProjectAgentRunner: projectRunner,
		Publisher:          &hostPublisherFake{},
	}, scheduler, triggerExecution(run), schedulers.TriggerEventMetadata{EventID: "topic-event"})

	ctx := domain.NewContextWithTraceContext(context.Background(), domain.TraceContext{Traceparent: schedulerTraceparent})
	if _, err := host.Agent(ctx, "review", domain.SchedulerAgentRequest{}); err != nil {
		t.Fatalf("Project Agent returned error: %v", err)
	}
	if projectRunner.ctx == nil {
		t.Fatal("project-agent runner received no context")
	}
	if got := domain.TraceContextFromContext(projectRunner.ctx).Traceparent; got != schedulerTraceparent {
		t.Fatalf("runner traceparent = %q, want %q", got, schedulerTraceparent)
	}
}
