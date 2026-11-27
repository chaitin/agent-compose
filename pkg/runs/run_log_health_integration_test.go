package runs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

type logHealthTestStore struct {
	projectorEventStore
	incomplete bool
}

func (s *logHealthTestStore) MarkProjectRunLogIncomplete(context.Context, string) error {
	s.incomplete = true
	return nil
}

// A run whose log cannot be written still finishes with its real outcome and
// is marked incomplete. With no commit to resume from, a restart falls back to
// replaying the journal from the start.
func TestIntegrationAgentLogFailurePreservesOutcome(t *testing.T) {
	for _, transport := range []string{"exec", "attach"} {
		for _, fault := range []string{"open", "append"} {
			t.Run(transport+"/"+fault, func(t *testing.T) {
				ctx := context.Background()
				run := projectionTestRun(t)
				switch fault {
				case "open":
					blocker := filepath.Join(t.TempDir(), "blocked")
					if err := os.WriteFile(blocker, nil, 0o600); err != nil {
						t.Fatal(err)
					}
					run.LogsPath = filepath.Join(blocker, runlog.FileName)
				case "append":
					w, err := runLogWriter(context.Background(), run, nil)
					if err != nil {
						t.Fatal(err)
					}
					// Leave the cached writer closed to simulate an append I/O failure.
					if err := w.Abandon(); err != nil {
						t.Fatal(err)
					}
				}
				store := &logHealthTestStore{projectorEventStore: projectorEventStore{keys: map[string]struct{}{}}}
				p := newAgentRunProjector(ctx, run, nil, run.LogsPath, nil)
				p.bindStore(store)
				var offset uint64
				push := func(text string) {
					t.Helper()
					offset += uint64(len(text))
					chunk := domain.ExecChunk{Text: text, Journal: &domain.JournalPosition{End: offset}}
					var err error
					if transport == "exec" {
						stream := projectRunAgentExecutionStream(ctx, agentExecutionStreamRequest{Run: run, Projector: p})
						err = stream.OnRuntimeOutput(chunk)
					} else {
						_, _, err = (&promptAttachProjector{projection: p}).ProjectChunk(chunk)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				push(`{"v":1,"seq":1,"type":"agent_event","event":{"kind":"text_delta","text":"hel`)
				if !store.incomplete {
					t.Fatalf("log fault not recorded: %+v", store)
				}
				push("lo\"}}\n")
				completed := "{\"v\":1,\"seq\":2,\"type\":\"agent_turn_completed\",\"finalText\":\"hello\",\"finalTextSource\":\"provider_message\"}\n"
				push(completed)
				push(completed)
				push("{\"v\":1,\"seq\":3,\"type\":\"result\",\"finalText\":\"hello\",\"finalTextSource\":\"provider_message\"}\n")
				if len(store.events) != 1 || store.events[0].Text != "hello" || store.events[0].LogSeq != 0 {
					t.Fatalf("messages=%+v", store.events)
				}
				if _, err := p.ProjectRuntimeOutput(domain.ExecChunk{Journal: &domain.JournalPosition{End: offset, Exit: &domain.JournalExit{Result: domain.ExecResult{Success: true}}}}); err != nil {
					t.Fatal(err)
				}
				transition, err := p.CompleteExecution(domain.ExecResult{Success: true}, nil)
				if err != nil || transition.ExitCode != 0 || transition.Error != "" || !transition.LogIncomplete {
					t.Fatalf("transition=%+v err=%v", transition, err)
				}
				processErr := errors.New("process failed")
				transition, err = p.CompleteExecution(domain.ExecResult{ExitCode: 7}, processErr)
				if err == nil || transition.ExitCode != 7 || !transition.LogIncomplete {
					t.Fatalf("failure was masked: %+v %v", transition, err)
				}
				abandonRunLog(t, run.LogsPath)
				run.LogIncomplete = true
				restarted := newAgentRunProjector(ctx, run, nil, run.LogsPath, nil)
				restarted.bindStore(store)
				if resume, err := restarted.RestoreJournal(); err != nil || resume != nil {
					t.Fatalf("resume=%+v err=%v; nothing was committed", resume, err)
				}
			})
		}
	}
}

func TestIntegrationCommandLogFailureStillForwardsOutputAndResult(t *testing.T) {
	for _, exitCode := range []int{0, 7} {
		name := "success"
		if exitCode != 0 {
			name = "process_failure"
		}
		t.Run(name, func(t *testing.T) {
			run := projectionTestRun(t)
			if err := os.Mkdir(run.LogsPath, 0o755); err != nil {
				t.Fatal(err)
			}
			interaction := newScriptedRunAttachInteraction()
			interaction.frames <- driverpkg.RuntimeOutputFrame{Type: driverpkg.RuntimeOutputStdout, Data: []byte("first\n")}
			interaction.frames <- driverpkg.RuntimeOutputFrame{Type: driverpkg.RuntimeOutputStderr, Data: []byte("last\n")}
			interaction.frames <- driverpkg.RuntimeOutputFrame{Type: driverpkg.RuntimeOutputResult, Result: &driverpkg.RuntimeResult{Success: exitCode == 0, ExitCode: exitCode}}
			close(interaction.frames)
			var outputs int
			transition, err := (&Controller{}).receiveCommandInteractionFrames(commandInteractionReceive{Context: context.Background(), Run: run, Sandbox: &domain.Sandbox{}, LogsPath: run.LogsPath, Interaction: interaction}, func(output RunAttachOutput) error { outputs++; return nil })
			if err != nil || outputs != 2 || transition.ExitCode != exitCode || !transition.LogIncomplete || transition.Output != "first\nlast\n" {
				t.Fatalf("transition=%+v outputs=%d err=%v", transition, outputs, err)
			}
		})
	}
}

func TestIntegrationTerminalLogFailurePreservesSelectedStatus(t *testing.T) {
	for _, status := range []string{domain.ProjectRunStatusSucceeded, domain.ProjectRunStatusFailed, domain.ProjectRunStatusCanceled} {
		t.Run(status, func(t *testing.T) {
			run := projectionTestRun(t)
			if err := os.Mkdir(run.LogsPath, 0o755); err != nil {
				t.Fatal(err)
			}
			completion := domain.ProjectRunCompletionRecord{TargetStatus: status, TransitionJSON: `{"RunID":"shared","ExitCode":7}`}
			next, _, err := terminalRunUpdate(context.Background(), run, completion, time.Unix(100, 0))
			if err != nil || next.Status != status || next.ExitCode != 7 || !next.LogIncomplete {
				t.Fatalf("run=%+v err=%v", next, err)
			}
		})
	}
}

func TestIntegrationCommandJournalGapMarksIncompleteAndKeepsLaterOutput(t *testing.T) {
	run := projectionTestRun(t)
	health := runLogHealthFor(context.Background(), run, nil)
	follower := newDetachFollower(func(chunk domain.ExecChunk) { health.appendOutput(nil, run, run.LogsPath, chunk) }, nil)
	follower.consume(domain.ExecChunk{Text: `{"t":"gap","from":0,"to":10}` + "\n" + `{"t":"out","s":"stdout","text":"after gap","offset":10}` + "\n"})
	entries := readProjectionLog(t, run)
	if !health.incomplete || len(entries) != 3 || entries[1].Type != runlog.EntryTruncated || !entries[1].SourceGap || entries[2].Data != "after gap" {
		t.Fatalf("incomplete=%v entries=%+v", health.incomplete, entries)
	}
}
