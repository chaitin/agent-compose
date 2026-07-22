package configstore

import (
	"context"
	"testing"
	"time"

	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
)

func TestLoaderRunPruneFiltersAndDeletesDirectRunData(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	createPruneTestLoader(t, store, "loader-a")
	createPruneTestLoader(t, store, "loader-b")
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	newer := now.Add(-time.Hour)
	for _, run := range []domain.LoaderRunSummary{
		{ID: "run-old-success", LoaderID: "loader-a", TriggerID: "trigger-a", Status: domain.LoaderRunStatusSucceeded, StartedAt: old, CompletedAt: old.Add(time.Minute)},
		{ID: "run-new-failed", LoaderID: "loader-a", TriggerID: "trigger-a", Status: domain.LoaderRunStatusFailed, StartedAt: newer, CompletedAt: newer.Add(time.Minute)},
		{ID: "run-old-other-trigger", LoaderID: "loader-a", TriggerID: "trigger-b", Status: domain.LoaderRunStatusFailed, StartedAt: old.Add(time.Minute), CompletedAt: old.Add(2 * time.Minute)},
		{ID: "run-running", LoaderID: "loader-a", TriggerID: "trigger-a", Status: domain.LoaderRunStatusRunning, StartedAt: old},
		{ID: "run-invocation", LoaderID: "loader-a", Status: domain.LoaderRunStatusSucceeded, StartedAt: old, CompletedAt: old.Add(time.Minute)},
		{ID: "run-other-loader", LoaderID: "loader-b", TriggerID: "trigger-a", Status: domain.LoaderRunStatusSucceeded, StartedAt: old, CompletedAt: old.Add(time.Minute)},
	} {
		if err := store.CreateLoaderRun(ctx, run); err != nil {
			t.Fatalf("create run %s: %v", run.ID, err)
		}
	}
	addPruneTestRelations(t, store, "loader-a", "run-old-success", "trigger-a")
	if _, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID: "topic-event", Topic: "topic.test", Source: domain.TopicEventSourceSystem,
		CorrelationID: "correlation-test", PayloadJSON: `{}`, CreatedAt: old,
	}); err != nil {
		t.Fatalf("create topic event: %v", err)
	}

	filtered, err := store.ListLoaderRunsForPrune(ctx, loaders.SchedulerRunPruneFilter{
		LoaderIDs: []string{"loader-a"}, TriggerID: "trigger-a",
		Statuses:  []string{domain.LoaderRunStatusSucceeded, domain.LoaderRunStatusFailed},
		OlderThan: 24 * time.Hour, Now: now,
	})
	if err != nil || len(filtered) != 1 || filtered[0].ID != "run-old-success" {
		t.Fatalf("filtered runs=%#v err=%v", filtered, err)
	}
	keys := []loaders.LoaderRunKey{{LoaderID: "loader-a", RunID: "run-old-success"}}
	counted, err := store.CountLoaderRunPruneData(ctx, keys)
	if err != nil {
		t.Fatalf("count prune data: %v", err)
	}
	if counted.Runs != 1 || counted.LoaderEvents != 1 || counted.EventDeliveries != 1 || counted.EventSandboxLinks != 1 {
		t.Fatalf("counted prune data = %#v", counted)
	}
	removed, err := store.DeleteLoaderRunPruneData(ctx, keys)
	if err != nil {
		t.Fatalf("delete prune data: %v", err)
	}
	if removed.Stats != counted || len(removed.RemovedKeys) != 1 || removed.RemovedKeys[0] != keys[0] {
		t.Fatalf("removed=%#v, want stats %#v and key %#v", removed, counted, keys[0])
	}
	if remaining, err := store.ListLoaderRunsForPrune(ctx, loaders.SchedulerRunPruneFilter{LoaderIDs: []string{"loader-a", "loader-b"}}); err != nil || len(remaining) != 4 {
		t.Fatalf("remaining trigger runs=%#v err=%v", remaining, err)
	}
	for table, where := range map[string]string{
		"loader_event":       "loader_id = 'loader-a' AND run_id = 'run-old-success'",
		"event_delivery":     "loader_id = 'loader-a' AND run_id = 'run-old-success'",
		"event_sandbox_link": "loader_id = 'loader-a' AND run_id = 'run-old-success'",
	} {
		assertPruneTestRowCount(t, store, table, where, 0)
	}
	assertPruneTestRowCount(t, store, "event", "id = 'topic-event'", 1)
	assertPruneTestRowCount(t, store, "loader_run", "run_id = 'run-invocation'", 1)
	assertPruneTestRowCount(t, store, "loader_run", "run_id = 'run-running'", 1)
}

func TestDeleteLoaderRunPruneDataRollsBackWholeTransaction(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	createPruneTestLoader(t, store, "loader-a")
	now := time.Now().UTC()
	for _, runID := range []string{"run-ok", "run-fail"} {
		if err := store.CreateLoaderRun(ctx, domain.LoaderRunSummary{ID: runID, LoaderID: "loader-a", TriggerID: "trigger-a", Status: domain.LoaderRunStatusSucceeded, StartedAt: now, CompletedAt: now}); err != nil {
			t.Fatalf("create run %s: %v", runID, err)
		}
		addPruneTestRelations(t, store, "loader-a", runID, "trigger-a")
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER block_prune_event BEFORE DELETE ON loader_event
		WHEN OLD.run_id = 'run-fail' BEGIN SELECT RAISE(ABORT, 'blocked prune'); END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	keys := []loaders.LoaderRunKey{{LoaderID: "loader-a", RunID: "run-ok"}, {LoaderID: "loader-a", RunID: "run-fail"}}
	if _, err := store.DeleteLoaderRunPruneData(ctx, keys); err == nil {
		t.Fatal("DeleteLoaderRunPruneData returned nil error")
	}
	for _, runID := range []string{"run-ok", "run-fail"} {
		assertPruneTestRowCount(t, store, "loader_run", "run_id = '"+runID+"'", 1)
		assertPruneTestRowCount(t, store, "loader_event", "run_id = '"+runID+"'", 1)
		assertPruneTestRowCount(t, store, "event_delivery", "run_id = '"+runID+"'", 1)
		assertPruneTestRowCount(t, store, "event_sandbox_link", "run_id = '"+runID+"'", 1)
	}
}

func TestDeleteLoaderRunPruneDataRechecksTerminalTriggerRun(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	createPruneTestLoader(t, store, "loader-a")
	for _, run := range []domain.LoaderRunSummary{
		{ID: "run-running", LoaderID: "loader-a", TriggerID: "trigger-a", Status: domain.LoaderRunStatusRunning, StartedAt: time.Now().UTC()},
		{ID: "run-empty-trigger", LoaderID: "loader-a", Status: domain.LoaderRunStatusSucceeded, StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC()},
	} {
		if err := store.CreateLoaderRun(ctx, run); err != nil {
			t.Fatalf("create run %s: %v", run.ID, err)
		}
		addPruneTestRelations(t, store, "loader-a", run.ID, run.TriggerID)
	}
	keys := []loaders.LoaderRunKey{{LoaderID: "loader-a", RunID: "run-running"}, {LoaderID: "loader-a", RunID: "run-empty-trigger"}}
	if counted, err := store.CountLoaderRunPruneData(ctx, keys); err != nil || counted != (loaders.SchedulerRunPruneDatabaseStats{}) {
		t.Fatalf("counted=%#v err=%v", counted, err)
	}
	if removed, err := store.DeleteLoaderRunPruneData(ctx, keys); err != nil || removed.Stats != (loaders.SchedulerRunPruneDatabaseStats{}) || len(removed.RemovedKeys) != 0 {
		t.Fatalf("removed=%#v err=%v", removed, err)
	}
	for _, runID := range []string{"run-running", "run-empty-trigger"} {
		assertPruneTestRowCount(t, store, "loader_run", "run_id = '"+runID+"'", 1)
		assertPruneTestRowCount(t, store, "loader_event", "run_id = '"+runID+"'", 1)
		assertPruneTestRowCount(t, store, "event_delivery", "run_id = '"+runID+"'", 1)
		assertPruneTestRowCount(t, store, "event_sandbox_link", "run_id = '"+runID+"'", 1)
	}
}

func createPruneTestLoader(t *testing.T, store *ConfigStore, loaderID string) {
	t.Helper()
	if _, err := store.UpsertManagedLoader(context.Background(), domain.Loader{
		Summary: domain.LoaderSummary{
			ID: loaderID, Name: loaderID, Runtime: domain.LoaderRuntimeScheduler,
			ManagedProjectID: "project-1", ManagedAgentName: loaderID, ManagedSchedulerID: "scheduler-" + loaderID,
		},
		Script: "function main() {}",
	}); err != nil {
		t.Fatalf("create loader %s: %v", loaderID, err)
	}
}

func addPruneTestRelations(t *testing.T, store *ConfigStore, loaderID, runID, triggerID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.AddLoaderEvent(ctx, domain.LoaderEvent{ID: "loader-event-" + runID, LoaderID: loaderID, RunID: runID, TriggerID: triggerID, Type: "loader.run.completed", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("add loader event: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO event_delivery(event_id, loader_id, trigger_id, run_id, status, created_at, updated_at) VALUES(?, ?, ?, ?, 'run_succeeded', 1, 1)`, "event-"+runID, loaderID, triggerID, runID); err != nil {
		t.Fatalf("add event delivery: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO event_sandbox_link(event_id, sandbox_id, relation, loader_id, run_id, trigger_id, created_at) VALUES(?, ?, 'used', ?, ?, ?, 1)`, "event-"+runID, "sandbox-"+runID, loaderID, runID, triggerID); err != nil {
		t.Fatalf("add event sandbox link: %v", err)
	}
}

func assertPruneTestRowCount(t *testing.T, store *ConfigStore, table, where string, want int) {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table+" WHERE "+where).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if count != want {
		t.Fatalf("%s count=%d, want %d", table, count, want)
	}
}
