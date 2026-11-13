package configstore

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestRunInputIntegrationTransactionReplayAndEvidence(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "input-project", Name: "input"}); err != nil {
		t.Fatal(err)
	}
	agent := createRunEventTestAgent(t, runEventTestAgentSpec{Ctx: ctx, Store: store, ProjectID: "input-project", AgentName: "worker"})
	digest := sha256.Sum256([]byte("bytes"))
	input := &domain.RunInputPreparation{RunID: "input-run", State: "receiving", ManifestSHA256: digest[:], Files: []domain.RunInputFile{{ID: "file", Name: "a.txt", Size: 5, SHA256: digest[:]}}, Deadline: time.Now().UTC().Add(time.Minute)}
	run := domain.ProjectRunRecord{RunID: input.RunID, ProjectID: "input-project", AgentName: "worker", AgentID: agent, Prompt: "read files", Status: domain.ProjectRunStatusPending, InputPreparation: input}
	created, err := store.CreateProjectRunWithEvents(ctx, run, nil)
	if err != nil || !created.InputAccepted {
		t.Fatalf("create=%+v err=%v", created, err)
	}
	replay, err := store.CreateProjectRunWithEvents(ctx, run, nil)
	if err != nil || replay.InputAccepted {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	conflict := run
	conflict.Prompt = "different"
	if _, err := store.CreateProjectRunWithEvents(ctx, conflict, nil); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("text conflict=%v", err)
	}
	conflict = run
	conflict.InputPreparation = nil
	if _, err := store.CreateProjectRunWithEvents(ctx, conflict, nil); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("missing manifest conflict=%v", err)
	}
	ready := *input
	ready.State = "ready"
	ready.SandboxID = "sandbox"
	ready.Files = append([]domain.RunInputFile(nil), input.Files...)
	ready.Files[0].RelativePath = "a (1).txt"
	if err := store.SaveRunInput(ctx, *input, ready); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRunInput(ctx, *input, ready); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale evidence write=%v", err)
	}
	if _, err := store.UpdateProjectRun(ctx, created); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetProjectRun(ctx, run.RunID)
	if err != nil || loaded.InputPreparation.State != "ready" || loaded.InputPreparation.Files[0].RelativePath != "a (1).txt" {
		t.Fatalf("loaded=%+v err=%v", loaded.InputPreparation, err)
	}
	// Run input and Run creation must be atomic when an event fails validation.
	run.RunID = "rolled-back"
	otherInput := *input
	otherInput.RunID = run.RunID
	run.InputPreparation = &otherInput
	if _, err := store.CreateProjectRunWithEvents(ctx, run, []domain.ProjectRunEventRecord{{RunID: "wrong"}}); err == nil {
		t.Fatal("invalid event accepted")
	}
	if _, err := store.GetProjectRun(ctx, run.RunID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("rollback read=%v", err)
	}
}
