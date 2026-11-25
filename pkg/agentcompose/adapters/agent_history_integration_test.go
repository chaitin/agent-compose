package adapters

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
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
