//go:build linux && cgo && microsandboxcgo

package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strings"
	"sync"
	"syscall"
	"time"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

type microsandboxInteractionExec interface {
	Recv(context.Context) (*microsandbox.ExecEvent, error)
	Kill(context.Context) error
	Wait(context.Context) (int, error)
	Signal(context.Context, int) error
	Resize(context.Context, uint16, uint16) error
	Close() error
}

type microsandboxInteractionStdin interface {
	WriteCtx(context.Context, []byte) (int, error)
	Close() error
}

type microsandboxCommandInteraction struct {
	ctx            context.Context
	cancel         context.CancelFunc
	handle         microsandboxInteractionExec
	stdin          microsandboxInteractionStdin
	release        func()
	operationID    string
	tty            bool
	attachStdin    bool
	startedAt      time.Time
	exitDrainTime  time.Duration
	probeAlive     microsandboxExecLivenessProbe
	firstProbeTime time.Duration
	stdoutDecoder  utf8StreamDecoder
	stderrDecoder  utf8StreamDecoder

	output chan RuntimeOutputFrame
	done   chan struct{}

	inputMu       sync.Mutex
	closeSendOnce sync.Once
	closeSendErr  error
	cleanupOnce   sync.Once
	result        RuntimeResult
	err           error
}

func (r *microsandboxRuntime) InteractionCapabilities() RuntimeInteractionCapabilities {
	return RuntimeInteractionCapabilities{
		NativeExec: true,
		Stdin:      true,
		StdinEOF:   true,
		TTY:        true,
		Resize:     true,
		Signal:     true,
	}
}

func (r *microsandboxRuntime) OpenInteraction(ctx context.Context, session *Sandbox, vmState VMState, spec RuntimeStartSpec) (RuntimeInteraction, error) {
	rows, cols, err := r.validateMicrosandboxInteractionSpec(spec)
	if err != nil {
		return nil, err
	}

	childCtx, cancel := context.WithCancel(ctx)
	if spec.TimeoutMs > 0 {
		childCtx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutMs)*time.Millisecond)
	}
	if err := r.ensureReady(childCtx); err != nil {
		cancel()
		return nil, err
	}

	name := r.sandboxName(session, vmState)
	sandbox, err := r.connectSandbox(childCtx, session, vmState, true)
	if err != nil {
		cancel()
		return nil, err
	}
	release := func() { r.closeSandboxHandle(sandbox) }
	if err := r.ensureDirectoryOnlyGuestSandboxBootstrap(childCtx, sandbox, session, name); err != nil {
		cancel()
		release()
		return nil, err
	}

	execSpec := ExecSpecFromRuntimeStartSpec(spec)
	options := r.execOptions(childCtx, execSpec)
	if spec.AttachStdin {
		options = append(options, microsandbox.WithExecStdinPipe())
	}
	if spec.TTY {
		options = append(options, microsandbox.WithExecTTY(true))
	}
	handle, err := sandbox.ExecStream(childCtx, execSpec.Command, execSpec.Args, options...)
	if err != nil {
		cancel()
		release()
		return nil, err
	}
	var stdin microsandboxInteractionStdin
	if spec.AttachStdin {
		stdin = handle.TakeStdin()
		if stdin == nil {
			cleanupErr := cleanupMicrosandboxInteractionStart(childCtx, handle, nil, release)
			cancel()
			return nil, errors.Join(fmt.Errorf("microsandbox interaction stdin pipe is unavailable"), cleanupErr)
		}
	}
	if rows != 0 || cols != 0 {
		if err := handle.Resize(childCtx, rows, cols); err != nil {
			cleanupErr := cleanupMicrosandboxInteractionStart(childCtx, handle, stdin, release)
			cancel()
			return nil, errors.Join(fmt.Errorf("resize microsandbox interaction terminal: %w", err), cleanupErr)
		}
	}

	interaction := &microsandboxCommandInteraction{
		ctx:            childCtx,
		cancel:         cancel,
		handle:         handle,
		stdin:          stdin,
		release:        release,
		operationID:    spec.OperationID,
		tty:            spec.TTY,
		attachStdin:    spec.AttachStdin,
		startedAt:      time.Now(),
		exitDrainTime:  microsandboxExecExitIdleGracePeriod,
		probeAlive:     microsandboxExecLivenessProbeFor(sandbox),
		firstProbeTime: microsandboxExecExitIdleGracePeriod,
		output:         make(chan RuntimeOutputFrame, 64),
		done:           make(chan struct{}),
	}
	go interaction.run()
	return interaction, nil
}

func (r *microsandboxRuntime) validateMicrosandboxInteractionSpec(spec RuntimeStartSpec) (uint16, uint16, error) {
	if err := r.InteractionCapabilities().ValidateStartSpec(RuntimeDriverMicrosandbox, spec); err != nil {
		return 0, 0, err
	}
	if normalizeRuntimeOperationKind(spec.Kind) != RuntimeOperationCommand {
		return 0, 0, NewRuntimeInteractionUnsupportedError(RuntimeDriverMicrosandbox, spec, RuntimeCapabilityNativeExec, "microsandbox native attach only supports command interactions")
	}
	command := RuntimeCommandSpec{}
	if spec.Command != nil {
		command = *spec.Command
	}
	if strings.TrimSpace(command.Command) == "" {
		return 0, 0, fmt.Errorf("microsandbox interaction command is required")
	}
	return microsandboxTerminalSize(spec.Rows, spec.Cols)
}

func microsandboxTerminalSize(rows, cols uint32) (uint16, uint16, error) {
	if rows > math.MaxUint16 || cols > math.MaxUint16 {
		return 0, 0, fmt.Errorf("microsandbox terminal size exceeds %d rows or columns", uint32(math.MaxUint16))
	}
	return uint16(rows), uint16(cols), nil
}

func cleanupMicrosandboxInteractionStart(ctx context.Context, handle microsandboxInteractionExec, stdin microsandboxInteractionStdin, release func()) error {
	var stdinErr error
	if stdin != nil {
		stdinErr = stdin.Close()
	}
	terminationErr := terminateMicrosandboxExec(ctx, handle)
	closeErr := handle.Close()
	if release != nil {
		release()
	}
	return errors.Join(stdinErr, terminationErr, closeErr)
}

func (i *microsandboxCommandInteraction) Send(frame RuntimeInputFrame) error {
	switch frame.Type {
	case RuntimeInputStdin:
		if !i.attachStdin || i.stdin == nil {
			return ErrRuntimeInteractionUnsupported
		}
		if len(frame.Data) == 0 {
			return nil
		}
		i.inputMu.Lock()
		defer i.inputMu.Unlock()
		if i.ctx.Err() != nil {
			return i.ctx.Err()
		}
		_, err := i.stdin.WriteCtx(i.ctx, frame.Data)
		return err
	case RuntimeInputStdinEOF:
		return i.CloseSend()
	case RuntimeInputResize:
		rows, cols, err := microsandboxTerminalSize(frame.Rows, frame.Cols)
		if err != nil {
			return err
		}
		if rows == 0 && cols == 0 {
			return nil
		}
		return i.handle.Resize(i.ctx, rows, cols)
	case RuntimeInputSignal:
		signal, err := microsandboxRuntimeSignal(frame.Signal)
		if err != nil {
			return err
		}
		return i.handle.Signal(i.ctx, signal)
	case RuntimeInputCancel:
		i.cancel()
		return nil
	default:
		return ErrRuntimeInteractionUnsupported
	}
}

func (i *microsandboxCommandInteraction) CloseSend() error {
	i.closeSendOnce.Do(func() {
		i.inputMu.Lock()
		defer i.inputMu.Unlock()
		if i.stdin != nil {
			i.closeSendErr = i.stdin.Close()
		}
	})
	return i.closeSendErr
}

func (i *microsandboxCommandInteraction) Recv() (RuntimeOutputFrame, error) {
	frame, ok := <-i.output
	if !ok {
		return RuntimeOutputFrame{}, io.EOF
	}
	return frame, nil
}

func (i *microsandboxCommandInteraction) Wait() (RuntimeResult, error) {
	<-i.done
	return i.result, i.err
}

func (i *microsandboxCommandInteraction) run() {
	defer close(i.done)
	defer close(i.output)
	defer i.cleanup()

	state := i.consumeOutput()
	i.flushOutput()
	if state.err != nil && !state.processGone && (i.ctx.Err() != nil || !state.sawExit) {
		terminationErr := terminateMicrosandboxExec(i.ctx, i.handle)
		state.err = execTerminationResultError(RuntimeDriverMicrosandbox, i.operationID, state.err, terminationErr)
	}
	i.finish(state)
}

type microsandboxInteractionRunState struct {
	exitCode    int
	pid         uint32
	sawExit     bool
	processGone bool
	err         error
}

func (i *microsandboxCommandInteraction) consumeOutput() microsandboxInteractionRunState {
	state := microsandboxInteractionRunState{exitCode: -1}
	receiveCtx, cancelReceive := context.WithCancel(i.ctx)
	received := receiveMicrosandboxExecEvents(receiveCtx, i.handle.Recv)
	defer func() {
		cancelReceive()
		for range received {
		}
	}()
	drain := newMicrosandboxInteractionExitDrain(i.exitDrainTime)
	defer drain.stop()
	probe := newMicrosandboxInteractionExitDrain(i.firstProbeTime)
	defer probe.stop()

	for state.err == nil {
		select {
		case <-i.ctx.Done():
			state.err = i.ctx.Err()
			return state
		case <-drain.wait:
			slog.Warn(
				"microsandbox interaction stream timed out waiting for done after process exit; closing handle",
				"operation_id", i.operationID,
				"exit_code", state.exitCode,
				"drain_window", drain.duration,
			)
			return state
		case <-probe.wait:
			gone, probeErr := microsandboxExecProcessGone(i.ctx, i.probeAlive, state.pid)
			if probeErr != nil {
				slog.Warn("failed to probe microsandbox interaction process liveness; continuing to wait", "operation_id", i.operationID, "pid", state.pid, "error", probeErr)
				probe.duration = microsandboxExecSilenceProbeInterval
				probe.reset()
				continue
			}
			if !gone {
				probe.duration = microsandboxExecSilenceProbeInterval
				probe.reset()
				continue
			}
			slog.Warn("microsandbox interaction process is gone but the stream never reported its exit; closing handle", "operation_id", i.operationID, "pid", state.pid)
			state.processGone = true
			state.err = fmt.Errorf("microsandbox interaction process %d exited without reporting its status; a process it started is still holding the output pipes open", state.pid)
			return state
		case result, ok := <-received:
			if !ok {
				if !state.sawExit {
					state.err = fmt.Errorf("microsandbox interaction stream ended without reporting a process exit status")
				}
				return state
			}
			if result.err != nil {
				if i.ctx.Err() != nil {
					state.err = i.ctx.Err()
				} else {
					state.err = result.err
				}
				return state
			}
			if i.projectOutputEvent(result.event, &state, drain, probe) {
				return state
			}
		}
	}
	return state
}

func (i *microsandboxCommandInteraction) projectOutputEvent(event *microsandbox.ExecEvent, state *microsandboxInteractionRunState, drain, probe *microsandboxInteractionExitDrain) bool {
	if event == nil || event.Kind == microsandbox.ExecEventDone {
		if !state.sawExit {
			state.err = fmt.Errorf("microsandbox interaction stream ended without reporting a process exit status")
		}
		return true
	}
	switch event.Kind {
	case microsandbox.ExecEventStarted:
		state.pid = event.PID
		i.emit(RuntimeOutputFrame{Type: RuntimeOutputStarted, StartedAt: i.startedAt})
		if i.probeAlive != nil && state.pid != 0 {
			probe.reset()
		}
	case microsandbox.ExecEventStdout:
		i.emitBytes(event.Data, StdioStdout)
		if state.sawExit {
			drain.reset()
		} else if i.probeAlive != nil && state.pid != 0 {
			probe.reset()
		}
	case microsandbox.ExecEventStderr:
		i.emitBytes(event.Data, StdioStderr)
		if state.sawExit {
			drain.reset()
		} else if i.probeAlive != nil && state.pid != 0 {
			probe.reset()
		}
	case microsandbox.ExecEventExited:
		state.exitCode = event.ExitCode
		state.sawExit = true
		probe.stop()
		drain.reset()
	case microsandbox.ExecEventFailed:
		state.err = formatMicrosandboxExecFailure(event.Failure)
		return true
	case microsandbox.ExecEventStdinError:
		i.emitBytes([]byte(formatMicrosandboxExecFailure(event.Failure).Error()+"\n"), StdioStderr)
	}
	return false
}

func (i *microsandboxCommandInteraction) finish(state microsandboxInteractionRunState) {
	i.result = RuntimeResult{
		OperationID: i.operationID,
		ExitCode:    state.exitCode,
		Success:     state.err == nil && state.exitCode == 0,
		StartedAt:   i.startedAt,
		CompletedAt: time.Now(),
	}
	if state.err != nil {
		i.err = state.err
		i.result.Error = state.err.Error()
		i.emit(RuntimeOutputFrame{Type: RuntimeOutputError, Error: &RuntimeError{Message: state.err.Error()}})
	}
	i.emit(RuntimeOutputFrame{Type: RuntimeOutputResult, Result: &i.result})
}

func (i *microsandboxCommandInteraction) emitBytes(data []byte, stream StdioStream) {
	decoder := &i.stdoutDecoder
	if NormalizeStdioStream(stream) == StdioStderr && !i.tty {
		decoder = &i.stderrDecoder
	} else {
		stream = StdioStdout
	}
	i.emitText(decoder.Write(data), stream)
}

func (i *microsandboxCommandInteraction) flushOutput() {
	i.emitText(i.stdoutDecoder.Finish(), StdioStdout)
	stream := StdioStderr
	if i.tty {
		stream = StdioStdout
	}
	i.emitText(i.stderrDecoder.Finish(), stream)
}

func (i *microsandboxCommandInteraction) emitText(text string, stream StdioStream) {
	if text == "" {
		return
	}
	frameType := RuntimeOutputStdout
	if NormalizeStdioStream(stream) == StdioStderr {
		frameType = RuntimeOutputStderr
	}
	i.emit(RuntimeOutputFrame{Type: frameType, Data: []byte(text)})
}

func (i *microsandboxCommandInteraction) emit(frame RuntimeOutputFrame) {
	select {
	case i.output <- frame:
	case <-i.ctx.Done():
	}
}

func (i *microsandboxCommandInteraction) cleanup() {
	i.cleanupOnce.Do(func() {
		if err := i.CloseSend(); err != nil {
			slog.Warn("failed to close microsandbox interaction stdin", "operation_id", i.operationID, "error", err)
		}
		if err := i.handle.Close(); err != nil {
			slog.Warn("failed to close microsandbox interaction exec handle", "operation_id", i.operationID, "error", err)
		}
		if i.release != nil {
			i.release()
		}
		i.cancel()
	})
}

func microsandboxRuntimeSignal(signal RuntimeSignal) (int, error) {
	switch signal {
	case RuntimeSignalInterrupt:
		return int(syscall.SIGINT), nil
	case RuntimeSignalTerminate:
		return int(syscall.SIGTERM), nil
	case RuntimeSignalKill:
		return int(syscall.SIGKILL), nil
	default:
		return 0, fmt.Errorf("unsupported microsandbox runtime signal %q", signal)
	}
}
