package runs_test

import (
	"context"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
)

type completionObserverFake struct {
	before []domain.ProjectRunRecord
	after  []domain.ProjectRunRecord
}

func (o *completionObserverFake) RunStatusChanged(_ context.Context, before, after domain.ProjectRunRecord) {
	o.before = append(o.before, before)
	o.after = append(o.after, after)
}

func TestCompletionManagerReportsFinalizedRunStatus(t *testing.T) {
	ctx := context.Background()
	store := newCompletionTestStore(t)
	run := createCompletionTestRun(t, store, domain.ProjectRunRecord{
		RunID: "observed-run", ProjectID: "project-1", AgentName: "worker", AgentID: "agent-1",
		Status: domain.ProjectRunStatusRunning, CleanupPolicy: domain.ProjectRunCleanupKeepRunning,
	})
	observer := &completionObserverFake{}
	manager := runs.NewCompletionManager(runs.CompletionManagerDeps{
		Store:     store,
		Sandboxes: completionSandboxStoreFake{},
		Lifecycle: &completionStopperFake{},
		Observer:  observer,
	})
	if _, err := manager.Complete(ctx, runs.TransitionRequest{
		RunID: run.RunID, Status: domain.ProjectRunStatusSucceeded, Output: "done",
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(observer.after) != 1 {
		t.Fatalf("observer calls = %d, want 1", len(observer.after))
	}
	if observer.before[0].Status != domain.ProjectRunStatusRunning || observer.after[0].Status != domain.ProjectRunStatusSucceeded {
		t.Fatalf("observed statuses = %q -> %q", observer.before[0].Status, observer.after[0].Status)
	}
}
