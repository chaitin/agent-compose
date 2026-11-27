package runs

import (
	"context"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

type runObserverFake struct {
	before []domain.ProjectRunRecord
	after  []domain.ProjectRunRecord
}

func (o *runObserverFake) RunStatusChanged(_ context.Context, before, after domain.ProjectRunRecord) {
	o.before = append(o.before, before)
	o.after = append(o.after, after)
}

func TestCoordinatorReportsPersistedStatusChange(t *testing.T) {
	store := &fakeRunStore{runs: map[string]domain.ProjectRunRecord{
		"run-1": {RunID: "run-1", ProjectID: "p1", AgentName: "worker", Status: domain.ProjectRunStatusRunning},
	}}
	observer := &runObserverFake{}
	coordinator := NewCoordinator(store, nil).WithObserver(observer)
	updated, err := coordinator.TransitionRun(context.Background(), TransitionRequest{
		RunID: "run-1", Status: domain.ProjectRunStatusSucceeded, ExitCode: 0,
	})
	if err != nil {
		t.Fatalf("TransitionRun: %v", err)
	}
	if updated.Status != domain.ProjectRunStatusSucceeded {
		t.Fatalf("updated status = %q", updated.Status)
	}
	if len(observer.after) != 1 {
		t.Fatalf("observer calls = %d, want 1", len(observer.after))
	}
	if observer.before[0].Status != domain.ProjectRunStatusRunning || observer.after[0].Status != domain.ProjectRunStatusSucceeded {
		t.Fatalf("observed statuses = %q -> %q", observer.before[0].Status, observer.after[0].Status)
	}
}

func TestRunLogHubNotifiesGlobalObserver(t *testing.T) {
	hub := NewRunLogHub()
	var observed []RunLogEvent
	hub.SetObserver(func(event RunLogEvent) { observed = append(observed, event) })
	hub.Publish(RunLogEvent{RunID: "run-1", ProjectID: "p1", SandboxID: "s1", Stream: "stdout", Data: "hello", Offset: 5})
	if len(observed) != 1 || observed[0].ProjectID != "p1" || observed[0].SandboxID != "s1" || observed[0].Stream != "stdout" {
		t.Fatalf("observed = %+v", observed)
	}
}
