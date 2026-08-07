package runs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	driverpkg "agent-compose/pkg/driver"
)

func TestAttachInputPumpsCloseRuntimeInputOnReceiveError(t *testing.T) {
	receiveErr := errors.New("stream reset")

	t.Run("command", func(t *testing.T) {
		interaction := &closingRuntimeInteraction{}
		pumpRunAttachInput(func() (RunAttachInput, error) {
			return RunAttachInput{}, receiveErr
		}, interaction)
		if interaction.closeCalls != 1 {
			t.Fatalf("CloseSend calls = %d, want 1", interaction.closeCalls)
		}
	})

	t.Run("prompt", func(t *testing.T) {
		interaction := &closingRuntimeInteraction{}
		input := &promptWrapperInput{interaction: interaction}
		pumpRunPromptAttachInput(context.Background(), func() (RunAttachInput, error) {
			return RunAttachInput{}, receiveErr
		}, input, nil, nil)
		if interaction.closeCalls != 1 {
			t.Fatalf("CloseSend calls = %d, want 1", interaction.closeCalls)
		}
		if len(interaction.sent) != 1 || string(interaction.sent[0].Data) != "{\"seq\":0,\"type\":\"eof\",\"v\":1}\n" {
			t.Fatalf("sent frames = %#v, want prompt EOF", interaction.sent)
		}
	})
}

func TestPromptAttachInputWaitsForCompletedTurnBeforeForwardingQueuedMessages(t *testing.T) {
	requests := make(chan RunAttachInput, 2)
	received := make(chan struct{}, 2)
	interaction := newObservedRuntimeInteraction()
	input := &promptWrapperInput{interaction: interaction}
	turnReady := make(chan struct{}, 1)
	done := make(chan struct{})

	go func() {
		defer close(done)
		pumpRunPromptAttachInput(context.Background(), func() (RunAttachInput, error) {
			req, ok := <-requests
			if !ok {
				return RunAttachInput{}, io.EOF
			}
			received <- struct{}{}
			return req, nil
		}, input, turnReady, nil)
	}()

	requests <- humanMessageAttachRequest("human-2")
	requests <- humanMessageAttachRequest("human-3")
	close(requests)
	<-received
	assertNoRuntimeInputFrame(t, interaction.sent)

	turnReady <- struct{}{}
	assertPromptRuntimeFrame(t, receiveRuntimeInputFrame(t, interaction.sent), "human_message", "human-2")
	<-received
	assertNoRuntimeInputFrame(t, interaction.sent)

	turnReady <- struct{}{}
	assertPromptRuntimeFrame(t, receiveRuntimeInputFrame(t, interaction.sent), "human_message", "human-3")
	assertPromptRuntimeFrame(t, receiveRuntimeInputFrame(t, interaction.sent), "eof", "")

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("prompt input pump did not exit after EOF")
	}
	if interaction.closeCallCount() != 1 {
		t.Fatalf("CloseSend calls = %d, want 1", interaction.closeCallCount())
	}
}

func TestPromptAttachInputCancellationUnblocksTurnWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	requests := make(chan RunAttachInput, 1)
	received := make(chan struct{}, 1)
	interaction := newObservedRuntimeInteraction()
	input := &promptWrapperInput{interaction: interaction}
	done := make(chan struct{})

	go func() {
		defer close(done)
		pumpRunPromptAttachInput(ctx, func() (RunAttachInput, error) {
			req := <-requests
			received <- struct{}{}
			return req, nil
		}, input, make(chan struct{}, 1), nil)
	}()

	requests <- humanMessageAttachRequest("queued")
	<-received
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("prompt input pump did not exit after cancellation")
	}
	assertNoRuntimeInputFrame(t, interaction.sent)
	if interaction.closeCallCount() != 1 {
		t.Fatalf("CloseSend calls = %d, want 1", interaction.closeCallCount())
	}
}

func humanMessageAttachRequest(message string) RunAttachInput {
	return RunAttachInput{Kind: RunAttachInputHumanMessage, Text: message}
}

func receiveRuntimeInputFrame(t *testing.T, frames <-chan driverpkg.RuntimeInputFrame) driverpkg.RuntimeInputFrame {
	t.Helper()
	select {
	case frame := <-frames:
		return frame
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runtime input frame")
	}
	return driverpkg.RuntimeInputFrame{}
}

func assertNoRuntimeInputFrame(t *testing.T, frames <-chan driverpkg.RuntimeInputFrame) {
	t.Helper()
	select {
	case frame := <-frames:
		t.Fatalf("unexpected runtime input frame: %s", frame.Data)
	default:
	}
}

func assertPromptRuntimeFrame(t *testing.T, frame driverpkg.RuntimeInputFrame, frameType, message string) {
	t.Helper()
	var payload struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(frame.Data, &payload); err != nil {
		t.Fatalf("decode runtime input frame: %v", err)
	}
	if payload.Type != frameType || payload.Message != message {
		t.Fatalf("runtime input frame = %#v, want type=%q message=%q", payload, frameType, message)
	}
}

type observedRuntimeInteraction struct {
	sent       chan driverpkg.RuntimeInputFrame
	closed     chan struct{}
	closeOnce  sync.Once
	mu         sync.Mutex
	closeCalls int
}

func newObservedRuntimeInteraction() *observedRuntimeInteraction {
	return &observedRuntimeInteraction{
		sent:   make(chan driverpkg.RuntimeInputFrame, 4),
		closed: make(chan struct{}),
	}
}

func (i *observedRuntimeInteraction) Send(frame driverpkg.RuntimeInputFrame) error {
	i.sent <- frame
	return nil
}

func (i *observedRuntimeInteraction) CloseSend() error {
	i.mu.Lock()
	i.closeCalls++
	i.mu.Unlock()
	i.closeOnce.Do(func() { close(i.closed) })
	return nil
}

func (*observedRuntimeInteraction) Recv() (driverpkg.RuntimeOutputFrame, error) {
	return driverpkg.RuntimeOutputFrame{}, errors.New("unused")
}

func (*observedRuntimeInteraction) Wait() (driverpkg.RuntimeResult, error) {
	return driverpkg.RuntimeResult{}, errors.New("unused")
}

func (i *observedRuntimeInteraction) closeCallCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.closeCalls
}

type closingRuntimeInteraction struct {
	sent       []driverpkg.RuntimeInputFrame
	closeCalls int
}

func (i *closingRuntimeInteraction) Send(frame driverpkg.RuntimeInputFrame) error {
	i.sent = append(i.sent, frame)
	return nil
}

func (i *closingRuntimeInteraction) CloseSend() error {
	i.closeCalls++
	return nil
}

func (*closingRuntimeInteraction) Recv() (driverpkg.RuntimeOutputFrame, error) {
	return driverpkg.RuntimeOutputFrame{}, errors.New("unused")
}

func (*closingRuntimeInteraction) Wait() (driverpkg.RuntimeResult, error) {
	return driverpkg.RuntimeResult{}, errors.New("unused")
}
