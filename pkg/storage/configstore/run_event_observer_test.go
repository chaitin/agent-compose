package configstore

import (
	"context"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestRunEventObserverSeesCommittedEvents(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "observer-project", Name: "observer"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentID := createRunEventTestAgent(t, runEventTestAgentSpec{Ctx: ctx, Store: store, ProjectID: project.ID, AgentName: "worker"})

	type observed struct {
		run    domain.ProjectRunRecord
		events []domain.ProjectRunEventRecord
	}
	var got []observed
	store.SetRunEventObserver(func(_ context.Context, run domain.ProjectRunRecord, events []domain.ProjectRunEventRecord) {
		got = append(got, observed{run: run, events: events})
	})

	created, err := store.CreateProjectRunWithEvents(ctx, domain.ProjectRunRecord{
		RunID: "observer-run", ProjectID: project.ID, AgentName: "worker", AgentID: agentID,
		Status: domain.ProjectRunStatusPending,
	}, []domain.ProjectRunEventRecord{{
		ID: "observer-event-initial", RunID: "observer-run", Kind: domain.ProjectRunEventKindUserMessage, Text: "hello",
	}})
	if err != nil {
		t.Fatalf("CreateProjectRunWithEvents: %v", err)
	}
	if len(got) != 1 || got[0].run.ProjectID != project.ID || len(got[0].events) != 1 ||
		got[0].events[0].Sequence == 0 || got[0].events[0].Text != "hello" {
		t.Fatalf("observer after create = %+v", got)
	}

	appended, createdFlag, err := store.AppendProjectRunEvent(ctx, domain.ProjectRunEventRecord{
		ID: "observer-event-agent", RunID: created.RunID, Kind: domain.ProjectRunEventKindAgentMessage, Text: "done",
	})
	if err != nil || !createdFlag {
		t.Fatalf("AppendProjectRunEvent = %+v, %v, %v", appended, createdFlag, err)
	}
	if len(got) != 2 || got[1].events[0].Sequence != 2 {
		t.Fatalf("observer after append = %+v", got)
	}

	if _, err := store.UpdateProjectRunWithEvents(ctx, domain.ProjectRunRecord{
		RunID: created.RunID, ProjectID: project.ID, AgentName: "worker", AgentID: agentID, Status: domain.ProjectRunStatusSucceeded,
	}, []domain.ProjectRunEventRecord{{
		ID: "observer-event-terminal", RunID: created.RunID, Kind: domain.ProjectRunEventKindStatus, Success: true,
	}}); err != nil {
		t.Fatalf("UpdateProjectRunWithEvents: %v", err)
	}
	if len(got) != 3 || got[2].run.Status != domain.ProjectRunStatusSucceeded || got[2].events[0].Sequence != 3 {
		t.Fatalf("observer after update = %+v", got)
	}
}
