package runs

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type sessionTestInteraction struct{ closed bool }

func (s *sessionTestInteraction) Send(driverpkg.RuntimeInputFrame) error { return nil }
func (s *sessionTestInteraction) CloseSend() error                       { s.closed = true; return nil }
func (s *sessionTestInteraction) Recv() (driverpkg.RuntimeOutputFrame, error) {
	return driverpkg.RuntimeOutputFrame{}, errors.New("eof")
}
func (s *sessionTestInteraction) Wait() (driverpkg.RuntimeResult, error) {
	return driverpkg.RuntimeResult{}, nil
}

func TestIntegrationInteractiveSessionManagerAttachAndClose(t *testing.T) {
	m := NewInteractiveSessionManager()
	s, err := m.Create("run-1")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &sessionTestInteraction{}
	if err := s.BindRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Runtime(); err != nil || got == nil {
		t.Fatalf("runtime = %v, %v", got, err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if got, err := m.Get("run-1"); err != nil || got != s {
		t.Fatalf("Get() = %v, %v", got, err)
	}
	if _, err := m.Create("run-1"); err == nil {
		t.Fatal("duplicate Create() returned nil error")
	}
	release, err := s.AcquireInput()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := s.Send(context.Background(), RunAttachInput{Kind: RunAttachInputHumanMessage, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	input, err := s.Receive()()
	if err != nil {
		t.Fatal(err)
	}
	if got := input.Text; got != "hello" {
		t.Fatalf("input = %q", got)
	}
	if _, err := s.AcquireInput(); !errors.Is(err, ErrInteractiveSessionAttached) {
		t.Fatalf("attach error = %v", err)
	}
	if err := m.Remove("run-1", InteractiveSessionCompleted); err != nil {
		t.Fatal(err)
	}
	if !runtime.closed {
		t.Fatal("session close did not close runtime input")
	}
	if err := s.Send(context.Background(), RunAttachInput{}); !errors.Is(err, ErrInteractiveSessionClosed) {
		t.Fatalf("send error = %v", err)
	}
}

func TestInteractiveSessionManagerAttachMissing(t *testing.T) {
	m := NewInteractiveSessionManager()
	if _, err := m.Attach("missing"); !errors.Is(err, ErrInteractiveSessionNotFound) {
		t.Fatalf("attach error = %v", err)
	}
}

func TestInteractiveSessionManagerBindRuntimeClosesInteractionOnFailure(t *testing.T) {
	m := NewInteractiveSessionManager()
	missing := &sessionTestInteraction{}
	if _, err := m.BindRuntime("missing", missing); !errors.Is(err, ErrInteractiveSessionNotFound) {
		t.Fatalf("BindRuntime() missing error = %v", err)
	}
	if !missing.closed {
		t.Fatal("missing session did not close interaction")
	}

	s, err := m.Create("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BindRuntime(&sessionTestInteraction{}); err != nil {
		t.Fatal(err)
	}
	duplicate := &sessionTestInteraction{}
	if _, err := m.BindRuntime("run-1", duplicate); err == nil {
		t.Fatal("BindRuntime() duplicate returned nil error")
	}
	if !duplicate.closed {
		t.Fatal("duplicate runtime did not close interaction")
	}
}

func TestInteractiveSessionSendRejectsEveryTerminalState(t *testing.T) {
	states := []InteractiveSessionState{
		InteractiveSessionCompleted,
		InteractiveSessionCanceled,
		InteractiveSessionFailed,
	}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			s := NewInteractiveSession("run-1")
			s.Close(state)
			if err := s.Send(context.Background(), RunAttachInput{}); !errors.Is(err, ErrInteractiveSessionClosed) {
				t.Fatalf("Send() error = %v, want %v", err, ErrInteractiveSessionClosed)
			}
		})
	}
}

func TestInteractiveSessionAttachmentCloseInterruptsBackpressure(t *testing.T) {
	m := NewInteractiveSessionManager()
	s, err := m.Create("run-1")
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := m.Attach("run-1")
	if err != nil {
		t.Fatal(err)
	}
	for range 32 {
		if err := s.Send(context.Background(), RunAttachInput{}); err != nil {
			t.Fatal(err)
		}
	}
	sendDone := make(chan error, 1)
	go func() { sendDone <- attachment.Send(context.Background(), RunAttachInput{}) }()
	attachment.Close()
	select {
	case err := <-sendDone:
		if !errors.Is(err, ErrInteractiveSessionClosed) {
			t.Fatalf("blocked Send() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("attachment Close() did not interrupt blocked Send()")
	}
}

func TestStartRunAttachInputForwarderReleasesInitialLeaseBeforeResume(t *testing.T) {
	m := NewInteractiveSessionManager()
	s, err := m.Create("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	release, err := s.AcquireInput()
	if err != nil {
		t.Fatal(err)
	}
	released := startRunAttachInputForwarder(context.Background(), func() (RunAttachInput, error) {
		return RunAttachInput{}, io.EOF
	}, s, AttachDisconnectCancel, release)
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("initial input lease was not released")
	}
	attachment, err := m.Attach("run-1")
	if err != nil {
		t.Fatalf("Attach() after initial input ended error = %v", err)
	}
	attachment.Close()
}

func TestRunAttachInputForwarderReleasesLeaseWhenBackpressuredConnectionCancels(t *testing.T) {
	m := NewInteractiveSessionManager()
	s, err := m.Create("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	release, err := s.AcquireInput()
	if err != nil {
		t.Fatal(err)
	}
	inputCtx, cancelInput := context.WithCancel(context.Background())
	received := make(chan struct{}, 33)
	released := startRunAttachInputForwarder(inputCtx, func() (RunAttachInput, error) {
		received <- struct{}{}
		return RunAttachInput{Kind: RunAttachInputHumanMessage}, nil
	}, s, AttachDisconnectCancel, release)
	for range 33 {
		<-received
	}
	cancelInput()
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("connection cancellation did not release the backpressured input lease")
	}
	attachment, err := m.Attach("run-1")
	if err != nil {
		t.Fatalf("Attach() after backpressured disconnect error = %v", err)
	}
	attachment.Close()
}

func TestInteractiveSessionInputLeaseTracksDetachAndResume(t *testing.T) {
	s := NewInteractiveSession("run-1")
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	release, err := s.AcquireInput()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireInput(); !errors.Is(err, ErrInteractiveSessionAttached) {
		t.Fatalf("second lease error = %v", err)
	}
	release()
	if s.State() != InteractiveSessionDetached {
		t.Fatalf("state = %q", s.State())
	}
	release, err = s.AcquireInput()
	if err != nil {
		t.Fatal(err)
	}
	if s.State() != InteractiveSessionRunning {
		t.Fatalf("resumed state = %q", s.State())
	}
	release()
}

func TestInteractiveSessionManagerCreateAttachedReservesInitialInputLease(t *testing.T) {
	m := NewInteractiveSessionManager()
	s, release, err := m.CreateAttached("run-1")
	if err != nil {
		t.Fatalf("CreateAttached() error = %v", err)
	}
	if s.State() != InteractiveSessionRunning {
		t.Fatalf("created session state = %q, want %q", s.State(), InteractiveSessionRunning)
	}
	if _, err := m.Attach("run-1"); !errors.Is(err, ErrInteractiveSessionAttached) {
		t.Fatalf("concurrent Attach() error = %v, want %v", err, ErrInteractiveSessionAttached)
	}
	release()
	attachment, err := m.Attach("run-1")
	if err != nil {
		t.Fatalf("Attach() after initial release error = %v", err)
	}
	attachment.Close()
}

func TestInteractiveSessionClosesSlowOutputSubscriber(t *testing.T) {
	s := NewInteractiveSession("run-1")
	outputs, unsubscribe, err := s.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	for i := 0; i < 33; i++ {
		s.Publish(RunAttachOutput{})
	}
	for range outputs {
	}
}

func TestInteractiveRunOutputSenderStopsWritingAfterDetach(t *testing.T) {
	s := NewInteractiveSession("run-1")
	sendCalls := 0
	sender := newInteractiveRunOutputSender(s, AttachDisconnectDetach, func(RunAttachOutput) error {
		sendCalls++
		return errors.New("stream closed")
	})
	if err := sender(RunAttachOutput{Kind: RunAttachOutputData}); err != nil {
		t.Fatalf("first detached send error = %v", err)
	}
	if err := sender(RunAttachOutput{Kind: RunAttachOutputData}); err != nil {
		t.Fatalf("second detached send error = %v", err)
	}
	if sendCalls != 1 {
		t.Fatalf("send calls = %d, want 1", sendCalls)
	}

	wantErr := errors.New("write failed")
	attached := newInteractiveRunOutputSender(s, AttachDisconnectCancel, func(RunAttachOutput) error { return wantErr })
	if err := attached(RunAttachOutput{}); !errors.Is(err, wantErr) {
		t.Fatalf("attached send error = %v, want %v", err, wantErr)
	}
}

func TestIntegrationControllerAttachesExistingInteractiveSession(t *testing.T) {
	m := NewInteractiveSessionManager()
	s, err := m.Create("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	requests := []RunAttachInput{{Kind: RunAttachInputStart, RunID: "run-1"}, {Kind: RunAttachInputHumanMessage, Text: "continue"}}
	i := 0
	receive := func() (RunAttachInput, error) {
		if i == len(requests) {
			return RunAttachInput{}, errors.New("done")
		}
		request := requests[i]
		i++
		return request, nil
	}
	controller := NewController(ControllerDependencies{InteractiveSessions: m})
	controller.configDB = &fakeControllerStore{runs: map[string]domain.ProjectRunRecord{"run-1": {RunID: "run-1", Status: domain.ProjectRunStatusRunning}}}
	err = controller.RunProjectCommandAttach(context.Background(), receive, func(RunAttachOutput) error { return nil })
	if err == nil || err.Error() != "done" {
		t.Fatalf("attach error = %v", err)
	}
	if got := (<-s.input).Text; got != "continue" {
		t.Fatalf("input = %q", got)
	}
}

func TestE2EControllerResumesAndCompletesInteractiveSession(t *testing.T) {
	m := NewInteractiveSessionManager()
	s, err := m.Create("run-1")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &sessionTestInteraction{}
	if err := s.BindRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if got, err := m.Get("run-1"); err != nil || got != s {
		t.Fatalf("Get() = %v, %v", got, err)
	}
	if _, err := m.Create("run-1"); err == nil {
		t.Fatal("duplicate Create() returned nil error")
	}
	outputs, unsubscribe, err := s.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()

	requests := make(chan RunAttachInput, 2)
	requests <- RunAttachInput{Kind: RunAttachInputStart, RunID: "run-1"}
	requests <- RunAttachInput{Kind: RunAttachInputHumanMessage, Text: "continue"}
	receive := func() (RunAttachInput, error) {
		request, ok := <-requests
		if !ok {
			return RunAttachInput{}, io.EOF
		}
		return request, nil
	}
	controller := NewController(ControllerDependencies{InteractiveSessions: m})
	controller.configDB = &fakeControllerStore{runs: map[string]domain.ProjectRunRecord{
		"run-1": {RunID: "run-1", Status: domain.ProjectRunStatusRunning},
	}}
	forwardedOutputs := make(chan RunAttachOutput, 1)
	attachDone := make(chan error, 1)
	go func() {
		attachDone <- controller.RunProjectCommandAttach(context.Background(), receive, func(output RunAttachOutput) error {
			forwardedOutputs <- output
			return nil
		})
	}()
	select {
	case err := <-attachDone:
		t.Fatalf("RunProjectCommandAttach() failed before forwarding input: %v", err)
	case got := <-s.input:
		if got.Text != "continue" {
			t.Fatalf("forwarded input = %q, want %q", got.Text, "continue")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for forwarded input")
	}

	wantOutput := RunAttachOutput{Kind: RunAttachOutputAgentEvent}
	s.Publish(wantOutput)
	if got := <-outputs; got.Kind != wantOutput.Kind {
		t.Fatalf("published output kind = %q, want %q", got.Kind, wantOutput.Kind)
	}
	if got := <-forwardedOutputs; got.Kind != wantOutput.Kind {
		t.Fatalf("attached output kind = %q, want %q", got.Kind, wantOutput.Kind)
	}
	close(requests)
	if err := <-attachDone; err != nil {
		t.Fatalf("RunProjectCommandAttach() error = %v", err)
	}
	unsubscribe()
	slowOutputs, unsubscribeSlow, err := s.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribeSlow()
	for range 33 {
		s.Publish(RunAttachOutput{Kind: RunAttachOutputData})
	}
	for range slowOutputs {
	}
	if err := m.Remove("run-1", InteractiveSessionCompleted); err != nil {
		t.Fatal(err)
	}
	if !runtime.closed {
		t.Fatal("completed session did not close runtime input")
	}
	if s.State() != InteractiveSessionCompleted {
		t.Fatalf("state = %q, want %q", s.State(), InteractiveSessionCompleted)
	}
	if _, err := m.Get("run-1"); !errors.Is(err, ErrInteractiveSessionNotFound) {
		t.Fatalf("Get() after completion error = %v", err)
	}
	if _, err := m.Attach("run-1"); !errors.Is(err, ErrInteractiveSessionNotFound) {
		t.Fatalf("Attach() after completion error = %v", err)
	}
	if err := s.Send(context.Background(), RunAttachInput{}); !errors.Is(err, ErrInteractiveSessionClosed) {
		t.Fatalf("Send() after completion error = %v", err)
	}
	if _, _, err := s.Subscribe(); !errors.Is(err, ErrInteractiveSessionClosed) {
		t.Fatalf("Subscribe() after completion error = %v", err)
	}
	if _, err := s.AcquireInput(); !errors.Is(err, ErrInteractiveSessionClosed) {
		t.Fatalf("AcquireInput() after completion error = %v", err)
	}
	if _, err := s.Receive()(); !errors.Is(err, ErrInteractiveSessionClosed) {
		t.Fatalf("Receive() after completion error = %v", err)
	}

	queued, err := m.Create("run-2")
	if err != nil {
		t.Fatal(err)
	}
	for range 32 {
		if err := queued.Send(context.Background(), RunAttachInput{Kind: RunAttachInputHumanMessage}); err != nil {
			t.Fatal(err)
		}
	}
	queuedSend := make(chan error, 1)
	go func() { queuedSend <- queued.Send(context.Background(), RunAttachInput{}) }()
	if _, err := queued.Receive()(); err != nil {
		t.Fatal(err)
	}
	if err := <-queuedSend; err != nil {
		t.Fatalf("Send() after queue space became available error = %v", err)
	}
	if err := m.Remove("run-2", InteractiveSessionCanceled); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("missing", InteractiveSessionCanceled); !errors.Is(err, ErrInteractiveSessionNotFound) {
		t.Fatalf("Remove() missing error = %v", err)
	}

	startOnly := func(runID string) RunAttachReceiver {
		return func() (RunAttachInput, error) {
			return RunAttachInput{Kind: RunAttachInputStart, RunID: runID}, nil
		}
	}
	if err := NewController(ControllerDependencies{InteractiveSessions: m}).RunProjectCommandAttach(
		context.Background(), startOnly("run-1"), func(RunAttachOutput) error { return nil },
	); err == nil || err.Error() != "config store is required" {
		t.Fatalf("attach without store error = %v", err)
	}
	controller.configDB = &fakeControllerStore{runs: map[string]domain.ProjectRunRecord{
		"terminal": {RunID: "terminal", Status: domain.ProjectRunStatusSucceeded},
		"missing":  {RunID: "missing", Status: domain.ProjectRunStatusRunning},
	}}
	if err := controller.RunProjectCommandAttach(context.Background(), startOnly("terminal"), func(RunAttachOutput) error { return nil }); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("attach terminal run error = %v", err)
	}
	if err := controller.RunProjectCommandAttach(context.Background(), startOnly("missing"), func(RunAttachOutput) error { return nil }); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("attach missing session error = %v", err)
	}

	duplicate, err := m.Create("duplicate")
	if err != nil {
		t.Fatal(err)
	}
	if err := duplicate.Start(); err != nil {
		t.Fatal(err)
	}
	controller.configDB = &fakeControllerStore{runs: map[string]domain.ProjectRunRecord{
		"duplicate": {RunID: "duplicate", Status: domain.ProjectRunStatusRunning},
	}}
	frames := make(chan RunAttachInput, 2)
	frames <- RunAttachInput{Kind: RunAttachInputStart, RunID: "duplicate"}
	frames <- RunAttachInput{Kind: RunAttachInputStart, RunID: "duplicate"}
	if err := controller.RunProjectCommandAttach(context.Background(), func() (RunAttachInput, error) {
		return <-frames, nil
	}, func(RunAttachOutput) error { return nil }); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("duplicate start frame error = %v", err)
	}
	if err := m.Remove("duplicate", InteractiveSessionCanceled); err != nil {
		t.Fatal(err)
	}

	attached, err := m.Create("attached")
	if err != nil {
		t.Fatal(err)
	}
	if err := attached.Start(); err != nil {
		t.Fatal(err)
	}
	release, err := attached.AcquireInput()
	if err != nil {
		t.Fatal(err)
	}
	controller.configDB = &fakeControllerStore{runs: map[string]domain.ProjectRunRecord{
		"attached": {RunID: "attached", Status: domain.ProjectRunStatusRunning},
	}}
	if err := controller.RunProjectCommandAttach(context.Background(), startOnly("attached"), func(RunAttachOutput) error { return nil }); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("attach busy session error = %v", err)
	}
	release()
	oldAttachment, err := m.Attach("attached")
	if err != nil {
		t.Fatal(err)
	}
	oldAttachment.Close()
	newAttachment, err := m.Attach("attached")
	if err != nil {
		t.Fatal(err)
	}
	if err := oldAttachment.Send(context.Background(), RunAttachInput{}); !errors.Is(err, ErrInteractiveSessionClosed) {
		t.Fatalf("stale attachment Send() error = %v", err)
	}
	newAttachment.Close()
	if err := m.Remove("attached", InteractiveSessionCanceled); err != nil {
		t.Fatal(err)
	}

	closed, err := m.Create("closed")
	if err != nil {
		t.Fatal(err)
	}
	closed.Close(InteractiveSessionFailed)
	controller.configDB = &fakeControllerStore{runs: map[string]domain.ProjectRunRecord{
		"closed": {RunID: "closed", Status: domain.ProjectRunStatusRunning},
	}}
	if err := controller.RunProjectCommandAttach(context.Background(), startOnly("closed"), func(RunAttachOutput) error { return nil }); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("attach closed session error = %v", err)
	}
	if err := m.Remove("closed", InteractiveSessionFailed); err != nil {
		t.Fatal(err)
	}

	sendDiscard := func(RunAttachOutput) error { return nil }
	invalidStreams := []struct {
		name    string
		receive RunAttachReceiver
		send    RunAttachSender
		want    error
	}{
		{name: "nil receiver", send: sendDiscard},
		{name: "nil sender", receive: startOnly("")},
		{name: "EOF before start", receive: func() (RunAttachInput, error) { return RunAttachInput{}, io.EOF }, send: sendDiscard, want: ErrInvalidRequest},
		{name: "receive failure", receive: func() (RunAttachInput, error) { return RunAttachInput{}, context.Canceled }, send: sendDiscard, want: context.Canceled},
		{name: "non-start first frame", receive: func() (RunAttachInput, error) { return RunAttachInput{Kind: RunAttachInputHumanMessage}, nil }, send: sendDiscard, want: ErrInvalidRequest},
		{name: "missing mode", receive: startOnly(""), send: sendDiscard, want: ErrInvalidRequest},
		{name: "missing command", receive: func() (RunAttachInput, error) {
			return RunAttachInput{Kind: RunAttachInputStart, Mode: RunAttachModeCommand}, nil
		}, send: sendDiscard, want: ErrInvalidRequest},
		{name: "missing prompt", receive: func() (RunAttachInput, error) {
			return RunAttachInput{Kind: RunAttachInputStart, Mode: RunAttachModePrompt}, nil
		}, send: sendDiscard, want: ErrInvalidRequest},
	}
	for _, test := range invalidStreams {
		t.Run(test.name, func(t *testing.T) {
			err := controller.RunProjectCommandAttach(context.Background(), test.receive, test.send)
			if test.want == nil {
				if err == nil || err.Error() != "run attach stream is required" {
					t.Fatalf("RunProjectCommandAttach() error = %v", err)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("RunProjectCommandAttach() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRunAttachInputForwarderEndsInputOnClientHalfClose(t *testing.T) {
	s := NewInteractiveSession("run-1")
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	release, err := s.AcquireInput()
	if err != nil {
		t.Fatal(err)
	}
	released := startRunAttachInputForwarder(context.Background(), func() (RunAttachInput, error) {
		return RunAttachInput{}, io.EOF
	}, s, AttachDisconnectCancel, release)
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("input lease was not released")
	}
	select {
	case input := <-s.input:
		if input.Kind != RunAttachInputStdinEOF {
			t.Fatalf("half-close forwarded %q, want %q", input.Kind, RunAttachInputStdinEOF)
		}
	case <-time.After(time.Second):
		t.Fatal("half-close did not forward stdin EOF to the session")
	}
}

func TestRunAttachInputForwarderKeepsInputOpenForDetachedClients(t *testing.T) {
	s := NewInteractiveSession("run-1")
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	release, err := s.AcquireInput()
	if err != nil {
		t.Fatal(err)
	}
	released := startRunAttachInputForwarder(context.Background(), func() (RunAttachInput, error) {
		return RunAttachInput{}, io.EOF
	}, s, AttachDisconnectDetach, release)
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("input lease was not released")
	}
	select {
	case input := <-s.input:
		t.Fatalf("detached half-close forwarded input %q", input.Kind)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRunAttachInputForwarderForwardsClientEOFOnlyOnce(t *testing.T) {
	s := NewInteractiveSession("run-1")
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	release, err := s.AcquireInput()
	if err != nil {
		t.Fatal(err)
	}
	sent := false
	released := startRunAttachInputForwarder(context.Background(), func() (RunAttachInput, error) {
		if sent {
			return RunAttachInput{}, io.EOF
		}
		sent = true
		return RunAttachInput{Kind: RunAttachInputStdinEOF}, nil
	}, s, AttachDisconnectCancel, release)
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("input lease was not released")
	}
	select {
	case input := <-s.input:
		if input.Kind != RunAttachInputStdinEOF {
			t.Fatalf("forwarded %q, want %q", input.Kind, RunAttachInputStdinEOF)
		}
	case <-time.After(time.Second):
		t.Fatal("explicit stdin EOF was not forwarded to the session")
	}
	select {
	case extra := <-s.input:
		t.Fatalf("half-close after explicit EOF forwarded a duplicate %q", extra.Kind)
	case <-time.After(50 * time.Millisecond):
	}
}
