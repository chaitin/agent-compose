package adapters

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
)

func TestIntegrationAgentRunLogSkipsNotebookAndTraceStorage(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "fresh"
		if resume {
			name = "resume"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			runtime := &detachAgentRuntime{}
			payload := execution.AgentResultPrefix + detachAgentPayload + "\n"
			runtime.journal = journalLines(t, "visible output\n", payload)
			f := newDetachRunnerFixture(t, runtime)
			executor := NewAgentExecutor(f.runner.config, f.store, nil, f.runner)
			// Block both legacy persistence locations. The run must still complete.
			state := filepath.Join(execution.HostSandboxDir(f.session), "state")
			for _, name := range []string{"cells", "cells.json"} {
				path := filepath.Join(state, name)
				if name == "cells" {
					if err := os.WriteFile(path, []byte("legacy storage unavailable"), 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(path, 0o755); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, err := f.store.ListEvents(ctx, f.session.Summary.ID)
			if err != nil {
				t.Fatal(err)
			}
			var chunks []string
			stream := execution.AgentExecutionStream{OnChunk: func(_ string, chunk domain.ExecChunk) error { chunks = append(chunks, chunk.Text); return nil }}
			var cell domain.NotebookCell
			var assistant domain.SandboxEvent
			if resume {
				cell, _, assistant, err = executor.ResumeAgentRequest(ctx, f.session, execution.ResumeAgentRequest{
					History: execution.AgentHistoryRunLog, Agent: "claude", Message: "hello", CellID: "step-1", RunID: "run-1", ExecutionID: "exec-1", Stream: stream,
				})
			} else {
				cell, _, assistant, err = executor.ExecuteAgentRequest(ctx, f.session, execution.ExecuteAgentRequest{
					History: execution.AgentHistoryRunLog, Agent: "claude", Message: "hello", RunID: "run-1", Stream: stream,
					Detach: &execution.AgentDetach{ExecutionID: "exec-1"},
				})
			}
			if err != nil || !cell.Success || cell.ID == "" || cell.Running || assistant.Message != "done" || !strings.Contains(strings.Join(chunks, ""), "visible output") {
				t.Fatalf("cell=%+v assistant=%+v chunks=%q err=%v", cell, assistant, chunks, err)
			}
			after, err := f.store.ListEvents(ctx, f.session.Summary.ID)
			if err != nil || len(after) != len(before) {
				t.Fatalf("duplicated sandbox events: before=%+v after=%+v err=%v", before, after, err)
			}
			if data, err := os.ReadFile(filepath.Join(state, "cells")); err != nil || string(data) != "legacy storage unavailable" {
				t.Fatalf("legacy storage changed: %q %v", data, err)
			}

		})
	}
}

// A run-log run stores nothing in the sandbox, but WatchSandbox subscribers
// still see the turn start, stream and finish under one cell id.
func TestIntegrationAgentRunLogStillReachesSandboxWatchers(t *testing.T) {
	ctx := context.Background()
	runtime := &detachAgentRuntime{}
	runtime.journal = journalLines(t, "visible output\n", execution.AgentResultPrefix+detachAgentPayload+"\n")
	f := newDetachRunnerFixture(t, runtime)
	broker := sandboxes.NewStreamBrokerForTest()
	events, unsubscribe := broker.Subscribe(f.session.Summary.ID)
	defer unsubscribe()
	executor := NewAgentExecutor(f.runner.config, f.store, broker, f.runner)
	before, err := f.store.ListEvents(ctx, f.session.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}

	cell, _, _, err := executor.ExecuteAgentRequest(ctx, f.session, execution.ExecuteAgentRequest{
		History: execution.AgentHistoryRunLog, Agent: "claude", Message: "hello", RunID: "run-1",
		Detach: &execution.AgentDetach{ExecutionID: "exec-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[sandboxes.WatchEventType]int{}
	var output strings.Builder
	for drained := false; !drained; {
		select {
		case event := <-events:
			seen[event.EventType]++
			switch event.EventType {
			case sandboxes.WatchEventTypeCellStarted, sandboxes.WatchEventTypeCellCompleted:
				if event.Cell == nil || event.Cell.ID != cell.ID {
					t.Fatalf("%v for cell %+v, want %s", event.EventType, event.Cell, cell.ID)
				}
			case sandboxes.WatchEventTypeCellOutput:
				if event.CellID != cell.ID {
					t.Fatalf("output for cell %s, want %s", event.CellID, cell.ID)
				}
				output.WriteString(event.Chunk)
			}
		default:
			drained = true
		}
	}
	if seen[sandboxes.WatchEventTypeCellStarted] != 1 || seen[sandboxes.WatchEventTypeCellCompleted] != 1 ||
		seen[sandboxes.WatchEventTypeEventAdded] != 2 || !strings.Contains(output.String(), "visible output") {
		t.Fatalf("watch events = %v, output %q", seen, output.String())
	}
	after, err := f.store.ListEvents(ctx, f.session.Summary.ID)
	if err != nil || len(after) != len(before) {
		t.Fatalf("run-log run stored sandbox events: before=%d after=%d err=%v", len(before), len(after), err)
	}
	cells, err := f.store.ListCells(ctx, f.session.Summary.ID)
	if err != nil || len(cells) != 0 {
		t.Fatalf("run-log run stored cells: %+v %v", cells, err)
	}
}
