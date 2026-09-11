package configstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/internal/projects"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestProjectSchedulerPageUsesStableCursorAndProjectQuery(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	for _, project := range []domain.ProjectRecord{{ID: "project-a", Name: "alpha"}, {ID: "project-b", Name: "beta"}} {
		if _, err := store.UpsertProject(ctx, project); err != nil {
			t.Fatalf("upsert project %s: %v", project.ID, err)
		}
	}
	for _, scheduler := range []domain.ProjectSchedulerRecord{
		{ProjectID: "project-a", AgentName: "agent-a", ID: "scheduler-record-a", Enabled: true},
		{ProjectID: "project-a", AgentName: "agent-b", ID: "scheduler-record-b", Enabled: true},
		{ProjectID: "project-b", AgentName: "agent-c", ID: "scheduler-record-c", Enabled: true},
	} {
		agentID, err := projects.StableProjectAgentID(scheduler.ProjectID, scheduler.AgentName)
		if err != nil {
			t.Fatalf("derive agent id %s: %v", scheduler.AgentName, err)
		}
		if _, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{ID: agentID, ProjectID: scheduler.ProjectID, AgentName: scheduler.AgentName}); err != nil {
			t.Fatalf("upsert agent %s: %v", scheduler.AgentName, err)
		}
		if _, err := store.UpsertProjectScheduler(ctx, scheduler); err != nil {
			t.Fatalf("upsert scheduler %s: %v", scheduler.SchedulerID, err)
		}
	}
	// The retained scheduler_id column is migration-only state. Deliberately
	// diverge it to prove current reads, writes, ordering, and cursors use id.
	if _, err := store.db.ExecContext(ctx, `UPDATE project_scheduler SET scheduler_id = ? WHERE id = ?`, "legacy-alias-a", "scheduler-record-a"); err != nil {
		t.Fatalf("diverge compatibility scheduler id: %v", err)
	}
	loaded, err := store.GetProjectScheduler(ctx, "project-a", "scheduler-record-a")
	if err != nil || loaded.ID != "scheduler-record-a" || loaded.SchedulerID != loaded.ID {
		t.Fatalf("get scheduler by native id = %#v, err = %v", loaded, err)
	}
	loadedByID, err := store.GetProjectSchedulerByID(ctx, "scheduler-record-a")
	if err != nil || loadedByID.ID != "scheduler-record-a" || loadedByID.ProjectID != "project-a" || loadedByID.SchedulerID != loadedByID.ID {
		t.Fatalf("get scheduler by global native id = %#v, err = %v", loadedByID, err)
	}
	if _, err := store.GetProjectScheduler(ctx, "project-a", "legacy-alias-a"); err == nil {
		t.Fatal("get scheduler unexpectedly used compatibility scheduler_id")
	}
	if _, err := store.GetProjectSchedulerByID(ctx, "legacy-alias-a"); err == nil {
		t.Fatal("global scheduler lookup unexpectedly used compatibility scheduler_id")
	}
	if loaded, err = store.SetProjectSchedulerEnabled(ctx, "project-a", "scheduler-record-a", false); err != nil || loaded.Enabled {
		t.Fatalf("disable scheduler by native id = %#v, err = %v", loaded, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE project_scheduler SET last_error = ? WHERE id = ?`, "last failure", "scheduler-record-a"); err != nil {
		t.Fatalf("update scheduler error: %v", err)
	}
	for index, startedAt := range []int64{1700000000, 1700000060} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO scheduler_run(scheduler_id, run_id, trigger_id, started_at) VALUES(?, ?, ?, ?)`, "scheduler-record-a", fmt.Sprintf("run-%d", index), "trigger-1", startedAt); err != nil {
			t.Fatalf("insert scheduler run: %v", err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO scheduler_run(scheduler_id, run_id, started_at) VALUES(?, ?, ?)`, "scheduler-record-a", "old-invocation", int64(1700000120)); err != nil {
		t.Fatalf("insert old invocation: %v", err)
	}

	first, err := store.ListProjectSchedulersPage(ctx, "", 0, 2)
	if err != nil || len(first) != 2 {
		t.Fatalf("first page = %#v, err = %v", first, err)
	}
	if first[0].RunCount != 2 || first[0].LastError != "last failure" || !first[0].LatestRunAt.Equal(time.Unix(1700000060, 0).UTC()) {
		t.Fatalf("scheduler summary = %#v", first[0])
	}
	second, err := store.ListProjectSchedulersPage(ctx, "", 2, 2)
	if err != nil || len(second) != 1 || second[0].ProjectID != "project-b" {
		t.Fatalf("second page = %#v, err = %v", second, err)
	}
	filtered, err := store.ListProjectSchedulersPage(ctx, "alpha", 0, 10)
	if err != nil || len(filtered) != 2 {
		t.Fatalf("filtered page = %#v, err = %v", filtered, err)
	}
}

func TestProjectSchedulerPageScalesWithRunHistory(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	const (
		projectID      = "project-scale"
		schedulerCount = 120
	)
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: projectID, Name: "scale"}); err != nil {
		t.Fatalf("upsert project: %v", err)
	}
	for index := range schedulerCount {
		agentName := fmt.Sprintf("agent-%03d", index)
		schedulerID := fmt.Sprintf("scheduler-%03d", index)
		agentID, err := projects.StableProjectAgentID(projectID, agentName)
		if err != nil {
			t.Fatalf("derive agent id %s: %v", agentName, err)
		}
		if _, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{ID: agentID, ProjectID: projectID, AgentName: agentName}); err != nil {
			t.Fatalf("upsert agent %s: %v", agentName, err)
		}
		if _, err := store.UpsertProjectScheduler(ctx, domain.ProjectSchedulerRecord{
			ProjectID: projectID,
			AgentName: agentName,
			ID:        schedulerID,
			Enabled:   index%2 == 0,
		}); err != nil {
			t.Fatalf("upsert scheduler %s: %v", schedulerID, err)
		}
		for runIndex := range 3 {
			startedAt := int64(1700000000 + index*10 + runIndex)
			if _, err := store.db.ExecContext(ctx, `INSERT INTO scheduler_run(scheduler_id, run_id, trigger_id, started_at) VALUES(?, ?, ?, ?)`, schedulerID, fmt.Sprintf("run-%03d-%d", index, runIndex), "trigger-1", startedAt); err != nil {
				t.Fatalf("insert scheduler run %s/%d: %v", schedulerID, runIndex, err)
			}
		}
		if _, err := store.db.ExecContext(ctx, `INSERT INTO scheduler_run(scheduler_id, run_id, started_at) VALUES(?, ?, ?)`, schedulerID, fmt.Sprintf("legacy-%03d", index), int64(1800000000+index)); err != nil {
			t.Fatalf("insert legacy scheduler invocation %s: %v", schedulerID, err)
		}
	}

	first, err := store.ListProjectSchedulersPage(ctx, "scale", 0, 100)
	if err != nil || len(first) != 100 {
		t.Fatalf("first page length = %d, err = %v", len(first), err)
	}
	second, err := store.ListProjectSchedulersPage(ctx, "scale", 100, 100)
	if err != nil || len(second) != 20 {
		t.Fatalf("second page length = %d, err = %v", len(second), err)
	}
	for index, scheduler := range append(first, second...) {
		wantID := fmt.Sprintf("scheduler-%03d", index)
		wantLatest := time.Unix(int64(1700000000+index*10+2), 0).UTC()
		if scheduler.ID != wantID || scheduler.Enabled != (index%2 == 0) || scheduler.RunCount != 3 || !scheduler.LatestRunAt.Equal(wantLatest) {
			t.Fatalf("scheduler %d = %#v, want id=%s enabled=%v run_count=3 latest=%s", index, scheduler, wantID, index%2 == 0, wantLatest)
		}
	}
}
