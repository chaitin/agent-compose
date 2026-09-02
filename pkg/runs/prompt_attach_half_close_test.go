package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
)

// wrapperInteraction models agent-compose-runtime stream (see
// runtime/javascript/src/stream.ts): it answers each human message with agent
// events plus an agent_turn_completed frame, and only reports its result once
// input ends — either through an eof frame or because stdin was closed. A
// prompt attach that never ends the wrapper's input therefore never sees a
// result frame, which is the hang reported in issue #659.
type wrapperInteraction struct {
	frames chan driverpkg.RuntimeOutputFrame

	mu       sync.Mutex
	seq      int
	turns    int
	finished bool
	replies  []string
}

func newWrapperInteraction() *wrapperInteraction {
	return &wrapperInteraction{frames: make(chan driverpkg.RuntimeOutputFrame, 32)}
}

func (i *wrapperInteraction) Send(frame driverpkg.RuntimeInputFrame) error {
	if frame.Type != driverpkg.RuntimeInputStdin {
		return nil
	}
	for _, line := range strings.Split(strings.TrimRight(string(frame.Data), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var decoded struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			return err
		}
		switch decoded.Type {
		case "start":
			i.emitLine(`{"v":1,"seq":%d,"type":"started","provider":"codex","sessionId":"thread-1"}`)
		case "human_message":
			i.runTurn(decoded.Message)
		case "eof":
			i.finish("eof")
		case "cancel":
			i.finish("cancelled")
		}
	}
	return nil
}

// CloseSend models the guest process losing stdin: the wrapper's readline loop
// ends and it reports whatever it has, exactly like the trailing emit in
// runStreamCommand.
func (i *wrapperInteraction) CloseSend() error {
	i.finish("eof")
	return nil
}

func (i *wrapperInteraction) Recv() (driverpkg.RuntimeOutputFrame, error) {
	frame, ok := <-i.frames
	if !ok {
		return driverpkg.RuntimeOutputFrame{}, io.EOF
	}
	return frame, nil
}

func (*wrapperInteraction) Wait() (driverpkg.RuntimeResult, error) {
	return driverpkg.RuntimeResult{Success: true}, nil
}

func (i *wrapperInteraction) runTurn(message string) {
	i.mu.Lock()
	i.turns++
	turn := i.turns
	reply := fmt.Sprintf("已记住 %s", message)
	i.replies = append(i.replies, reply)
	i.mu.Unlock()
	i.emitLine(`{"v":1,"seq":%d,"type":"agent_event","event":{"type":"item.completed","item":{"id":"m` + fmt.Sprint(turn) + `","type":"agent_message","text":` + quoteJSON(reply+"\n") + `}}}`)
	i.emitLine(`{"v":1,"seq":%d,"type":"agent_turn_completed","provider":"codex","sessionId":"thread-1","finalText":` + quoteJSON(reply+"\n") + `,"finalTextSource":"provider_message"}`)
}

func (i *wrapperInteraction) finish(stopReason string) {
	i.mu.Lock()
	if i.finished {
		i.mu.Unlock()
		return
	}
	i.finished = true
	transcript := strings.Join(i.replies, "\n")
	final := ""
	if len(i.replies) > 0 {
		final = i.replies[len(i.replies)-1] + "\n"
	}
	i.mu.Unlock()
	i.emitLine(`{"v":1,"seq":%d,"type":"result","provider":"codex","sessionId":"thread-1","stopReason":"` + stopReason + `","finalText":` + quoteJSON(final) + `,"finalTextSource":"provider_message","transcript":` + quoteJSON(transcript) + `}`)
	i.frames <- driverpkg.RuntimeOutputFrame{Type: driverpkg.RuntimeOutputResult, Result: &driverpkg.RuntimeResult{OperationID: "run-attach", Success: true}}
}

func (i *wrapperInteraction) emitLine(format string) {
	i.mu.Lock()
	seq := i.seq
	i.seq++
	i.mu.Unlock()
	i.frames <- promptRuntimeStdoutFrame(fmt.Sprintf(format, seq))
}

func quoteJSON(text string) string {
	encoded, err := json.Marshal(text)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// TestRunsControllerPromptAttachFinishesWhenClientOnlyHalfCloses reproduces
// issue #659: `exec --prompt` sends its start frame and then half-closes the
// request without any further frame. The runtime wrapper keeps the turn open
// until its input ends, so the attach has to translate that half-close into
// stdin EOF; otherwise the prompt response stream never completes and the CLI
// blocks until its RPC deadline.
func TestRunsControllerPromptAttachFinishesWhenClientOnlyHalfCloses(t *testing.T) {
	controller, _, runtime := newTestRunAttachController(t, nil)
	interaction := newWrapperInteraction()
	runtime.interactionOverride = interaction

	sentStart := false
	receive := func() (RunAttachInput, error) {
		if !sentStart {
			sentStart = true
			return RunAttachInput{Kind: RunAttachInputStart, Mode: RunAttachModePrompt, Request: RunAgentRequest{
				ProjectID: "project-1", AgentName: "worker", Prompt: "请记住验证码 KUNPENG-920",
			}}, nil
		}
		// connect reports the client's CloseRequest as io.EOF.
		return RunAttachInput{}, io.EOF
	}

	var mu sync.Mutex
	var outputs []RunAttachOutput
	done := make(chan error, 1)
	go func() {
		done <- controller.RunProjectCommandAttach(context.Background(), receive, func(output RunAttachOutput) error {
			mu.Lock()
			defer mu.Unlock()
			outputs = append(outputs, output)
			return nil
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunProjectCommandAttach returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("prompt attach never completed after the client half-closed the request (issue #659)")
	}

	mu.Lock()
	defer mu.Unlock()
	var sawTurn, sawResult bool
	for _, output := range outputs {
		switch output.Kind {
		case RunAttachOutputAgentTurnCompleted:
			sawTurn = true
		case RunAttachOutputResult:
			sawResult = true
			if !output.Success {
				t.Fatalf("prompt attach result was not successful: %#v", output)
			}
		}
	}
	if !sawTurn {
		t.Fatalf("prompt attach did not report a completed turn: %#v", outputs)
	}
	if !sawResult {
		t.Fatalf("prompt attach did not report a result: %#v", outputs)
	}
}
