package runs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

func TestIntegrationAgentJournalRestoresSplitFramesAndTurnLifecycle(t *testing.T) {
	for _, transport := range []string{"exec", "attach"} {
		t.Run(transport, func(t *testing.T) {
			run := projectionTestRun(t)
			store := &projectorEventStore{keys: map[string]struct{}{}}
			p := newAgentRunProjector(context.Background(), run, nil, run.LogsPath, nil)
			p.events = store
			var offset uint64
			push := func(text string, stream domain.StdioStream) {
				t.Helper()
				offset += uint64(len(text))
				chunk := domain.ExecChunk{Text: text, Stream: stream, Journal: &domain.JournalPosition{End: offset}}
				if transport == "exec" {
					callbacks := projectRunAgentExecutionStream(context.Background(), agentExecutionStreamRequest{Run: run, Projector: p})
					if err := callbacks.OnRuntimeOutput(chunk); err != nil {
						t.Fatal(err)
					}
				} else {
					attach := &promptAttachProjector{projection: p}
					if _, _, err := attach.ProjectChunk(chunk); err != nil {
						t.Fatal(err)
					}
				}
			}
			restart := func() *execution.AgentJournalResume {
				t.Helper()
				if err := closeRunLog(run.LogsPath); err != nil {
					t.Fatal(err)
				}
				p = newAgentRunProjector(context.Background(), run, nil, run.LogsPath, nil)
				p.events = store
				resume, err := p.RestoreJournal()
				if err != nil || resume == nil || resume.Offset != offset {
					t.Fatalf("resume=%+v offset=%d err=%v", resume, offset, err)
				}
				return resume
			}
			push(`{"v":1,"seq":1,"type":"agent_event","event":{"kind":"text_delta","text":"hel`, domain.StdioStdout)
			push("warning\n", domain.StdioStderr)
			recovery := restart()
			if !strings.HasSuffix(recovery.Decoder.Pending, "hel") {
				t.Fatalf("decoder=%+v", recovery.Decoder)
			}
			push("lo\"}}\n", domain.StdioStdout)
			tool := "{\"v\":1,\"seq\":2,\"type\":\"agent_event\",\"event\":{\"kind\":\"tool_call\",\"id\":\"same\",\"name\":\"shell\",\"command\":\"pwd\"}}\n"
			push(tool, domain.StdioStdout)
			restart()
			push(tool, domain.StdioStdout)
			completion := "{\"v\":1,\"seq\":3,\"type\":\"agent_turn_completed\",\"finalText\":\"hello done\",\"finalTextSource\":\"provider_message\"}\n"
			push(completion, domain.StdioStdout)
			restart()
			push(completion, domain.StdioStdout)
			push("{\"v\":1,\"seq\":4,\"type\":\"result\",\"finalText\":\"hello done\",\"finalTextSource\":\"provider_message\"}\n", domain.StdioStdout)
			recovery = restart()
			if !json.Valid([]byte(recovery.ResultJSON)) {
				t.Fatalf("lost terminal result: %+v", recovery)
			}
			transition, err := p.CompleteExecution(domain.ExecResult{Success: true}, nil)
			if err != nil || transition.Error != "" || len(transition.TerminalEvents) != 0 {
				t.Fatalf("transition=%+v err=%v", transition, err)
			}
			entries := readProjectionLog(t, run)
			ends := 0
			for _, e := range entries {
				if e.Type == runlog.EntryTurnEnd {
					ends++
				}
				if e.Type != runlog.EntryUser && e.SourceOffset == 0 {
					t.Fatalf("missing source offset: %+v", e)
				}
			}
			if ends != 1 || runlog.Summarize(entries).ToolCalls != 1 || len(store.events) != 1 || store.events[0].Text != "hello done" || store.events[0].LogSeq == 0 {
				t.Fatalf("entries=%+v messages=%+v", entries, store.events)
			}
			if text := runlog.RenderText(entries); strings.Count(text, "hello") != 1 || strings.Count(text, "done") != 1 || strings.Count(text, "warning") != 1 {
				t.Fatalf("text duplicated/lost: %q", text)
			}
		})
	}
}

func TestIntegrationAgentJournalRestoresGapAndKeepsLaterOutput(t *testing.T) {
	run := projectionTestRun(t)
	p := newAgentRunProjector(context.Background(), run, nil, run.LogsPath, nil)
	push := func(p *agentRunProjector, chunk domain.ExecChunk) {
		t.Helper()
		if _, err := p.ProjectRuntimeOutput(chunk); err != nil {
			t.Fatal(err)
		}
	}
	push(p, domain.ExecChunk{Text: `{"type":"agent_event","event":`, Journal: &domain.JournalPosition{End: 30}})
	push(p, domain.ExecChunk{Journal: &domain.JournalPosition{End: 90, Gap: &domain.JournalGap{From: 30, To: 90}}})
	if err := closeRunLog(run.LogsPath); err != nil {
		t.Fatal(err)
	}
	p = newAgentRunProjector(context.Background(), run, nil, run.LogsPath, nil)
	recovery, err := p.RestoreJournal()
	if err != nil || recovery == nil || recovery.Offset != 90 {
		t.Fatalf("recovery=%+v err=%v", recovery, err)
	}
	push(p, domain.ExecChunk{Text: "broken suffix\n{\"type\":\"result\",\"finalText\":\"after gap\",\"finalTextSource\":\"provider_message\"}\n", Journal: &domain.JournalPosition{End: 200}})
	entries := readProjectionLog(t, run)
	gaps := 0
	for _, e := range entries {
		if e.SourceGap {
			gaps++
			if e.DroppedBytes != 60 {
				t.Fatalf("gap=%+v", e)
			}
		}
	}
	text := runlog.RenderText(entries)
	if gaps != 1 || !strings.Contains(text, "after gap") || strings.Contains(text, "broken suffix") {
		t.Fatalf("entries=%+v text=%q", entries, text)
	}
}

func TestIntegrationCheckpointedAgentExitCompletesWithoutGuestJournal(t *testing.T) {
	for _, kind := range []string{domain.DetachKindAgent, domain.DetachKindInteractivePrompt} {
		for _, exitCode := range []int{0, 7} {
			t.Run(kind+string(rune('0'+exitCode)), func(t *testing.T) {
				resumer := &fakeAgentResumer{}
				controller, run := newAgentReattachTestController(t, resumer)
				run.DetachKind = kind
				logsPath, err := runlog.RunPath(controller.config.DataRoot, run.RunID)
				if err != nil {
					t.Fatal(err)
				}
				run.LogFormat, run.LogsPath = runlog.FormatJSONL, logsPath
				if _, err := controller.configDB.UpdateProjectRun(context.Background(), run); err != nil {
					t.Fatal(err)
				}
				sandbox, err := controller.store.GetSandbox(context.Background(), run.SandboxID)
				if err != nil {
					t.Fatal(err)
				}
				p := newAgentRunProjector(context.Background(), run, sandbox, run.LogsPath, nil)
				if _, err := p.ProjectRuntimeOutput(domain.ExecChunk{Text: "{\"v\":1,\"type\":\"result\",\"finalText\":\"done\",\"finalTextSource\":\"provider_message\"}\n", Journal: &domain.JournalPosition{End: 100}}); err != nil {
					t.Fatal(err)
				}
				if _, err := p.ProjectRuntimeOutput(domain.ExecChunk{Journal: &domain.JournalPosition{End: 100, Exit: &domain.JournalExit{Result: domain.ExecResult{ExitCode: exitCode, Success: exitCode == 0}}}}); err != nil {
					t.Fatal(err)
				}
				if err := closeRunLog(run.LogsPath); err != nil {
					t.Fatal(err)
				}
				// Simulate restart after journal cleanup and even sandbox shutdown.
				sandbox.Summary.VMStatus = domain.VMStatusStopped
				if err := controller.store.UpdateSandbox(context.Background(), sandbox); err != nil {
					t.Fatal(err)
				}
				controller.runtime = nil
				got, err := controller.ReattachDetachedRun(context.Background(), run)
				if err != nil {
					t.Fatal(err)
				}
				want := domain.ProjectRunStatusSucceeded
				if exitCode != 0 {
					want = domain.ProjectRunStatusFailed
				}
				if got.Status != want || got.ExitCode != exitCode || resumer.resumed.RunID != "" {
					t.Fatalf("run=%+v resume=%+v", got, resumer.resumed)
				}
				if got.Output != "done" {
					t.Fatalf("lost checkpointed result: %+v", got)
				}
			})
		}
	}
}

func TestIntegrationAgentJournalFlushesFinalResultWithoutNewline(t *testing.T) {
	run := projectionTestRun(t)
	p := newAgentRunProjector(context.Background(), run, nil, run.LogsPath, nil)
	if _, err := p.ProjectRuntimeOutput(domain.ExecChunk{Text: `{"type":"result","finalText":"last answer","finalTextSource":"provider_message"}`, Journal: &domain.JournalPosition{End: 90}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ProjectRuntimeOutput(domain.ExecChunk{Journal: &domain.JournalPosition{End: 90, Exit: &domain.JournalExit{Result: domain.ExecResult{Success: true}}}}); err != nil {
		t.Fatal(err)
	}
	if err := closeRunLog(run.LogsPath); err != nil {
		t.Fatal(err)
	}
	p = newAgentRunProjector(context.Background(), run, nil, run.LogsPath, nil)
	recovered, err := p.RestoreJournal()
	if err != nil || recovered == nil || recovered.Exit == nil || recovered.ResultJSON == "" {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	transition, err := p.CompleteExecution(recovered.Exit.Result, nil)
	if err != nil || len(transition.TerminalEvents) != 1 || transition.Output != "last answer" {
		t.Fatalf("transition=%+v err=%v", transition, err)
	}
}

func TestIntegrationPromptNeutralErrorRemainsAnAgentEvent(t *testing.T) {
	run := projectionTestRun(t)
	p := newPromptAttachProjector(run, nil, run.LogsPath, nil)
	responses, terminal, err := p.Project([]byte("{\"type\":\"agent_event\",\"event\":{\"kind\":\"error\",\"message\":\"retrying\"}}\n"))
	if err != nil || terminal != nil || len(responses) != 1 || responses[0].Kind != RunAttachOutputAgentEvent {
		t.Fatalf("responses=%+v terminal=%+v err=%v", responses, terminal, err)
	}
}
