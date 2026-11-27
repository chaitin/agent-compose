package schedulers_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

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

type recoveryStoreFake struct {
	schedulers.ControllerStore
	runs      []domain.SchedulerRunSummary
	updated   []domain.SchedulerRunSummary
	events    []domain.SchedulerEvent
	scheduler domain.Scheduler
}

func (s *recoveryStoreFake) ListInterruptedSchedulerRuns(context.Context, time.Time) ([]domain.SchedulerRunSummary, error) {
	return append([]domain.SchedulerRunSummary(nil), s.runs...), nil
}

func (s *recoveryStoreFake) UpdateSchedulerRun(_ context.Context, run domain.SchedulerRunSummary) error {
	s.updated = append(s.updated, run)
	return nil
}

func (s *recoveryStoreFake) AddSchedulerEvent(_ context.Context, event domain.SchedulerEvent) error {
	s.events = append(s.events, event)
	return nil
}

func (s *recoveryStoreFake) GetScheduler(context.Context, string) (domain.Scheduler, error) {
	return s.scheduler, nil
}

func TestRecoveryReportsInterruptedSchedulerRunStatus(t *testing.T) {
	ctx := context.Background()
	store := &recoveryStoreFake{
		runs: []domain.SchedulerRunSummary{{
			ID: "run-1", SchedulerID: "scheduler-1", Status: domain.SchedulerRunStatusRunning, StartedAt: time.Now().Add(-time.Minute),
		}},
		scheduler: domain.Scheduler{Summary: domain.SchedulerSummary{ID: "scheduler-1", ProjectID: "p1", AgentName: "worker"}},
	}
	observer := &schedulerObserverFake{}
	controller := schedulers.NewController(schedulers.ControllerDependencies{
		Store:    store,
		Observer: observer,
	})
	if err := controller.RecoverInterruptedRuns(ctx, time.Now()); err != nil {
		t.Fatalf("RecoverInterruptedRuns: %v", err)
	}
	if len(store.updated) != 1 || store.updated[0].Status != domain.SchedulerRunStatusFailed {
		t.Fatalf("updated runs = %+v", store.updated)
	}
	if len(observer.statuses) != 1 || observer.statuses[0] != domain.SchedulerRunStatusFailed ||
		observer.previous[0] != domain.SchedulerRunStatusRunning ||
		observer.projectIDs[0] != "p1" || observer.agentNames[0] != "worker" {
		t.Fatalf("observed recovery status = %#v / %#v / %#v / %#v", observer.statuses, observer.previous, observer.projectIDs, observer.agentNames)
	}
	if len(store.events) != 1 {
		t.Fatalf("stored events = %d, want 1", len(store.events))
	}
}
