package configstore

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// TestPostgresRunTransactionPathsIntegration exercises the ent-over-transaction
// run write paths on PostgreSQL. They are written against the shared *sql.Tx
// (the run-event append still needs SQL for sequence allocation), so the
// backend-specific placeholder and conflict behaviour needs its own coverage.
func TestPostgresRunTransactionPathsIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store := openPostgresStore(t, ctx)

	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "p-pg-tx", Name: "pg-tx"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentID := createRunEventTestAgent(t, runEventTestAgentSpec{Ctx: ctx, Store: store, ProjectID: "p-pg-tx", AgentName: "worker"})
	run := domain.ProjectRunRecord{
		RunID: "r-pg-tx", ProjectID: "p-pg-tx", AgentName: "worker", AgentID: agentID,
		Status: domain.ProjectRunStatusRunning, Prompt: "hi", Labels: map[string]string{"env": "prod"},
	}
	created, err := store.CreateProjectRunWithEvents(ctx, run, []domain.ProjectRunEventRecord{{
		ID: "e-pg-tx-1", RunID: run.RunID, Kind: domain.ProjectRunEventKindUserMessage, Text: "hi",
	}})
	if err != nil {
		t.Fatalf("create run with events: %v", err)
	}
	if created.Labels["env"] != "prod" {
		t.Fatalf("labels = %v", created.Labels)
	}
	// A duplicate create keeps the stored row and appends the new event batch.
	duplicate, err := store.CreateProjectRunWithEvents(ctx, run, []domain.ProjectRunEventRecord{{
		ID: "e-pg-tx-2", RunID: run.RunID, Kind: domain.ProjectRunEventKindStatus, Text: "again",
	}})
	if err != nil {
		t.Fatalf("duplicate create: %v", err)
	}
	if duplicate.RunID != created.RunID || !duplicate.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("duplicate create changed the run: first=%+v second=%+v", created, duplicate)
	}
	updated, err := store.UpdateProjectRunWithEvents(ctx, duplicate, []domain.ProjectRunEventRecord{{
		ID: "e-pg-tx-3", RunID: run.RunID, Kind: domain.ProjectRunEventKindStatus, Text: "running",
	}})
	if err != nil || updated.Status != domain.ProjectRunStatusRunning {
		t.Fatalf("update run with events: %+v err=%v", updated, err)
	}
	completion := domain.ProjectRunCompletionRecord{
		RunID: run.RunID, TargetStatus: domain.ProjectRunStatusSucceeded,
		TransitionJSON: `{"status":"succeeded"}`, CleanupAction: "stop",
	}
	staged, isNew, err := store.StageProjectRunCompletion(ctx, completion, []domain.ProjectRunEventRecord{{
		ID: "e-pg-tx-4", RunID: run.RunID, Kind: domain.ProjectRunEventKindStatus, Text: "staging",
	}})
	if err != nil || !isNew || staged.RunID != run.RunID {
		t.Fatalf("stage completion: %+v new=%v err=%v", staged, isNew, err)
	}
	final := updated
	final.Status = domain.ProjectRunStatusSucceeded
	final.CompletedAt = time.Now().UTC()
	final, err = store.FinalizeProjectRunCompletion(ctx, final, domain.ProjectRunEventRecord{
		ID: "e-pg-tx-5", RunID: run.RunID, Kind: domain.ProjectRunEventKindStatus, Text: "terminal",
	})
	if err != nil || final.Status != domain.ProjectRunStatusSucceeded {
		t.Fatalf("finalize completion: %+v err=%v", final, err)
	}
	if _, err := store.GetProjectRunCompletion(ctx, run.RunID); err == nil {
		t.Fatal("completion row survived finalize")
	}
	events, err := store.ListProjectRunEvents(ctx, run.RunID, 0, 20)
	if err != nil {
		t.Fatalf("list run events: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("run events = %d, want 5", len(events))
	}
	seen := make(map[uint64]struct{}, len(events))
	for _, event := range events {
		if _, ok := seen[event.Sequence]; ok {
			t.Fatalf("duplicate run event sequence %d", event.Sequence)
		}
		seen[event.Sequence] = struct{}{}
	}
}

// TestPostgresConcurrentRunEventAppendIntegration checks that the
// SELECT ... FOR UPDATE + INSERT ... SELECT sequence allocator keeps producing
// unique sequences across connections on PostgreSQL.
func TestPostgresConcurrentRunEventAppendIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store := openPostgresStore(t, ctx)

	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "p-pg-append", Name: "pg-append"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentID := createRunEventTestAgent(t, runEventTestAgentSpec{Ctx: ctx, Store: store, ProjectID: "p-pg-append", AgentName: "worker"})
	if _, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID: "r-pg-append", ProjectID: "p-pg-append", AgentName: "worker", AgentID: agentID,
		Status: domain.ProjectRunStatusRunning,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	const writers, perWriter = 4, 6
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for writer := 0; writer < writers; writer++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			for index := 0; index < perWriter; index++ {
				id := fmt.Sprintf("e-pg-append-%d-%d", writer, index)
				if _, _, err := store.AppendProjectRunEvents(ctx, []domain.ProjectRunEventRecord{{
					ID: id, RunID: "r-pg-append", Kind: domain.ProjectRunEventKindAgentMessage, Text: id,
				}}); err != nil {
					errs <- err
					return
				}
			}
		}(writer)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("append run event: %v", err)
	}
	events, err := store.ListProjectRunEvents(ctx, "r-pg-append", 0, 100)
	if err != nil {
		t.Fatalf("list run events: %v", err)
	}
	if len(events) != writers*perWriter {
		t.Fatalf("run events = %d, want %d", len(events), writers*perWriter)
	}
	seen := make(map[uint64]struct{}, len(events))
	for _, event := range events {
		if _, ok := seen[event.Sequence]; ok {
			t.Fatalf("duplicate run event sequence %d", event.Sequence)
		}
		seen[event.Sequence] = struct{}{}
	}
}
