//go:build linux && cgo && microsandboxcgo

package driver

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

func TestMicrosandboxInteractionCapabilities(t *testing.T) {
	caps := (&microsandboxRuntime{}).InteractionCapabilities()
	if !caps.NativeExec || !caps.Stdin || !caps.StdinEOF || !caps.TTY || !caps.Resize || !caps.Signal {
		t.Fatalf("InteractionCapabilities() = %#v, want native command terminal capabilities", caps)
	}
	if caps.WrapperStream || caps.Artifacts || caps.AgentTurns {
		t.Fatalf("InteractionCapabilities() = %#v, want unsupported wrapper and agent capabilities disabled", caps)
	}
}

func TestMicrosandboxCommandInteractionProjectsEvents(t *testing.T) {
	runeBytes := []byte("中")
	handle := &fakeMicrosandboxInteractionExec{events: []*microsandbox.ExecEvent{
		{Kind: microsandbox.ExecEventStarted},
		{Kind: microsandbox.ExecEventStdout, Data: runeBytes[:1]},
		{Kind: microsandbox.ExecEventStdout, Data: runeBytes[1:]},
		{Kind: microsandbox.ExecEventStderr, Data: []byte("warn\n")},
		{Kind: microsandbox.ExecEventExited, ExitCode: 3},
		{Kind: microsandbox.ExecEventDone},
	}}
	stdin := &fakeMicrosandboxInteractionStdin{}
	released := 0
	interaction := newTestMicrosandboxInteraction(handle, stdin, false, func() { released++ })

	frames := receiveAllMicrosandboxInteractionFrames(t, interaction)
	result, err := interaction.Wait()
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result.ExitCode != 3 || result.Success {
		t.Fatalf("Wait() result = %#v, want exit 3 failure", result)
	}
	want := []RuntimeOutputFrameType{RuntimeOutputStarted, RuntimeOutputStdout, RuntimeOutputStderr, RuntimeOutputResult}
	if got := microsandboxInteractionFrameTypes(frames); !reflect.DeepEqual(got, want) {
		t.Fatalf("frame types = %#v, want %#v", got, want)
	}
	if string(frames[1].Data) != "中" || string(frames[2].Data) != "warn\n" {
		t.Fatalf("output frames = %#v", frames)
	}
	if stdin.closeCalls != 1 || handle.closeCalls != 1 || released != 1 {
		t.Fatalf("cleanup calls = stdin:%d handle:%d release:%d, want 1 each", stdin.closeCalls, handle.closeCalls, released)
	}
}

func TestMicrosandboxCommandInteractionMergesTTYOutput(t *testing.T) {
	handle := &fakeMicrosandboxInteractionExec{events: []*microsandbox.ExecEvent{
		{Kind: microsandbox.ExecEventStarted},
		{Kind: microsandbox.ExecEventStderr, Data: []byte("terminal")},
		{Kind: microsandbox.ExecEventExited, ExitCode: 0},
		{Kind: microsandbox.ExecEventDone},
	}}
	interaction := newTestMicrosandboxInteraction(handle, nil, true, nil)
	frames := receiveAllMicrosandboxInteractionFrames(t, interaction)
	if len(frames) != 3 || frames[1].Type != RuntimeOutputStdout || string(frames[1].Data) != "terminal" {
		t.Fatalf("TTY frames = %#v, want merged stdout", frames)
	}
}

// TestMicrosandboxCommandInteractionCountsSplitSeccompWarning is the regression
// test for decoded-chunk counting: the lower layer reports one warning, but the
// stream splits it across two stderr events, so neither event contains the
// whole message. The interaction must still report exactly one security fact,
// matching the streaming exec path.
func TestMicrosandboxCommandInteractionCountsSplitSeccompWarning(t *testing.T) {
	handle := &fakeMicrosandboxInteractionExec{events: []*microsandbox.ExecEvent{
		{Kind: microsandbox.ExecEventStarted},
		{Kind: microsandbox.ExecEventStderr, Data: []byte("libcontainer::process::init::process: seccomp not available, unable to set se")},
		{Kind: microsandbox.ExecEventStderr, Data: []byte("ccomp privileges!\n")},
		{Kind: microsandbox.ExecEventExited, ExitCode: 0},
		{Kind: microsandbox.ExecEventDone},
	}}
	interaction := newTestMicrosandboxInteraction(handle, nil, false, nil)
	receiveAllMicrosandboxInteractionFrames(t, interaction)
	result, err := interaction.Wait()
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result.SecurityFacts == nil {
		t.Fatal("SecurityFacts = nil, want the split seccomp warning to be reported")
	}
	if result.SecurityFacts.SeccompUnavailable != 1 || result.SecurityFacts.NoNewPrivilegesUnavailable != 0 {
		t.Fatalf("SecurityFacts = %+v, want exactly one seccomp report", result.SecurityFacts)
	}
}

// TestMicrosandboxCommandInteractionCountsUnterminatedSeccompWarning covers the
// final stderr line that never receives a newline: it must still be counted.
func TestMicrosandboxCommandInteractionCountsUnterminatedSeccompWarning(t *testing.T) {
	handle := &fakeMicrosandboxInteractionExec{events: []*microsandbox.ExecEvent{
		{Kind: microsandbox.ExecEventStarted},
		{Kind: microsandbox.ExecEventStderr, Data: []byte("seccomp not available, unable to enforce no_new_privileges!")},
		{Kind: microsandbox.ExecEventExited, ExitCode: 0},
		{Kind: microsandbox.ExecEventDone},
	}}
	interaction := newTestMicrosandboxInteraction(handle, nil, false, nil)
	receiveAllMicrosandboxInteractionFrames(t, interaction)
	result, err := interaction.Wait()
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if result.SecurityFacts == nil || result.SecurityFacts.NoNewPrivilegesUnavailable != 1 {
		t.Fatalf("SecurityFacts = %+v, want one no_new_privileges report", result.SecurityFacts)
	}
}

func TestMicrosandboxCommandInteractionBoundsDrainAfterExit(t *testing.T) {
	handle := &fakeMicrosandboxInteractionExec{
		events: []*microsandbox.ExecEvent{
			{Kind: microsandbox.ExecEventStarted},
			{Kind: microsandbox.ExecEventExited, ExitCode: 0},
		},
		blockRecv: true,
	}
	interaction := newTestMicrosandboxInteractionWithDrain(handle, nil, false, nil, 10*time.Millisecond)

	frames := receiveAllMicrosandboxInteractionFrames(t, interaction)
	result, err := interaction.Wait()
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if !result.Success || result.ExitCode != 0 {
		t.Fatalf("Wait() result = %#v, want successful exit after bounded drain", result)
	}
	if got := microsandboxInteractionFrameTypes(frames); !reflect.DeepEqual(got, []RuntimeOutputFrameType{RuntimeOutputStarted, RuntimeOutputResult}) {
		t.Fatalf("frame types = %#v", got)
	}
	if handle.killCalls != 0 || handle.waitCalls != 0 || handle.closeCalls != 1 {
		t.Fatalf("handle calls = kill:%d wait:%d close:%d, want graceful close only", handle.killCalls, handle.waitCalls, handle.closeCalls)
	}
}

func TestMicrosandboxCommandInteractionBoundsMissingExitHeldByBackgroundOutputPipe(t *testing.T) {
	handle := &fakeMicrosandboxInteractionExec{
		events:    []*microsandbox.ExecEvent{{Kind: microsandbox.ExecEventStarted, PID: 42}},
		blockRecv: true,
	}
	probe := func(context.Context, uint32) (bool, error) { return false, nil }
	interaction := newTestMicrosandboxInteractionWithTiming(handle, nil, false, nil, 0, 10*time.Millisecond, probe)

	frames := receiveAllMicrosandboxInteractionFrames(t, interaction)
	result, err := interaction.Wait()
	if err == nil || !strings.Contains(err.Error(), "still holding the output pipes open") {
		t.Fatalf("Wait() error = %v, want bounded missing-exit failure", err)
	}
	if result.Success || result.ExitCode != -1 {
		t.Fatalf("Wait() result = %#v, want failed unknown exit", result)
	}
	if got := microsandboxInteractionFrameTypes(frames); !reflect.DeepEqual(got, []RuntimeOutputFrameType{RuntimeOutputStarted, RuntimeOutputError, RuntimeOutputResult}) {
		t.Fatalf("frame types = %#v", got)
	}
	if handle.killCalls != 0 || handle.waitCalls != 0 || handle.closeCalls != 1 {
		t.Fatalf("handle calls = kill:%d wait:%d close:%d, want graceful close only", handle.killCalls, handle.waitCalls, handle.closeCalls)
	}
}

func TestMicrosandboxCommandInteractionHandlesInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle := &fakeMicrosandboxInteractionExec{}
	stdin := &fakeMicrosandboxInteractionStdin{}
	interaction := &microsandboxCommandInteraction{
		ctx:         ctx,
		cancel:      cancel,
		handle:      handle,
		stdin:       stdin,
		attachStdin: true,
	}

	if err := interaction.Send(RuntimeInputFrame{Type: RuntimeInputStdin, Data: []byte("pwd\n")}); err != nil {
		t.Fatalf("Send(stdin) error = %v", err)
	}
	if err := interaction.Send(RuntimeInputFrame{Type: RuntimeInputResize, Rows: 40, Cols: 120}); err != nil {
		t.Fatalf("Send(resize) error = %v", err)
	}
	for _, signal := range []RuntimeSignal{RuntimeSignalInterrupt, RuntimeSignalTerminate, RuntimeSignalKill} {
		if err := interaction.Send(RuntimeInputFrame{Type: RuntimeInputSignal, Signal: signal}); err != nil {
			t.Fatalf("Send(%s) error = %v", signal, err)
		}
	}
	if err := interaction.CloseSend(); err != nil {
		t.Fatalf("CloseSend() error = %v", err)
	}
	if err := interaction.CloseSend(); err != nil {
		t.Fatalf("second CloseSend() error = %v", err)
	}

	if string(stdin.writes) != "pwd\n" || stdin.closeCalls != 1 {
		t.Fatalf("stdin = writes:%q closes:%d", stdin.writes, stdin.closeCalls)
	}
	if handle.resizeRows != 40 || handle.resizeCols != 120 {
		t.Fatalf("resize = %dx%d, want 40x120", handle.resizeRows, handle.resizeCols)
	}
	wantSignals := []int{int(syscall.SIGINT), int(syscall.SIGTERM), int(syscall.SIGKILL)}
	if !reflect.DeepEqual(handle.signals, wantSignals) {
		t.Fatalf("signals = %#v, want %#v", handle.signals, wantSignals)
	}
}

func TestMicrosandboxCommandInteractionCancellationTerminatesProcess(t *testing.T) {
	handle := &fakeMicrosandboxInteractionExec{blockRecv: true}
	stdin := &fakeMicrosandboxInteractionStdin{}
	released := 0
	interaction := newTestMicrosandboxInteraction(handle, stdin, false, func() { released++ })

	if err := interaction.Send(RuntimeInputFrame{Type: RuntimeInputCancel}); err != nil {
		t.Fatalf("Send(cancel) error = %v", err)
	}
	result, err := interaction.Wait()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want context.Canceled", err)
	}
	if result.ExitCode != -1 || result.Error == "" {
		t.Fatalf("Wait() result = %#v, want canceled result", result)
	}
	if handle.killCalls != 1 || handle.waitCalls != 1 || handle.closeCalls != 1 || stdin.closeCalls != 1 || released != 1 {
		t.Fatalf("cleanup calls = kill:%d wait:%d handle:%d stdin:%d release:%d", handle.killCalls, handle.waitCalls, handle.closeCalls, stdin.closeCalls, released)
	}
}

func TestMicrosandboxCommandInteractionRejectsInvalidControl(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interaction := &microsandboxCommandInteraction{ctx: ctx, cancel: cancel, handle: &fakeMicrosandboxInteractionExec{}}
	if err := interaction.Send(RuntimeInputFrame{Type: RuntimeInputSignal, Signal: "unknown"}); err == nil {
		t.Fatal("Send(unknown signal) returned nil error")
	}
	if err := interaction.Send(RuntimeInputFrame{Type: RuntimeInputResize, Rows: 1 << 16, Cols: 80}); err == nil {
		t.Fatal("Send(oversized resize) returned nil error")
	}
	if err := interaction.Send(RuntimeInputFrame{Type: RuntimeInputStdin, Data: []byte("x")}); !errors.Is(err, ErrRuntimeInteractionUnsupported) {
		t.Fatalf("Send(unattached stdin) error = %v, want unsupported", err)
	}
}

func TestMicrosandboxCommandInteractionPreservesCloseSendError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wantErr := errors.New("close stdin")
	stdin := &fakeMicrosandboxInteractionStdin{closeErr: wantErr}
	interaction := &microsandboxCommandInteraction{
		ctx:         ctx,
		cancel:      cancel,
		handle:      &fakeMicrosandboxInteractionExec{},
		stdin:       stdin,
		attachStdin: true,
	}
	for call := 1; call <= 2; call++ {
		if err := interaction.CloseSend(); !errors.Is(err, wantErr) {
			t.Fatalf("CloseSend() call %d error = %v, want %v", call, err, wantErr)
		}
	}
	if stdin.closeCalls != 1 {
		t.Fatalf("stdin close calls = %d, want 1", stdin.closeCalls)
	}
}

func newTestMicrosandboxInteraction(handle microsandboxInteractionExec, stdin microsandboxInteractionStdin, tty bool, release func()) *microsandboxCommandInteraction {
	return newTestMicrosandboxInteractionWithDrain(handle, stdin, tty, release, 0)
}

func newTestMicrosandboxInteractionWithDrain(handle microsandboxInteractionExec, stdin microsandboxInteractionStdin, tty bool, release func(), drainTime time.Duration) *microsandboxCommandInteraction {
	return newTestMicrosandboxInteractionWithTiming(handle, stdin, tty, release, drainTime, 0, nil)
}

func newTestMicrosandboxInteractionWithTiming(handle microsandboxInteractionExec, stdin microsandboxInteractionStdin, tty bool, release func(), drainTime, probeTime time.Duration, probe microsandboxExecLivenessProbe) *microsandboxCommandInteraction {
	ctx, cancel := context.WithCancel(context.Background())
	interaction := &microsandboxCommandInteraction{
		ctx:            ctx,
		cancel:         cancel,
		handle:         handle,
		stdin:          stdin,
		release:        release,
		operationID:    "operation-1",
		tty:            tty,
		attachStdin:    stdin != nil,
		startedAt:      time.Now(),
		exitDrainTime:  drainTime,
		probeAlive:     probe,
		firstProbeTime: probeTime,
		output:         make(chan RuntimeOutputFrame, 16),
		done:           make(chan struct{}),
	}
	go interaction.run()
	return interaction
}

func receiveAllMicrosandboxInteractionFrames(t *testing.T, interaction RuntimeInteraction) []RuntimeOutputFrame {
	t.Helper()
	var frames []RuntimeOutputFrame
	for {
		frame, err := interaction.Recv()
		if errors.Is(err, io.EOF) {
			return frames
		}
		if err != nil {
			t.Fatalf("Recv() error = %v", err)
		}
		frames = append(frames, frame)
	}
}

func microsandboxInteractionFrameTypes(frames []RuntimeOutputFrame) []RuntimeOutputFrameType {
	types := make([]RuntimeOutputFrameType, 0, len(frames))
	for _, frame := range frames {
		types = append(types, frame.Type)
	}
	return types
}

type fakeMicrosandboxInteractionExec struct {
	mu         sync.Mutex
	events     []*microsandbox.ExecEvent
	blockRecv  bool
	resizeRows uint16
	resizeCols uint16
	signals    []int
	killCalls  int
	waitCalls  int
	closeCalls int
}

func (h *fakeMicrosandboxInteractionExec) Recv(ctx context.Context) (*microsandbox.ExecEvent, error) {
	h.mu.Lock()
	if len(h.events) > 0 {
		event := h.events[0]
		h.events = h.events[1:]
		h.mu.Unlock()
		return event, nil
	}
	block := h.blockRecv
	h.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &microsandbox.ExecEvent{Kind: microsandbox.ExecEventDone}, nil
}

func (h *fakeMicrosandboxInteractionExec) Kill(context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.killCalls++
	return nil
}

func (h *fakeMicrosandboxInteractionExec) Wait(context.Context) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.waitCalls++
	return -1, nil
}

func (h *fakeMicrosandboxInteractionExec) Signal(_ context.Context, signal int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.signals = append(h.signals, signal)
	return nil
}

func (h *fakeMicrosandboxInteractionExec) Resize(_ context.Context, rows, cols uint16) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resizeRows = rows
	h.resizeCols = cols
	return nil
}

func (h *fakeMicrosandboxInteractionExec) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closeCalls++
	return nil
}

type fakeMicrosandboxInteractionStdin struct {
	writes     []byte
	closeCalls int
	closeErr   error
}

func (s *fakeMicrosandboxInteractionStdin) WriteCtx(_ context.Context, data []byte) (int, error) {
	s.writes = append(s.writes, data...)
	return len(data), nil
}

func (s *fakeMicrosandboxInteractionStdin) Close() error {
	s.closeCalls++
	return s.closeErr
}
