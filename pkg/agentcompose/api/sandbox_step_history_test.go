package api

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

type fakeSandboxSteps []domain.SchedulerEvent

func (f fakeSandboxSteps) ListSandboxSchedulerSteps(context.Context, string) ([]domain.SchedulerEvent, error) {
	return f, nil
}

func TestListSandboxHistoryRebuildsSchedulerSteps(t *testing.T) {
	const sandboxID = "4b8e4e7c-1b3f-4b8e-9d55-1c2f0a6f2a10"
	root := t.TempDir()
	at := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	path, err := runlog.SchedulerCellPath(root, "scheduler-1", "run-1", "step-done")
	if err != nil {
		t.Fatal(err)
	}
	w, err := runlog.Open(path, runlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AppendOutput("stdout", "polled 3 items\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	steps := fakeSandboxSteps{
		{SchedulerID: "scheduler-1", RunID: "run-1", Type: "scheduler.command.started", LinkedCellID: "step-done", PayloadJSON: `{"source":"poll.sh"}`, CreatedAt: at},
		{SchedulerID: "scheduler-1", RunID: "run-1", Type: "scheduler.command.completed", LinkedCellID: "step-done", PayloadJSON: `{"exitCode":0,"success":true}`, CreatedAt: at.Add(time.Second)},
		{SchedulerID: "scheduler-1", RunID: "run-2", Type: "scheduler.command.started", LinkedCellID: "step-running", PayloadJSON: `{"source":"poll.sh"}`, CreatedAt: at.Add(time.Minute)},
		// A step from before step logs kept its notebook cell.
		{SchedulerID: "scheduler-1", RunID: "run-0", Type: "scheduler.command.completed", LinkedCellID: "cell-stored", CreatedAt: at.Add(-time.Hour)},
	}
	store := &characterizationSandboxStore{
		session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID}},
		cells:   []domain.NotebookCell{{ID: "cell-stored", Type: "shell", Output: "notebook output", CreatedAt: at.Add(-time.Hour)}},
	}
	handler := NewSandboxHandler(SandboxHandlerDeps{Delegate: &characterizationSessionDelegate{}, Store: store, Remover: &characterizationSandboxRemover{}}).WithStepHistory(root, steps)

	response, err := handler.ListSandboxHistory(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxHistoryRequest{SandboxId: sandboxID}))
	if err != nil {
		t.Fatal(err)
	}
	cells := map[string]*agentcomposev2.SandboxHistoryCell{}
	for _, cell := range response.Msg.GetCells() {
		cells[cell.GetId()] = cell
	}
	if len(cells) != 3 {
		t.Fatalf("cells = %v", response.Msg.GetCells())
	}
	done := cells["step-done"]
	if done.GetType() != "shell" || done.GetSource() != "poll.sh" || !done.GetSuccess() || done.GetRunning() || done.GetOutput() != "polled 3 items\n" {
		t.Fatalf("finished step = %#v", done)
	}
	if running := cells["step-running"]; !running.GetRunning() || running.GetOutput() != "" {
		t.Fatalf("running step = %#v", running)
	}
	if cells["cell-stored"].GetOutput() != "notebook output" {
		t.Fatalf("stored cell = %#v", cells["cell-stored"])
	}
}
