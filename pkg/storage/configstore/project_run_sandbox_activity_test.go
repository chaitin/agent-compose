package configstore

import (
	"context"
	"testing"

	"github.com/chaitin/agent-compose/internal/projects"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestUnfinishedProjectRunForSandboxIgnoresFinishedAndExcludedRuns(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-sandbox-activity", Name: "sandbox-activity"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentID, err := projects.StableProjectAgentID(project.ID, "worker")
	if err != nil {
		t.Fatalf("derive agent id: %v", err)
	}
	if _, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{ID: agentID, ProjectID: project.ID, AgentName: "worker"}); err != nil {
		t.Fatalf("create project agent: %v", err)
	}
	for _, run := range []domain.ProjectRunRecord{
		{RunID: "finished", SandboxID: "sandbox-1", Status: domain.ProjectRunStatusSucceeded},
		{RunID: "self", SandboxID: "sandbox-1", Status: domain.ProjectRunStatusRunning},
		{RunID: "elsewhere", SandboxID: "sandbox-2", Status: domain.ProjectRunStatusRunning},
	} {
		run.ProjectID, run.AgentName, run.AgentID = project.ID, "worker", agentID
		if _, err := store.CreateProjectRun(ctx, run); err != nil {
			t.Fatalf("create run %s: %v", run.RunID, err)
		}
	}
	if runID, found, err := store.UnfinishedProjectRunForSandbox(ctx, "sandbox-1", "self"); err != nil || found {
		t.Fatalf("unfinished run = %q found=%v err=%v, want none", runID, found, err)
	}

	pending := domain.ProjectRunRecord{RunID: "pending", ProjectID: project.ID, AgentName: "worker", AgentID: agentID, SandboxID: "sandbox-1", Status: domain.ProjectRunStatusPending}
	if _, err := store.CreateProjectRun(ctx, pending); err != nil {
		t.Fatalf("create pending run: %v", err)
	}
	if runID, found, err := store.UnfinishedProjectRunForSandbox(ctx, "sandbox-1", "self"); err != nil || !found || runID != "pending" {
		t.Fatalf("unfinished run = %q found=%v err=%v, want pending", runID, found, err)
	}
}
