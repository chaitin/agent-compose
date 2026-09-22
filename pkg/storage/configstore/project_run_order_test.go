package configstore

import (
	"context"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// The run list's order is a contract, not an implementation detail. Clients
// read the newest run of a conversation by asking for the firstResult row — the Go
// chat SDK's Lookup and EndSession both do, and EndSession stopping a stale
// run would leave the live one, and its sandbox, behind. Pin the order here so
// a change to the query has to come past this test.
func TestListProjectRunsByOptionsReturnsNewestFirstAcrossPages(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-order", Name: "order"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentID := createRunEventTestAgent(t, runEventTestAgentSpec{Ctx: ctx, Store: store, ProjectID: "project-order", AgentName: "worker"})

	// Insert oldest firstResult, so a query that preserved insertion order would fail
	// this test rather than pass it by accident. CreateProjectRun stamps
	// created_at itself, so the ages are written afterwards; the column holds
	// Unix seconds.
	ages := map[string]int64{"run-oldest": 1_700_000_000, "run-middle": 1_700_000_060, "run-newest": 1_700_000_120}
	for _, runID := range []string{"run-oldest", "run-middle", "run-newest"} {
		if _, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
			RunID: runID, ProjectID: "project-order", AgentName: "worker", AgentID: agentID,
			Status: domain.ProjectRunStatusRunning,
		}); err != nil {
			t.Fatalf("create %s: %v", runID, err)
		}
		if _, err := store.db.ExecContext(ctx, `UPDATE project_run SET created_at = ? WHERE run_id = ?`, ages[runID], runID); err != nil {
			t.Fatalf("age %s: %v", runID, err)
		}
	}

	allResult, err := store.ListProjectRunsByOptions(ctx, domain.ProjectRunListOptions{ProjectID: "project-order"})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	want := []string{"run-newest", "run-middle", "run-oldest"}
	if len(allResult.Runs) != len(want) {
		t.Fatalf("listed %d runs, want %d", len(allResult.Runs), len(want))
	}
	for index, runID := range want {
		if allResult.Runs[index].RunID != runID {
			t.Fatalf("run %d = %s, want %s: the list must be newest firstResult", index, allResult.Runs[index].RunID, runID)
		}
	}

	// A client that wants only the newest run asks for one row, so the firstResult
	// pageResult of one has to be the newest and not merely some matching run.
	firstResult, err := store.ListProjectRunsByOptions(ctx, domain.ProjectRunListOptions{ProjectID: "project-order", Limit: 1})
	if err != nil {
		t.Fatalf("list newest run: %v", err)
	}
	if len(firstResult.Runs) != 1 || firstResult.Runs[0].RunID != "run-newest" {
		t.Fatalf("firstResult pageResult = %#v, want just run-newest", firstResult)
	}

	// Paging continues in the same order, so a walk sees every run once.
	pageResult, err := store.ListProjectRunsByOptions(ctx, domain.ProjectRunListOptions{ProjectID: "project-order", Offset: 1, Limit: 1})
	if err != nil {
		t.Fatalf("list second pageResult: %v", err)
	}
	if len(pageResult.Runs) != 1 || pageResult.Runs[0].RunID != "run-middle" {
		t.Fatalf("second pageResult = %#v, want just run-middle", pageResult)
	}
}
