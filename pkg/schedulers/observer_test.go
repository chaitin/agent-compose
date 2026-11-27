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
	updates    []schedulers.SchedulerChange
}

func (o *schedulerObserverFake) SchedulerEventAdded(context.Context, domain.SchedulerEvent, string, string) {
}

func (o *schedulerObserverFake) SchedulerUpdated(_ context.Context, _ domain.Scheduler, change schedulers.SchedulerChange) {
	o.updates = append(o.updates, change)
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

type schedulerUpdateStoreFake struct {
	schedulers.ControllerStore
	items map[string]domain.Scheduler
}

func (s *schedulerUpdateStoreFake) ListSchedulers(context.Context) ([]domain.Scheduler, error) {
	items := make([]domain.Scheduler, 0, len(s.items))
	for _, item := range s.items {
		items = append(items, item)
	}
	return items, nil
}

func (s *schedulerUpdateStoreFake) GetScheduler(_ context.Context, schedulerID string) (domain.Scheduler, error) {
	return s.items[schedulerID], nil
}

func (s *schedulerUpdateStoreFake) SetSchedulerEnabled(_ context.Context, schedulerID string, enabled bool) error {
	item := s.items[schedulerID]
	item.Summary.Enabled = enabled
	s.items[schedulerID] = item
	return nil
}

func (s *schedulerUpdateStoreFake) SetSchedulerTriggerEnabled(_ context.Context, schedulerID, triggerID string, enabled bool) error {
	item := s.items[schedulerID]
	for index := range item.Triggers {
		if item.Triggers[index].ID == triggerID {
			item.Triggers[index].Enabled = enabled
		}
	}
	s.items[schedulerID] = item
	return nil
}

func (s *schedulerUpdateStoreFake) SetSchedulerTriggerNextFireAt(context.Context, string, string, time.Time) error {
	return nil
}

func TestSchedulerEnableAndTriggerChangesAreObserved(t *testing.T) {
	ctx := context.Background()
	scheduler := domain.Scheduler{
		Summary:  domain.SchedulerSummary{ID: "scheduler-1", ProjectID: "project-1", AgentName: "worker", Enabled: true},
		Triggers: []domain.SchedulerTrigger{{ID: "trigger-1", Enabled: true, Kind: domain.SchedulerTriggerKindEvent}},
	}
	store := &schedulerUpdateStoreFake{items: map[string]domain.Scheduler{"scheduler-1": scheduler}}
	observer := &schedulerObserverFake{}
	controller := schedulers.NewController(schedulers.ControllerDependencies{Store: store, Observer: observer})
	controller.ReplaceCachedSchedulers(map[string]domain.Scheduler{"scheduler-1": scheduler})

	if _, err := controller.SetSchedulerEnabled(ctx, "scheduler-1", false); err != nil {
		t.Fatalf("SetSchedulerEnabled: %v", err)
	}
	if len(observer.updates) != 1 || observer.updates[0].Reason != schedulers.SchedulerChangeDisabled {
		t.Fatalf("disable updates = %+v", observer.updates)
	}
	if _, err := controller.SetSchedulerTriggerEnabled(ctx, "scheduler-1", "trigger-1", false); err != nil {
		t.Fatalf("SetSchedulerTriggerEnabled: %v", err)
	}
	if len(observer.updates) != 2 || observer.updates[1].Reason != schedulers.SchedulerChangeTriggerOff ||
		observer.updates[1].TriggerID != "trigger-1" || observer.updates[1].TriggerEnabled {
		t.Fatalf("trigger updates = %+v", observer.updates)
	}
}

func TestSchedulerRefreshReportsConfigChanges(t *testing.T) {
	ctx := context.Background()
	oldScheduler := domain.Scheduler{Summary: domain.SchedulerSummary{ID: "scheduler-1", ProjectID: "project-1", AgentName: "worker", ProjectRevision: 1}}
	store := &schedulerUpdateStoreFake{items: map[string]domain.Scheduler{"scheduler-1": oldScheduler}}
	observer := &schedulerObserverFake{}
	controller := schedulers.NewController(schedulers.ControllerDependencies{Store: store, Observer: observer})
	controller.ReplaceCachedSchedulers(map[string]domain.Scheduler{"scheduler-1": oldScheduler})

	updated := oldScheduler
	updated.Summary.ProjectRevision = 2
	store.items["scheduler-1"] = updated
	if err := controller.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(observer.updates) != 1 || observer.updates[0].Reason != schedulers.SchedulerChangeConfig {
		t.Fatalf("config updates = %+v", observer.updates)
	}
}
