package schedulers_test

import (
	"context"
	"path/filepath"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

type schedulerObserverFake struct {
	statuses   []string
	previous   []string
	projectIDs []string
	agentNames []string
}

func (o *schedulerObserverFake) SchedulerEventAdded(context.Context, domain.SchedulerEvent, string, string) {
}

func (o *schedulerObserverFake) SchedulerRunStatusChanged(_ context.Context, previousStatus string, run domain.SchedulerRunSummary, projectID, agentName string) {
	o.statuses = append(o.statuses, run.Status)
	o.previous = append(o.previous, previousStatus)
	o.projectIDs = append(o.projectIDs, projectID)
	o.agentNames = append(o.agentNames, agentName)
}

func TestRunExecutorReportsSchedulerRunStatusChanges(t *testing.T) {
	ctx := context.Background()
	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{
		ID: "scheduler-1", Runtime: domain.SchedulerRuntimeScheduler, ProjectID: "project-1", AgentName: "worker",
	}, Script: "script"}
	store := &runStoreFake{}
	observer := &schedulerObserverFake{}
	executor := schedulers.NewRunExecutor(schedulers.RunExecutorDependencies{
		Store:  store,
		Engine: &schedulerEngineFake{result: schedulers.SchedulerExecutionResult{ResultJSON: `{"ok":true}`}},
		HostFactory: func(domain.Scheduler, schedulers.RuntimeExecutionContext, schedulers.TriggerEventMetadata) schedulers.RunHost {
			return &runHostFake{}
		},
		ArtifactsDir: func(schedulerID, runID string) string {
			return filepath.Join(t.TempDir(), schedulerID, runID)
		},
		WriteArtifact:     func(string, string, string) error { return nil },
		EnterRun:          func(domain.Scheduler) bool { return true },
		LeaveRun:          func(string) {},
		AddSchedulerEvent: func(context.Context, schedulers.SchedulerEventInput) error { return nil },
		Notify:            func(string) {},
		Observer:          observer,
	})
	run, err := executor.Run(ctx, schedulers.RunTriggerRequest{Scheduler: scheduler, PayloadJSON: `{}`, Source: "manual"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != domain.SchedulerRunStatusSucceeded {
		t.Fatalf("run status = %q", run.Status)
	}
	if len(observer.statuses) != 2 || observer.statuses[0] != domain.SchedulerRunStatusRunning || observer.statuses[1] != domain.SchedulerRunStatusSucceeded {
		t.Fatalf("observed statuses = %#v", observer.statuses)
	}
	if observer.previous[0] != "" || observer.previous[1] != domain.SchedulerRunStatusRunning {
		t.Fatalf("observed previous = %#v", observer.previous)
	}
	if observer.projectIDs[0] != "project-1" || observer.agentNames[0] != "worker" {
		t.Fatalf("observed ownership = %#v / %#v", observer.projectIDs, observer.agentNames)
	}
}
