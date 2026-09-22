package configstore

import (
	"context"
	"testing"
	"time"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestListProjectRunsByOptionsFiltersInclusiveStartRange(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-1", Name: "project", SourcePath: "/project", SourceJSON: `{}`})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	revision, _, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{
		ProjectID: project.ID, SpecHash: "hash", SpecJSON: `{"agents":[]}`,
	})
	if err != nil {
		t.Fatalf("create project revision: %v", err)
	}
	agent, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{
		ProjectID: project.ID, AgentName: "worker", ID: "agent-1", Revision: revision.Revision,
		Provider: "codex", Model: "gpt", Image: "guest:latest", Driver: driverpkg.RuntimeDriverDocker,
		SpecJSON: `{"name":"worker"}`,
	})
	if err != nil {
		t.Fatalf("create project agent: %v", err)
	}

	base := time.Date(2026, time.July, 28, 10, 0, 0, 123000000, time.UTC)
	starts := map[string]time.Time{
		"before": base.Add(-time.Millisecond),
		"from":   base,
		"inside": base.Add(time.Hour),
		"to":     base.Add(2 * time.Hour),
		"after":  base.Add(2*time.Hour + time.Millisecond),
	}
	for runID, startedAt := range starts {
		schedulerRunID := ""
		if runID == "from" {
			schedulerRunID = "scheduler-run-1"
		}
		_, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
			RunID: runID, ProjectID: project.ID, ProjectName: project.Name, ProjectRevision: revision.Revision,
			AgentName: agent.AgentName, AgentID: agent.ID, SchedulerRunID: schedulerRunID, Source: domain.ProjectRunSourceAPI,
			Status: domain.ProjectRunStatusPending, ResultJSON: `{}`, StartedAt: startedAt,
		})
		if err != nil {
			t.Fatalf("create run %s: %v", runID, err)
		}
	}

	tests := []struct {
		name string
		from *time.Time
		to   *time.Time
		want map[string]bool
	}{
		{name: "from only", from: timePointer(base), want: runIDs("from", "inside", "to", "after")},
		{name: "to only", to: timePointer(base.Add(2 * time.Hour)), want: runIDs("before", "from", "inside", "to")},
		{name: "closed interval", from: timePointer(base), to: timePointer(base.Add(2 * time.Hour)), want: runIDs("from", "inside", "to")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := domain.ProjectRunListOptions{StartedFrom: tt.from, StartedTo: tt.to, Limit: 50}
			gotResult, err := store.ListProjectRunsByOptions(ctx, options)
			if err != nil {
				t.Fatalf("list runs: %v", err)
			}
			if len(gotResult.Runs) != len(tt.want) {
				t.Fatalf("run count = %d, want %d: %#v", len(gotResult.Runs), len(tt.want), gotResult.Runs)
			}
			for _, run := range gotResult.Runs {
				if !tt.want[run.RunID] {
					t.Errorf("unexpected run %q", run.RunID)
				}
			}
			total, _, err := store.CountProjectRuns(ctx, options)
			if err != nil || total != len(tt.want) {
				t.Fatalf("count = %d, want %d (err=%v)", total, len(tt.want), err)
			}
		})
	}

	filteredOptions := domain.ProjectRunListOptions{SchedulerRunID: " scheduler-run-1 ", Limit: 50}
	filteredRunsResult, err := store.ListProjectRunsByOptions(ctx, filteredOptions)
	if err != nil {
		t.Fatalf("list runs by scheduler run: %v", err)
	}
	if len(filteredRunsResult.Runs) != 1 || filteredRunsResult.Runs[0].RunID != "from" {
		t.Fatalf("scheduler run filter = %#v, want run from", filteredRunsResult)
	}
	filteredTotal, _, err := store.CountProjectRuns(ctx, filteredOptions)
	if err != nil {
		t.Fatalf("count runs by scheduler run: %v", err)
	}
	if filteredTotal != len(filteredRunsResult.Runs) {
		t.Fatalf("scheduler run count = %d, list length = %d", filteredTotal, len(filteredRunsResult.Runs))
	}
}

func timePointer(value time.Time) *time.Time { return &value }

func runIDs(ids ...string) map[string]bool {
	result := make(map[string]bool, len(ids))
	for _, id := range ids {
		result[id] = true
	}
	return result
}
