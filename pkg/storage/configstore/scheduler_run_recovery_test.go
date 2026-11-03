package configstore

import (
	"context"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestListInterruptedSchedulerRunsReturnsOlderRunningRuns(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	createPruneTestScheduler(t, store, "scheduler-a")
	startedAt := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	for _, run := range []domain.SchedulerRunSummary{
		{ID: "old-trigger", SchedulerID: "scheduler-a", TriggerID: "trigger-a", Status: domain.SchedulerRunStatusRunning, StartedAt: startedAt.Add(-time.Hour)},
		{ID: "fresh-trigger", SchedulerID: "scheduler-a", TriggerID: "trigger-a", Status: domain.SchedulerRunStatusRunning, StartedAt: startedAt},
		{ID: "old-terminal", SchedulerID: "scheduler-a", TriggerID: "trigger-a", Status: domain.SchedulerRunStatusSucceeded, StartedAt: startedAt.Add(-time.Hour), CompletedAt: startedAt.Add(-time.Minute)},
		{ID: "old-empty-trigger", SchedulerID: "scheduler-a", Status: domain.SchedulerRunStatusRunning, StartedAt: startedAt.Add(-time.Hour)},
	} {
		if err := store.CreateSchedulerRun(ctx, run); err != nil {
			t.Fatalf("create run %s: %v", run.ID, err)
		}
	}
	runs, err := store.ListInterruptedSchedulerRuns(ctx, startedAt)
	if err != nil {
		t.Fatalf("list interrupted runs: %v", err)
	}
	if len(runs) != 2 || runs[0].ID != "old-empty-trigger" || runs[1].ID != "old-trigger" {
		t.Fatalf("interrupted runs=%#v, want old-empty-trigger and old-trigger", runs)
	}
}
