package configstore

import (
	"context"
	"testing"
	"time"

	domain "agent-compose/pkg/model"
)

func TestListProjectAgentRunStatesAggregatesAllAgentRuns(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-run-states", Name: "run states"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	runs := []domain.ProjectRunRecord{
		{RunID: "a-old-manual", ProjectID: project.ID, AgentName: "agent-a", Status: domain.ProjectRunStatusRunning, Source: domain.ProjectRunSourceManual},
		{RunID: "a-scheduler", ProjectID: project.ID, AgentName: "agent-a", Status: domain.ProjectRunStatusRunning, Source: domain.ProjectRunSourceScheduler},
		{RunID: "a-latest", ProjectID: project.ID, AgentName: "agent-a", Status: domain.ProjectRunStatusFailed, Source: domain.ProjectRunSourceAPI},
		{RunID: "b-latest", ProjectID: project.ID, AgentName: "agent-b", Status: domain.ProjectRunStatusSucceeded, Source: domain.ProjectRunSourceManual},
	}
	for _, run := range runs {
		if _, err := store.CreateProjectRun(ctx, run); err != nil {
			t.Fatalf("create run %s: %v", run.RunID, err)
		}
	}
	updates := []struct {
		runID       string
		createdAt   int64
		startedAt   int64
		completedAt int64
	}{
		{runID: "a-old-manual", createdAt: 1_700_000_100, startedAt: 1_700_000_100_000},
		{runID: "a-scheduler", createdAt: 1_700_000_200, startedAt: 1_700_000_200_000},
		{runID: "a-latest", createdAt: 1_700_000_300, completedAt: 1_700_000_400_000},
		{runID: "b-latest", createdAt: 1_700_000_150},
	}
	for _, update := range updates {
		if _, err := store.db.ExecContext(ctx, `UPDATE project_run SET created_at = ?, started_at = ?, completed_at = ? WHERE run_id = ?`, update.createdAt, update.startedAt, update.completedAt, update.runID); err != nil {
			t.Fatalf("set run times for %s: %v", update.runID, err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `WITH RECURSIVE numbers(n) AS (
		SELECT 1 UNION ALL SELECT n + 1 FROM numbers WHERE n < 205
	)
	INSERT INTO project_run(run_id, project_id, agent_name, status, source, created_at, updated_at)
	SELECT printf('bulk-%03d', n), ?, 'agent-b', ?, ?, 1600000000 + n, 1600000000 + n FROM numbers`, project.ID, domain.ProjectRunStatusRunning, domain.ProjectRunSourceManual); err != nil {
		t.Fatalf("create runs beyond legacy page limit: %v", err)
	}

	states, err := store.ListProjectAgentRunStates(ctx, project.ID)
	if err != nil {
		t.Fatalf("list agent run states: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("states = %#v, want two agents", states)
	}
	agentA := states[0]
	if agentA.AgentName != "agent-a" || agentA.RunningRunCount != 1 || agentA.RunningSchedulerRunCount != 1 || agentA.LatestRunID != "a-latest" || agentA.LatestStatus != domain.ProjectRunStatusFailed || agentA.LatestSource != domain.ProjectRunSourceAPI || !agentA.LatestAt.Equal(time.Unix(1_700_000_400, 0).UTC()) {
		t.Fatalf("agent-a state = %#v", agentA)
	}
	agentB := states[1]
	if agentB.AgentName != "agent-b" || agentB.RunningRunCount != 205 || agentB.RunningSchedulerRunCount != 0 || agentB.LatestRunID != "b-latest" || !agentB.LatestAt.Equal(time.Unix(1_700_000_150, 0).UTC()) {
		t.Fatalf("agent-b state = %#v", agentB)
	}
}
