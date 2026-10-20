package runs

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestControllerStopInteractiveRunChoosesEOFOrCancelFromTurnState(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prompt   bool
		turn     bool
		wantKind RunAttachInputKind
		wantStop bool
	}{
		{name: "idle prompt closes normally", prompt: true, wantKind: RunAttachInputStdinEOF, wantStop: true},
		{name: "running prompt falls back to context cancellation", prompt: true, turn: true, wantStop: false},
		{name: "command session falls back to supervisor cancellation", wantStop: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewInteractiveSessionManager()
			session, err := manager.Create("run-1")
			if err != nil {
				t.Fatal(err)
			}
			if tc.prompt {
				session.enablePromptTurns()
			}
			if tc.turn {
				session.beginInitialPromptTurn()
			}
			controller := &Controller{interactiveSessions: manager}
			handled, err := controller.StopInteractiveRun(context.Background(), "run-1", "audit only")
			if err != nil || handled != tc.wantStop {
				t.Fatalf("StopInteractiveRun() handled=%v err=%v, want %v/nil", handled, err, tc.wantStop)
			}
			if !tc.wantStop {
				return
			}
			input, err := session.Receive()()
			if err != nil {
				t.Fatal(err)
			}
			if input.Kind != tc.wantKind {
				t.Fatalf("stop input = %#v, want kind=%q", input, tc.wantKind)
			}
		})
	}
}

func TestInteractiveSessionRemainsActiveWhileQueuedPromptTurnsRemain(t *testing.T) {
	session := NewInteractiveSession("run-1")
	session.enablePromptTurns()
	session.beginInitialPromptTurn()
	if err := session.Send(context.Background(), RunAttachInput{Kind: RunAttachInputHumanMessage, Text: "queued"}); err != nil {
		t.Fatal(err)
	}
	session.completePromptTurn()
	if enabled, running := session.promptTurnRunning(); !enabled || !running {
		t.Fatalf("after one turn completed enabled=%v running=%v, want true/true while a queued turn remains", enabled, running)
	}
	session.completePromptTurn()
	if enabled, running := session.promptTurnRunning(); !enabled || running {
		t.Fatalf("after all turns completed enabled=%v running=%v, want true/false", enabled, running)
	}
}

func TestControllerStopPromptAttachAfterCompletedTurnEndsSucceeded(t *testing.T) {
	controller, configDB, runtime := newTestRunAttachController(t, nil)
	interaction := newWrapperInteraction()
	runtime.interactionOverride = interaction

	requests := make(chan RunAttachInput, 1)
	requests <- RunAttachInput{Kind: RunAttachInputStart, Mode: RunAttachModePrompt, Request: RunAgentRequest{
		ProjectID: "project-1", AgentName: "worker", Prompt: "remember this",
	}}
	turns := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- controller.RunProjectCommandAttach(context.Background(), func() (RunAttachInput, error) {
			input, ok := <-requests
			if !ok {
				return RunAttachInput{}, context.Canceled
			}
			return input, nil
		}, func(output RunAttachOutput) error {
			if output.Kind == RunAttachOutputAgentTurnCompleted {
				turns <- output.Run.RunID
			}
			return nil
		})
	}()

	var runID string
	select {
	case runID = <-turns:
	case <-time.After(5 * time.Second):
		t.Fatal("prompt attach did not complete its turn")
	}
	handled, err := controller.StopInteractiveRun(context.Background(), runID, "conversation closed")
	if err != nil || !handled {
		t.Fatalf("StopInteractiveRun() handled=%v err=%v, want true/nil", handled, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("prompt attach returned error after graceful stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prompt attach did not finish after graceful stop")
	}
	stored, err := configDB.GetProjectRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.ProjectRunStatusSucceeded {
		t.Fatalf("run status = %q, want %q", stored.Status, domain.ProjectRunStatusSucceeded)
	}
}

type turnHoldInteraction struct {
	frames      chan driverpkg.RuntimeOutputFrame
	sent        chan driverpkg.RuntimeInputFrame
	turnStarted chan struct{}
	recvErr     chan error
	finishOnce  sync.Once
}

func newTurnHoldInteraction() *turnHoldInteraction {
	return &turnHoldInteraction{
		frames:      make(chan driverpkg.RuntimeOutputFrame, 8),
		sent:        make(chan driverpkg.RuntimeInputFrame, 8),
		turnStarted: make(chan struct{}),
		recvErr:     make(chan error, 1),
	}
}

func (i *turnHoldInteraction) Send(frame driverpkg.RuntimeInputFrame) error {
	i.sent <- frame
	for _, line := range strings.Split(strings.TrimRight(string(frame.Data), "\n"), "\n") {
		var decoded struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			return err
		}
		switch decoded.Type {
		case "start":
			i.frames <- driverpkg.RuntimeOutputFrame{Type: driverpkg.RuntimeOutputStarted}
		case "human_message":
			close(i.turnStarted)
		case "cancel":
			i.finish("cancelled")
		case "eof":
			i.finish("eof")
		}
	}
	return nil
}

func (i *turnHoldInteraction) finish(reason string) {
	i.finishOnce.Do(func() {
		i.frames <- driverpkg.RuntimeOutputFrame{Type: driverpkg.RuntimeOutputStdout, Data: []byte(`{"type":"result","stopReason":"` + reason + `","finalText":"held"}` + "\n")}
		i.frames <- driverpkg.RuntimeOutputFrame{Type: driverpkg.RuntimeOutputResult, Result: &driverpkg.RuntimeResult{OperationID: "run-attach", Success: true}}
	})
}

func (i *turnHoldInteraction) CloseSend() error {
	i.finish("eof")
	return nil
}

func (i *turnHoldInteraction) Recv() (driverpkg.RuntimeOutputFrame, error) {
	select {
	case err := <-i.recvErr:
		return driverpkg.RuntimeOutputFrame{}, err
	case frame, ok := <-i.frames:
		if !ok {
			return driverpkg.RuntimeOutputFrame{}, io.EOF
		}
		return frame, nil
	}
}

func (*turnHoldInteraction) Wait() (driverpkg.RuntimeResult, error) {
	return driverpkg.RuntimeResult{Success: true}, nil
}

func TestControllerStopPromptAttachDuringTurnEndsCanceled(t *testing.T) {
	controller, configDB, runtime := newTestRunAttachController(t, nil)
	interaction := newTurnHoldInteraction()
	runtime.interactionOverride = interaction

	requests := make(chan RunAttachInput, 1)
	requests <- RunAttachInput{Kind: RunAttachInputStart, Mode: RunAttachModePrompt, Request: RunAgentRequest{
		ProjectID: "project-1", AgentName: "worker", Prompt: "long task",
	}}
	execCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- controller.RunProjectCommandAttach(execCtx, func() (RunAttachInput, error) {
			input, ok := <-requests
			if !ok {
				return RunAttachInput{}, io.EOF
			}
			return input, nil
		}, func(output RunAttachOutput) error {
			if output.Kind == RunAttachOutputStarted {
				started <- output.Run.RunID
			}
			return nil
		})
	}()

	var runID string
	select {
	case runID = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("prompt attach did not start")
	}
	select {
	case <-interaction.turnStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("prompt turn did not start")
	}
	handled, err := controller.StopInteractiveRun(context.Background(), runID, "user stopped")
	if err != nil || handled {
		t.Fatalf("StopInteractiveRun() handled=%v err=%v, want false/nil for a running turn", handled, err)
	}
	cancel()
	interaction.recvErr <- io.ErrClosedPipe
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("prompt attach returned error after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prompt attach did not finish after cancel")
	}
	stored, err := configDB.GetProjectRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.ProjectRunStatusCanceled {
		t.Fatalf("run status = %q, want %q", stored.Status, domain.ProjectRunStatusCanceled)
	}
}
