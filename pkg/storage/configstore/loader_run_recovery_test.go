package configstore

import (
	"context"
	"testing"
	"time"

	domain "agent-compose/pkg/model"
)

func TestListInterruptedLoaderRunsOnlyReturnsOlderTriggerRuns(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	createPruneTestLoader(t, store, "loader-a")
	startedAt := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	for _, run := range []domain.LoaderRunSummary{
		{ID: "old-trigger", LoaderID: "loader-a", TriggerID: "trigger-a", Status: domain.LoaderRunStatusRunning, StartedAt: startedAt.Add(-time.Hour)},
		{ID: "fresh-trigger", LoaderID: "loader-a", TriggerID: "trigger-a", Status: domain.LoaderRunStatusRunning, StartedAt: startedAt},
		{ID: "old-terminal", LoaderID: "loader-a", TriggerID: "trigger-a", Status: domain.LoaderRunStatusSucceeded, StartedAt: startedAt.Add(-time.Hour), CompletedAt: startedAt.Add(-time.Minute)},
		{ID: "old-empty-trigger", LoaderID: "loader-a", Status: domain.LoaderRunStatusRunning, StartedAt: startedAt.Add(-time.Hour)},
	} {
		if err := store.CreateLoaderRun(ctx, run); err != nil {
			t.Fatalf("create run %s: %v", run.ID, err)
		}
	}
	runs, err := store.ListInterruptedLoaderRuns(ctx, startedAt)
	if err != nil {
		t.Fatalf("list interrupted runs: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "old-trigger" {
		t.Fatalf("interrupted runs=%#v", runs)
	}
}
