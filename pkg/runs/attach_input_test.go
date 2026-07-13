package runs

import (
	"context"
	"errors"
	"testing"
	"time"

	driverpkg "agent-compose/pkg/driver"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestAttachInputPumpsCloseRuntimeInputOnReceiveError(t *testing.T) {
	receiveErr := errors.New("stream reset")

	t.Run("command", func(t *testing.T) {
		interaction := &closingRuntimeInteraction{}
		pumpRunAttachInput(func() (*agentcomposev2.RunAttachRequest, error) {
			return nil, receiveErr
		}, interaction)
		if interaction.closeCalls != 1 {
			t.Fatalf("CloseSend calls = %d, want 1", interaction.closeCalls)
		}
	})

	t.Run("prompt", func(t *testing.T) {
		interaction := &closingRuntimeInteraction{}
		input := &promptWrapperInput{interaction: interaction}
		pumpRunPromptAttachInput(context.Background(), func() (*agentcomposev2.RunAttachRequest, error) {
			return nil, receiveErr
		}, input, nil, nil)
		if interaction.closeCalls != 1 {
			t.Fatalf("CloseSend calls = %d, want 1", interaction.closeCalls)
		}
		if len(interaction.sent) != 1 || string(interaction.sent[0].Data) != "{\"seq\":0,\"type\":\"eof\",\"v\":1}\n" {
			t.Fatalf("sent frames = %#v, want prompt EOF", interaction.sent)
		}
	})
}

func TestForwardPromptHumanMessageWaitsForTurnCompletion(t *testing.T) {
	interaction := &closingRuntimeInteraction{}
	input := &promptWrapperInput{interaction: interaction}
	turnReady := make(chan struct{}, 1)
	done := make(chan bool, 1)
	go func() {
		done <- forwardPromptHumanMessage(context.Background(), input, turnReady, "next", nil)
	}()

	select {
	case <-done:
		t.Fatal("human message was forwarded before the previous turn completed")
	default:
	}
	releasePromptTurn(turnReady)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("human message was not forwarded")
		}
	case <-time.After(time.Second):
		t.Fatal("human message remained blocked after turn completion")
	}
	if len(interaction.sent) != 1 || string(interaction.sent[0].Data) != "{\"message\":\"next\",\"seq\":0,\"type\":\"human_message\",\"v\":1}\n" {
		t.Fatalf("sent frames = %#v", interaction.sent)
	}
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
