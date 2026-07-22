package configstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	domain "agent-compose/pkg/model"
)

func TestProjectSchedulerPageUsesStableCursorAndProjectQuery(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	for _, project := range []domain.ProjectRecord{{ID: "project-a", Name: "Alpha"}, {ID: "project-b", Name: "Beta"}} {
		if _, err := store.UpsertProject(ctx, project); err != nil {
			t.Fatalf("upsert project %s: %v", project.ID, err)
		}
	}
	for _, scheduler := range []domain.ProjectSchedulerRecord{
		{ProjectID: "project-a", AgentName: "agent-a", SchedulerID: "scheduler-a", ManagedLoaderID: "loader-a", Enabled: true},
		{ProjectID: "project-a", AgentName: "agent-b", SchedulerID: "scheduler-b", Enabled: true},
		{ProjectID: "project-b", AgentName: "agent-c", SchedulerID: "scheduler-c", Enabled: true},
	} {
		if _, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{ProjectID: scheduler.ProjectID, AgentName: scheduler.AgentName}); err != nil {
			t.Fatalf("upsert agent %s: %v", scheduler.AgentName, err)
		}
		if _, err := store.UpsertProjectScheduler(ctx, scheduler); err != nil {
			t.Fatalf("upsert scheduler %s: %v", scheduler.SchedulerID, err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO loader(id, name, script, last_error) VALUES(?, ?, ?, ?)`, "loader-a", "scheduler-a", "return {}", "last failure"); err != nil {
		t.Fatalf("insert loader: %v", err)
	}
	for index, startedAt := range []int64{1700000000, 1700000060} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO loader_run(loader_id, run_id, trigger_id, started_at) VALUES(?, ?, ?, ?)`, "loader-a", fmt.Sprintf("run-%d", index), "trigger-1", startedAt); err != nil {
			t.Fatalf("insert loader run: %v", err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO loader_run(loader_id, run_id, started_at) VALUES(?, ?, ?)`, "loader-a", "old-invocation", int64(1700000120)); err != nil {
		t.Fatalf("insert old invocation: %v", err)
	}

	first, err := store.ListProjectSchedulersPage(ctx, "", "", 2)
	if err != nil || len(first) != 2 {
		t.Fatalf("first page = %#v, err = %v", first, err)
	}
	if first[0].RunCount != 2 || first[0].LastError != "last failure" || !first[0].LatestRunAt.Equal(time.Unix(1700000060, 0).UTC()) {
		t.Fatalf("scheduler summary = %#v", first[0])
	}
	afterKey := first[1].ProjectID + "\x00" + first[1].AgentName + "\x00" + first[1].SchedulerID
	second, err := store.ListProjectSchedulersPage(ctx, "", afterKey, 2)
	if err != nil || len(second) != 1 || second[0].ProjectID != "project-b" {
		t.Fatalf("second page = %#v, err = %v", second, err)
	}
	filtered, err := store.ListProjectSchedulersPage(ctx, "alpha", "", 10)
	if err != nil || len(filtered) != 2 {
		t.Fatalf("filtered page = %#v, err = %v", filtered, err)
	}
}
