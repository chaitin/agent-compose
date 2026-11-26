package runs

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

func TestIntegrationAgentTransportsShareLifecycle(t *testing.T) {
	frames := []string{
		`{"v":1,"seq":0,"type":"started"}`,
		`{"v":1,"seq":1,"type":"agent_event","event":{"kind":"text_delta","text":"checking. "}}`,
		`{"v":1,"seq":2,"type":"agent_event","event":{"kind":"tool_call","id":"tool","name":"shell","command":"pwd"}}`,
		`{"v":1,"seq":3,"type":"agent_event","event":{"kind":"tool_result","id":"tool","output":"/workspace\n","ok":true}}`,
		`{"v":1,"seq":4,"type":"agent_turn_completed","provider":"codex","finalText":"checking. done","finalTextSource":"provider_message","stopReason":"completed"}`,
		// Repeated completion and event frames must not duplicate any effects.
		`{"v":1,"seq":4,"type":"agent_turn_completed","provider":"codex","finalText":"checking. done","finalTextSource":"provider_message","stopReason":"completed"}`,
		`{"v":1,"seq":3,"type":"agent_event","event":{"kind":"tool_result","id":"tool","output":"/workspace\n","ok":true}}`,
		`{"v":1,"seq":5,"type":"result","provider":"codex","finalText":"checking. done","finalTextSource":"provider_message","stopReason":"completed"}`,
	}
	var logs [][]runlog.Entry
	var messages [][]domain.ProjectRunEventRecord
	for _, transport := range []string{"exec", "attach"} {
		t.Run(transport, func(t *testing.T) {
			run := projectionTestRun(t)
			sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "s"}}
			store := &projectorEventStore{keys: map[string]struct{}{}}
			var transition *TransitionRequest
			if transport == "attach" {
				p := newPersistentPromptAttachProjector(context.Background(), persistentPromptAttachProjectorDeps{
					Run: run, Sandbox: sandbox, LogsPath: run.LogsPath, EventStore: store,
				})
				for _, line := range frames {
					for _, b := range []byte(line + "\n") {
						_, next, err := p.Project([]byte{b})
						if err != nil {
							t.Fatal(err)
						}
						if next != nil {
							transition = next
						}
					}
				}
			} else {
				p := newAgentRunProjector(context.Background(), run, sandbox, run.LogsPath, nil)
				stream := projectRunAgentExecutionStream(context.Background(), agentExecutionStreamRequest{Run: run, Sandbox: sandbox, Projector: p})
				for _, line := range frames {
					frame, _, err := execution.DecodeAgentFrame([]byte(line))
					if err != nil {
						t.Fatal(err)
					}
					if err := stream.OnFrame("cell", frame); err != nil {
						t.Fatal(err)
					}
				}
				result := p.CompleteCell(domain.NotebookCell{Success: true, Agent: "codex", StopReason: "completed"}, domain.SandboxEvent{Message: "checking. done"}, nil)
				transition = &result
			}
			if transition == nil || transition.ExitCode != 0 || transition.Error != "" {
				t.Fatalf("transition=%+v", transition)
			}
			events := append(store.events, transition.TerminalEvents...)
			if len(events) != 1 || events[0].Text != "checking. done" || events[0].LogSeq == 0 {
				t.Fatalf("messages=%+v", events)
			}
			entries := readProjectionLog(t, run)
			if len(entries) != 6 || entries[5].Type != runlog.EntryTurnEnd || entries[5].FinalText != "checking. done" {
				t.Fatalf("log=%+v", entries)
			}
			logs = append(logs, entries)
			messages = append(messages, events)
		})
	}
	if t.Failed() {
		return
	}
	for i := range logs[0] {
		logs[1][i].Time = logs[0][i].Time
	}
	if !reflect.DeepEqual(logs[0], logs[1]) || !reflect.DeepEqual(messages[0], messages[1]) {
		t.Fatalf("transport projections differ: logs=%+v messages=%+v", logs, messages)
	}
}

func TestIntegrationAgentLegacyCallbacksUseTerminalProjection(t *testing.T) {
	run := projectionTestRun(t)
	p := newAgentRunProjector(context.Background(), run, nil, run.LogsPath, nil)
	stream := projectRunAgentExecutionStream(context.Background(), agentExecutionStreamRequest{Run: run, Projector: p})
	if err := stream.OnAgentEvent("cell", []byte(`{"kind":"text_delta","text":"done"}`)); err != nil {
		t.Fatal(err)
	}
	if err := stream.OnChunk("cell", domain.ExecChunk{Text: "done", Transcript: true}); err != nil {
		t.Fatal(err)
	}
	cell := domain.NotebookCell{Success: true, Agent: "codex", StopReason: "completed"}
	first := p.CompleteCell(cell, domain.SandboxEvent{Message: "done"}, nil)
	again := p.CompleteCell(cell, domain.SandboxEvent{Message: "done"}, nil)
	if len(first.TerminalEvents) != 1 || !reflect.DeepEqual(first.TerminalEvents, again.TerminalEvents) {
		t.Fatalf("completion events=%+v repeated=%+v", first.TerminalEvents, again.TerminalEvents)
	}
	entries := readProjectionLog(t, run)
	if len(entries) != 3 || entries[1].Text != "done" || entries[2].Type != runlog.EntryTurnEnd {
		t.Fatalf("log=%+v", entries)
	}
}

func TestIntegrationAgentFinalOnlyResultIsVisibleInLog(t *testing.T) {
	for _, diagnostic := range []string{"", "warning\n"} {
		run := projectionTestRun(t)
		p := newAgentRunProjector(context.Background(), run, nil, run.LogsPath, nil)
		if err := p.AppendOutput(domain.ExecChunk{Text: diagnostic, Stream: domain.StdioStderr}); err != nil {
			t.Fatal(err)
		}
		terminal, err := p.ProjectFrame(execution.AgentFrame{Type: "result", FinalText: "final only", FinalTextSource: domain.AgentFinalTextSourceProviderMessage})
		if err != nil || terminal.Terminal == nil || len(terminal.Terminal.TerminalEvents) != 1 {
			t.Fatalf("terminal=%+v err=%v", terminal, err)
		}
		entries := readProjectionLog(t, run)
		if text := runlog.RenderText(entries); text != diagnostic+"final only" {
			t.Fatalf("final-only result absent from log: text=%q entries=%+v", text, entries)
		}
	}
}

func TestIntegrationAgentProjectionFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"error", "cancelled", "process_failure"} {
		t.Run(mode, func(t *testing.T) {
			run := projectionTestRun(t)
			p := newAgentRunProjector(context.Background(), run, nil, run.LogsPath, nil)
			if _, err := p.ProjectFrame(execution.AgentFrame{Type: "agent_event", Event: []byte(`{"kind":"text_delta","text":"partial"}`)}); err != nil {
				t.Fatal(err)
			}
			var terminal TransitionRequest
			if mode == "process_failure" {
				terminal = p.CompleteCell(domain.NotebookCell{ExitCode: 7}, domain.SandboxEvent{Message: "failure diagnostic"}, errors.New("process failed"))
			} else {
				frame := execution.AgentFrame{Type: "error", Message: "provider failed"}
				if mode == "cancelled" {
					frame = execution.AgentFrame{Type: "result", StopReason: "cancelled", FinalText: "partial", FinalTextSource: domain.AgentFinalTextSourceProviderMessage}
				}
				projection, err := p.ProjectFrame(frame)
				if err != nil || projection.Terminal == nil {
					t.Fatalf("projection=%+v err=%v", projection, err)
				}
				terminal = *projection.Terminal
			}
			if terminal.ExitCode == 0 || terminal.Error == "" || len(terminal.TerminalEvents) != 0 {
				t.Fatalf("failure became success/message: %+v", terminal)
			}
			entries := readProjectionLog(t, run)
			ends := 0
			for _, entry := range entries {
				if entry.Type == runlog.EntryTurnEnd {
					ends++
					if entry.StopReason != "error" && entry.StopReason != "cancelled" {
						t.Fatalf("turn end=%+v", entry)
					}
				}
			}
			if ends != 1 {
				t.Fatalf("turn ends=%d log=%+v", ends, entries)
			}
		})
	}
}

func projectionTestRun(t *testing.T) domain.ProjectRunRecord {
	t.Helper()
	path := filepath.Join(t.TempDir(), runlog.FileName)
	t.Cleanup(func() {
		if err := closeRunLog(context.Background(), path); err != nil {
			t.Error(err)
		}
	})
	return domain.ProjectRunRecord{RunID: "shared", Prompt: "go", AgentName: "worker", LogFormat: runlog.FormatJSONL, LogsPath: path}
}

func readProjectionLog(t *testing.T, run domain.ProjectRunRecord) []runlog.Entry {
	t.Helper()
	if err := closeRunLog(context.Background(), run.LogsPath); err != nil {
		t.Fatal(err)
	}
	entries, err := runlog.ReadEntries(run.LogsPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
