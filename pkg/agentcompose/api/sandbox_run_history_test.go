package api

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

type fakeSandboxRunHistory struct {
	runs     []domain.ProjectRunRecord
	messages []domain.ProjectRunEventRecord
}

func (f fakeSandboxRunHistory) ListProjectRunsByOptions(_ context.Context, options domain.ProjectRunListOptions) (domain.ProjectRunListResult, error) {
	var matched []domain.ProjectRunRecord
	for _, run := range f.runs {
		if run.SandboxID == options.SandboxID {
			matched = append(matched, run)
		}
	}
	start := min(options.Offset, len(matched))
	return domain.ProjectRunListResult{Runs: matched[start:min(start+options.Limit, len(matched))]}, nil
}

func (f fakeSandboxRunHistory) ListProjectRunMessagesPage(_ context.Context, _, _ string, offset, limit int) ([]domain.ProjectRunEventRecord, error) {
	start := min(offset, len(f.messages))
	return f.messages[start:min(start+limit, len(f.messages))], nil
}

func writeHistoryRunLog(t *testing.T, root, runID, output string) string {
	t.Helper()
	path := filepath.Join(root, "runs", runID, runlog.FileName)
	w, err := runlog.Open(path, runlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.AppendInitialPrompt("prompt"); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendOutput("stdout", output); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestListSandboxHistoryRebuildsRunLogAgentRuns(t *testing.T) {
	const sandboxID = "28fed243-4d9d-4e56-96cf-8b2baa8643c8"
	root := t.TempDir()
	at := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	donePath := writeHistoryRunLog(t, root, "run-done", "built ok\n")
	archivedPath := writeHistoryRunLog(t, root, "run-archived", "old output\n")
	if _, err := runlog.ArchiveLog(archivedPath, filepath.Join(root, "archives", "runs", "run-archived", runlog.ArchivedFileName), at); err != nil {
		t.Fatal(err)
	}
	history := fakeSandboxRunHistory{
		runs: []domain.ProjectRunRecord{
			{RunID: "run-done", SandboxID: sandboxID, AgentName: "dev", AgentCellID: "cell-done", Prompt: "build it", Status: domain.ProjectRunStatusSucceeded, LogFormat: runlog.FormatJSONL, LogsPath: donePath, StartedAt: at.Add(time.Minute)},
			{RunID: "run-archived", SandboxID: sandboxID, AgentName: "dev", AgentCellID: "cell-archived", Prompt: "old", Status: domain.ProjectRunStatusFailed, Error: "agent crashed", ExitCode: 3, LogFormat: runlog.FormatJSONL, LogsPath: archivedPath, StartedAt: at, CompletedAt: at.Add(30 * time.Second)},
			// A notebook run is already in the sandbox's own history.
			{RunID: "run-notebook", SandboxID: sandboxID, AgentCellID: "cell-stored", Status: domain.ProjectRunStatusSucceeded, LogsPath: "legacy.txt", StartedAt: at},
			// A run whose agent never started has no cell.
			{RunID: "run-early", SandboxID: sandboxID, Status: domain.ProjectRunStatusFailed, LogFormat: runlog.FormatJSONL, StartedAt: at},
		},
		messages: []domain.ProjectRunEventRecord{
			{ID: "m1", RunID: "run-done", Kind: domain.ProjectRunEventKindUserMessage, Text: "build it", CreatedAt: at.Add(time.Minute)},
			{ID: "m2", RunID: "run-done", Kind: domain.ProjectRunEventKindAgentMessage, Text: "done", Agent: "claude", StopReason: "end_turn", CreatedAt: at.Add(2 * time.Minute)},
			{ID: "m3", RunID: "run-notebook", Kind: domain.ProjectRunEventKindUserMessage, Text: "duplicate", CreatedAt: at},
		},
	}
	store := &characterizationSandboxStore{
		session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID}},
		cells:   []domain.NotebookCell{{ID: "cell-stored", Type: "agent", Output: "notebook output", CreatedAt: at}},
	}
	handler := NewSandboxHandler(SandboxHandlerDeps{Delegate: &characterizationSessionDelegate{}, Store: store, Remover: &characterizationSandboxRemover{}}).WithRunHistory(history)

	response, err := handler.ListSandboxHistory(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxHistoryRequest{SandboxId: sandboxID}))
	if err != nil {
		t.Fatal(err)
	}
	cells := map[string]*agentcomposev2.SandboxHistoryCell{}
	for _, cell := range response.Msg.GetCells() {
		cells[cell.GetId()] = cell
	}
	if len(cells) != 3 {
		t.Fatalf("cells = %v, want the stored cell and two rebuilt runs", response.Msg.GetCells())
	}
	done := cells["cell-done"]
	if done.GetSource() != "build it" || !done.GetSuccess() || done.GetRunning() || done.GetAgent() != "claude" || done.GetStopReason() != "end_turn" || done.GetOutput() != "built ok\n" {
		t.Fatalf("rebuilt cell = %#v", done)
	}
	archived := cells["cell-archived"]
	if archived.GetSuccess() || archived.GetExitCode() != 3 || !strings.Contains(archived.GetOutput(), "archived") || strings.Contains(archived.GetOutput(), root) {
		t.Fatalf("archived run cell = %#v", archived)
	}
	if cells["cell-stored"].GetOutput() != "notebook output" {
		t.Fatalf("stored cell = %#v", cells["cell-stored"])
	}
	var types []string
	for _, event := range response.Msg.GetEvents() {
		types = append(types, event.GetType()+":"+event.GetMessage())
	}
	// Newest first, and the notebook run's message is not repeated.
	if got := strings.Join(types, "|"); got != "agent.assistant:done|agent.user:build it|agent.assistant.failed:agent crashed" {
		t.Fatalf("events = %s", got)
	}
	if response.Msg.GetTotal() != 6 {
		t.Fatalf("total = %d, want 3 cells + 3 events", response.Msg.GetTotal())
	}
}

func TestListSandboxHistoryKeepsTheTailOfALongRunLog(t *testing.T) {
	path := writeHistoryRunLog(t, t.TempDir(), "run-long", strings.Repeat("x", sandboxRunOutputBytes)+"the end\n")
	output := sandboxRunOutput(path)
	if !strings.HasPrefix(output, "[first ") || !strings.HasSuffix(output, "the end\n") || len(output) > sandboxRunOutputBytes+100 {
		t.Fatalf("output = %d bytes, starts %q", len(output), output[:min(len(output), 40)])
	}
}
