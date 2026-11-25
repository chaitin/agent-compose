package runs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

func TestAgentProjectionResetsTurnStateAndKeepsPendingMessages(t *testing.T) {
	for _, format := range []string{runlog.FormatJSONL, "text"} {
		t.Run(format, func(t *testing.T) {
			run := projectionTestRun(t)
			if format == "text" {
				run.LogFormat, run.LogsPath = "", filepath.Join(t.TempDir(), "transcript.txt")
			}
			p := newAgentRunProjector(context.Background(), run, nil, run.LogsPath, nil)
			for turn := uint64(0); turn < 2; turn++ {
				if turn > 0 {
					if err := p.AppendHumanMessageFrame("next", "human-2"); err != nil {
						t.Fatal(err)
					}
				}
				for i, event := range []string{
					`{"kind":"tool_call","id":"same","name":"shell","command":"pwd"}`,
					`{"kind":"tool_result","id":"same","output":"/workspace\n"}`,
					`{"kind":"text_delta","text":"same answer"}`,
				} {
					if _, err := p.ProjectFrame(execution.AgentFrame{Type: "agent_event", Seq: turn*10 + uint64(i) + 1, Event: []byte(event)}); err != nil {
						t.Fatal(err)
					}
				}
				frame := execution.AgentFrame{Type: "agent_turn_completed", Seq: turn*10 + 4, FinalText: "same answer", FinalTextSource: domain.AgentFinalTextSourceProviderMessage}
				if _, err := p.ProjectFrame(frame); err != nil {
					t.Fatal(err)
				}
			}
			terminal, err := p.ProjectFrame(execution.AgentFrame{Type: "result", Seq: 20, FinalText: "same answer", FinalTextSource: domain.AgentFinalTextSourceProviderMessage})
			if err != nil || terminal.Terminal == nil || len(terminal.Terminal.TerminalEvents) != 2 {
				t.Fatalf("terminal=%+v err=%v", terminal, err)
			}
			if terminal.Terminal.TerminalEvents[0].ID == terminal.Terminal.TerminalEvents[1].ID {
				t.Fatal("different turns shared a message ID")
			}
			var text string
			if format == "text" {
				data, err := os.ReadFile(run.LogsPath)
				if err != nil {
					t.Fatal(err)
				}
				text = string(data)
			} else {
				entries := readProjectionLog(t, run)
				text = runlog.RenderText(entries)
				ends := 0
				for _, entry := range entries {
					if entry.Type == runlog.EntryTurnEnd {
						ends++
					}
				}
				if ends != 2 {
					t.Fatalf("turn ends=%d", ends)
				}
			}
			for _, fragment := range []string{"same answer", "$ pwd", "/workspace"} {
				if strings.Count(text, fragment) != 2 {
					t.Fatalf("turn content %q not recorded twice: %q", fragment, text)
				}
			}
		})
	}
}

func TestAgentProjectionRetriesMessagePersistenceWithoutEndingTurnAgain(t *testing.T) {
	run := projectionTestRun(t)
	store := &retryProjectionEventStore{projectorEventStore: projectorEventStore{keys: map[string]struct{}{}}, fail: true}
	p := newAgentRunProjector(context.Background(), run, nil, run.LogsPath, nil)
	p.eventCtx, p.events = context.Background(), store
	frame := execution.AgentFrame{Type: "agent_turn_completed", Seq: 1, FinalText: "done", FinalTextSource: domain.AgentFinalTextSourceProviderMessage}
	if _, err := p.ProjectFrame(frame); err == nil {
		t.Fatal("message persistence error was lost")
	}
	if effect, err := p.ProjectFrame(frame); err != nil || !effect.TurnCompleted {
		t.Fatalf("retry=%+v err=%v", effect, err)
	}
	if effect, err := p.ProjectFrame(frame); err != nil || !effect.Ignored {
		t.Fatalf("repeated completed turn=%+v err=%v", effect, err)
	}
	effect, err := p.ProjectFrame(execution.AgentFrame{Type: "result", Seq: 2, FinalText: "done"})
	if err != nil || effect.Terminal == nil || len(effect.Terminal.TerminalEvents) != 0 || len(store.events) != 1 {
		t.Fatalf("terminal=%+v messages=%+v err=%v", effect, store.events, err)
	}
	ends := 0
	for _, entry := range readProjectionLog(t, run) {
		if entry.Type == runlog.EntryTurnEnd {
			ends++
		}
	}
	if ends != 1 {
		t.Fatalf("turn ends=%d", ends)
	}
}

func TestAgentCompletionPreservesConfirmedOutcomeWhenLogUnavailable(t *testing.T) {
	run := domain.ProjectRunRecord{RunID: "r", LogFormat: runlog.FormatJSONL, LogsPath: t.TempDir()}
	p := newAgentRunProjector(context.Background(), run, nil, run.LogsPath, nil)
	transition := p.CompleteCell(domain.NotebookCell{Success: true}, domain.SandboxEvent{Message: "done"}, nil)
	if transition.ExitCode != 0 || transition.Error != "" || len(transition.TerminalEvents) != 1 || transition.TerminalEvents[0].Text != "done" {
		t.Fatalf("confirmed outcome changed: %+v", transition)
	}
}

func TestAgentCompletionProcessFailureOverridesWireSuccess(t *testing.T) {
	run := projectionTestRun(t)
	p := newAgentRunProjector(context.Background(), run, nil, run.LogsPath, nil)
	if _, err := p.ProjectFrame(execution.AgentFrame{Type: "result", FinalText: "done", FinalTextSource: domain.AgentFinalTextSourceProviderMessage}); err != nil {
		t.Fatal(err)
	}
	transition := p.CompleteCell(domain.NotebookCell{ExitCode: 7}, domain.SandboxEvent{Message: "done"}, errors.New("process exit failed"))
	if transition.ExitCode != 7 || transition.Error == "" || transition.ErrorStack != "process exit failed" {
		t.Fatalf("wire success hid process failure: %+v", transition)
	}
	if len(transition.TerminalEvents) != 1 || transition.TerminalEvents[0].Text != "done" {
		t.Fatalf("completed answer was lost: %+v", transition.TerminalEvents)
	}
	entries := readProjectionLog(t, run)
	if len(entries) != 3 || entries[2].Type != runlog.EntryTurnEnd {
		t.Fatalf("process completion duplicated turn: %+v", entries)
	}
}

func TestAgentExecutionCompletionRequiresResultAndHonorsProcessOutcome(t *testing.T) {
	for _, mode := range []string{"missing_result", "process_failure", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			run := projectionTestRun(t)
			p := newAgentRunProjector(context.Background(), run, nil, run.LogsPath, nil)
			process := domain.ExecResult{Success: true}
			if mode != "missing_result" {
				frame := execution.AgentFrame{Type: "result", FinalText: "answer", FinalTextSource: domain.AgentFinalTextSourceProviderMessage}
				if mode == "cancelled" {
					frame.StopReason = "cancelled"
				} else {
					process = domain.ExecResult{ExitCode: 7}
				}
				if _, err := p.ProjectFrame(frame); err != nil {
					t.Fatal(err)
				}
			}
			transition, err := p.CompleteExecution(process, nil)
			if err == nil || transition.ExitCode == 0 || transition.Error == "" {
				t.Fatalf("invalid execution succeeded: transition=%+v err=%v", transition, err)
			}
			if mode == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost its classification: %v", err)
			}
			entries := readProjectionLog(t, run)
			ends := 0
			for _, entry := range entries {
				if entry.Type == runlog.EntryTurnEnd {
					ends++
				}
			}
			if ends != 1 {
				t.Fatalf("turn ends=%d entries=%+v", ends, entries)
			}
		})
	}
}

type retryProjectionEventStore struct {
	projectorEventStore
	fail bool
}

func (s *retryProjectionEventStore) AppendProjectRunEvents(ctx context.Context, events []domain.ProjectRunEventRecord) ([]domain.ProjectRunEventRecord, []bool, error) {
	if s.fail {
		s.fail = false
		return nil, nil, errors.New("storage unavailable")
	}
	return s.projectorEventStore.AppendProjectRunEvents(ctx, events)
}
