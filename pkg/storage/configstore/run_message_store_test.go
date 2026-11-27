package configstore

import (
	"context"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// The run handler finds the message store by type assertion; this pins that
// the real store provides it, which a handler test with a fake cannot.
var _ interface {
	ListProjectRunMessagesPage(context.Context, string, string, int, int) ([]domain.ProjectRunEventRecord, error)
	CountProjectRunMessages(context.Context, string, string) (int, error)
} = (*ConfigStore)(nil)

func TestProjectRunMessagesPageByRunAndBySandbox(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-messages", Name: "messages"})
	if err != nil {
		t.Fatal(err)
	}
	agentID := createRunEventTestAgent(t, runEventTestAgentSpec{Ctx: ctx, Store: store, ProjectID: project.ID, AgentName: "worker"})
	for _, runID := range []string{"run-a", "run-b"} {
		if _, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{RunID: runID, ProjectID: project.ID, AgentName: "worker", AgentID: agentID, SandboxID: "sandbox-shared", Status: domain.ProjectRunStatusRunning}); err != nil {
			t.Fatal(err)
		}
	}
	events := []domain.ProjectRunEventRecord{
		{ID: "a-user", RunID: "run-a", Kind: domain.ProjectRunEventKindUserMessage, Text: "first", LogSeq: 1},
		{ID: "a-agent", RunID: "run-a", Kind: domain.ProjectRunEventKindAgentMessage, Text: "reply", LogSeq: 4},
		{ID: "a-status", RunID: "run-a", Kind: domain.ProjectRunEventKindStatus},
		{ID: "b-user", RunID: "run-b", Kind: domain.ProjectRunEventKindUserMessage, Text: "again", LogSeq: 1},
	}
	for _, event := range events {
		if _, _, err := store.AppendProjectRunEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	byRun, err := store.ListProjectRunMessagesPage(ctx, "run-a", "", 0, 10)
	if err != nil || len(byRun) != 2 || byRun[0].ID != "a-user" || byRun[1].LogSeq != 4 {
		t.Fatalf("messages of run-a = %+v, %v", byRun, err)
	}
	if total, err := store.CountProjectRunMessages(ctx, "run-a", ""); err != nil || total != 2 {
		t.Fatalf("count of run-a = %d, %v; the status event is not a message", total, err)
	}
	bySandbox, err := store.ListProjectRunMessagesPage(ctx, "", "sandbox-shared", 1, 10)
	if err != nil || len(bySandbox) != 2 || bySandbox[1].RunID != "run-b" {
		t.Fatalf("sandbox page = %+v, %v", bySandbox, err)
	}
	if total, err := store.CountProjectRunMessages(ctx, "", "sandbox-shared"); err != nil || total != 3 {
		t.Fatalf("sandbox count = %d, %v", total, err)
	}
}
