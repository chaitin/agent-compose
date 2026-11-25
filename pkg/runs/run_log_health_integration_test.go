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
	incomplete    bool
	checkpoint    string
	checkpointErr error
}

func (s *logHealthTestStore) MarkProjectRunLogIncomplete(context.Context, string) error {
	s.incomplete = true
	return nil
}
func (s *logHealthTestStore) SaveProjectRunExecutionCheckpoint(_ context.Context, _, checkpoint string) error {
	if s.checkpointErr != nil {
		return s.checkpointErr
	}
	s.incomplete, s.checkpoint = true, checkpoint
	return nil
}

func TestIntegrationAgentLogFailurePreservesOutcomeAndRecovery(t *testing.T) {
	for _, transport := range []string{"exec", "attach"} {
		for _, fault := range []string{"open", "append", "checkpoint"} {
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
					w, err := runLogWriter(run, nil)
					if err != nil {
						t.Fatal(err)
					}
					// Leave the cached writer closed to simulate an append I/O failure.
					if err := w.Close(); err != nil {
						t.Fatal(err)
					}
				case "checkpoint":
					if _, err := runLogWriter(run, nil); err != nil {
						t.Fatal(err)
					}
					// A directory at the checkpoint destination makes atomic publication fail.
					if err := os.Mkdir(run.LogsPath+".checkpoint.json", 0o755); err != nil {
						t.Fatal(err)
					}
				}
				store := &logHealthTestStore{projectorEventStore: projectorEventStore{keys: map[string]struct{}{}}}
				p := newAgentRunProjector(ctx, run, nil, run.LogsPath, nil)
				p.bindStore(store)
				if fault == "append" {
					if err := p.AppendOutput(domain.ExecChunk{Text: "warning"}); err != nil {
						t.Fatal(err)
					}
				}
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
				if !store.incomplete || store.checkpoint == "" {
					t.Fatalf("missing health/recovery: %+v", store)
				}
				run.LogIncomplete, run.ExecutionCheckpointJSON = true, store.checkpoint
				p = newAgentRunProjector(ctx, run, nil, run.LogsPath, nil)
				p.bindStore(store)
				resume, err := p.RestoreJournal()
				if err != nil || resume == nil || resume.Offset != offset || resume.Decoder.Pending == "" {
					t.Fatalf("resume=%+v err=%v", resume, err)
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
				run.ExecutionCheckpointJSON = store.checkpoint
				p = newAgentRunProjector(ctx, run, nil, run.LogsPath, nil)
				p.bindStore(store)
				restored, err := p.RestoreJournal()
				if err != nil || restored == nil || restored.Exit == nil || restored.ResultJSON == "" {
					t.Fatalf("lost confirmed exit: %+v %v", restored, err)
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
			})
		}
	}
}

func TestIntegrationAgentRecoveryFailureDoesNotAcknowledgeJournal(t *testing.T) {
	run := projectionTestRun(t)
	if err := os.Mkdir(run.LogsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	persistenceErr := errors.New("recovery database unavailable")
	store := &logHealthTestStore{checkpointErr: persistenceErr}
	p := newAgentRunProjector(context.Background(), run, nil, run.LogsPath, nil)
	p.bindStore(store)
	_, err := p.ProjectRuntimeOutput(domain.ExecChunk{Text: "hello", Journal: &domain.JournalPosition{End: 5}})
	if !errors.Is(err, persistenceErr) || store.checkpoint != "" || !store.incomplete {
		t.Fatalf("err=%v checkpoint=%s incomplete=%v", err, store.checkpoint, store.incomplete)
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
			next, _, err := terminalRunUpdate(run, completion, time.Unix(100, 0))
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
