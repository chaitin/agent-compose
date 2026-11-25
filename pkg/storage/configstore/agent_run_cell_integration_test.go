package configstore

import (
	"context"
	"path/filepath"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
)

func TestIntegrationAgentRunCellSurvivesDatabaseReopen(t *testing.T) {
	for _, withEvents := range []bool{false, true} {
		name := "single"
		if withEvents {
			name = "with_events"
		}
		t.Run(name, func(t *testing.T) { testAgentRunCellReopen(t, withEvents) })
	}
}

func testAgentRunCellReopen(t *testing.T, withEvents bool) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "data.db")
	db, err := storagesqlite.Open(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	initialDB := db
	t.Cleanup(func() { _ = initialDB.Close() })
	store := FromDB(db.DB())
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-cells", Name: "cells"})
	if err != nil {
		t.Fatal(err)
	}
	agentID := createRunEventTestAgent(t, runEventTestAgentSpec{Ctx: ctx, Store: store, ProjectID: project.ID, AgentName: "worker"})
	logsPath, err := runlog.RunPath(root, "run-cells")
	if err != nil {
		t.Fatal(err)
	}
	run := domain.ProjectRunRecord{
		RunID: "run-cells", ProjectID: project.ID, AgentName: "worker", AgentID: agentID,
		Status: domain.ProjectRunStatusRunning, AgentCellID: "cell-before",
		LogFormat: runlog.FormatJSONL, LogsPath: logsPath,
		ExecutionID: "exec-before", DetachKind: domain.DetachKindAgent, Detach: true,
		SummaryJSON: `{"turns":1}`,
	}
	if withEvents {
		run, err = store.CreateProjectRunWithEvents(ctx, run, []domain.ProjectRunEventRecord{{ID: "prompt", RunID: run.RunID, Kind: domain.ProjectRunEventKindUserMessage, Text: "go", LogSeq: 1}})
	} else {
		run, err = store.CreateProjectRun(ctx, run)
	}
	if err != nil {
		t.Fatal(err)
	}
	if run.AgentCellID != "cell-before" || run.LogFormat != runlog.FormatJSONL || run.ExecutionID != "exec-before" || !run.Detach || run.SummaryJSON != `{"turns":1}` {
		t.Fatalf("created run=%+v", run)
	}
	run.AgentCellID = "cell-live"
	run.ExecutionID, run.DetachKind, run.Detach = "exec-live", domain.DetachKindAgent, true
	run.SummaryJSON = `{"turns":2}`
	if withEvents {
		_, err = store.UpdateProjectRunWithEvents(ctx, run, []domain.ProjectRunEventRecord{{ID: "answer", RunID: run.RunID, Kind: domain.ProjectRunEventKindAgentMessage, Text: "done", LogSeq: 4}})
	} else {
		_, err = store.UpdateProjectRun(ctx, run)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storagesqlite.Open(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store = FromDB(db.DB())
	recovered, err := store.GetProjectRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.AgentCellID != "cell-live" || recovered.ExecutionID != "exec-live" || recovered.LogsPath != run.LogsPath || !recovered.Detach || recovered.LogFormat != runlog.FormatJSONL || recovered.SummaryJSON != run.SummaryJSON {
		t.Fatalf("recovered run=%+v", recovered)
	}
	if withEvents {
		events, err := store.ListProjectRunEvents(ctx, run.RunID, 0, 10)
		if err != nil || len(events) != 2 || events[0].LogSeq != 1 || events[1].LogSeq != 4 {
			t.Fatalf("events=%+v err=%v", events, err)
		}
	}
	page, err := store.ListProjectRunsByOptions(ctx, domain.ProjectRunListOptions{ProjectID: project.ID})
	if err != nil || len(page.Runs) != 1 || page.Runs[0].AgentCellID != "cell-live" {
		t.Fatalf("listed runs=%+v err=%v", page, err)
	}
}
