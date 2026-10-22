package runs

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// Stdin frame types understood by the guest supervisor (see
// runtime/javascript/src/detach/stdin-frames.ts).
const (
	detachStdinData   = 'D'
	detachStdinEOF    = 'E'
	detachStdinSignal = 'S'
)

func encodeDetachStdinFrame(kind byte, payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	frame[0] = kind
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload))) //nolint:gosec // stdin chunks are far below 4 GiB.
	copy(frame[5:], payload)
	return frame
}

// detachedInteractionOptions describes an interactive task behind the guest
// supervisor and how to follow it.
type detachedInteractionOptions struct {
	Task        DetachedTask
	Interaction InteractionRuntime
	OperationID string
	Origin      string
	// RawCommand runs Task.TaskArgs as a command line rather than a runtime
	// subcommand.
	RawCommand bool
}

// startDetachedInteraction starts an interactive task behind the guest
// supervisor with a socket-fed stdin, then opens the attach interaction that
// carries its input and output.
func startDetachedInteraction(ctx context.Context, opts detachedInteractionOptions) (driverpkg.RuntimeInteraction, error) {
	startSpec := execution.BuildDetachStartSpec(opts.Task.Config, opts.Task.ExecutionID, execution.DetachStartOptions{TaskTimeout: opts.Task.TaskTimeout, Stdin: true, RawCommand: opts.RawCommand}, opts.Task.TaskArgs)
	startSpec.Env = opts.Task.Env
	if result, err := opts.Task.Runtime.ExecStream(ctx, opts.Task.Sandbox, opts.Task.VMState, startSpec, nil); err != nil || result.ExitCode != 0 {
		if detachIsShutdown(ctx) {
			return nil, ErrRunDisconnected
		}
		return nil, detachControlExecErr(result, err, "start detached interactive execution")
	}
	return openDetachedInteraction(ctx, opts)
}

// openDetachedInteraction attaches to an interactive task started by
// startDetachedInteraction, possibly by an earlier daemon.
//
// The returned interaction looks like a direct one to the prompt and command
// loops: stdout/stderr frames carry the task's own output (journal records
// past the cursor), a result frame carries its exit, and input frames reach
// its stdin. ctx is the run context. A user stop or timeout (ctx cancelled)
// is handed to the supervisor; a daemon shutdown only drops the attach, and
// Recv then reports ErrRunDisconnected while the task keeps running.
func openDetachedInteraction(ctx context.Context, opts detachedInteractionOptions) (driverpkg.RuntimeInteraction, error) {
	task := opts.Task
	attachCtx, cancelAttach := context.WithCancelCause(context.WithoutCancel(ctx))
	spec := driverpkg.RuntimeStartSpec{
		OperationID: opts.OperationID,
		Kind:        driverpkg.RuntimeOperationCommand,
		Origin:      opts.Origin,
		Command: &driverpkg.RuntimeCommandSpec{
			Command: "agent-compose-runtime",
			Args: []string{
				"attach",
				"--execution-id", task.ExecutionID,
				"--from", fmt.Sprintf("%d", task.Cursor),
				"--state-root", task.Config.GuestStateRoot,
				"--stdin",
			},
			Env: task.Env,
			Cwd: task.Config.GuestWorkspacePath,
		},
		Cwd:         task.Config.GuestWorkspacePath,
		Env:         task.Env,
		AttachStdin: true,
	}
	underlying, err := opts.Interaction.OpenInteraction(attachCtx, task.Sandbox, task.VMState, spec)
	if err != nil {
		cancelAttach(nil)
		if detachIsShutdown(ctx) {
			return nil, ErrRunDisconnected
		}
		return nil, err
	}
	d := &detachedInteraction{
		underlying:   underlying,
		task:         task,
		runCtx:       ctx,
		attachCtx:    attachCtx,
		cancelAttach: cancelAttach,
		follower:     newDetachFollower(nil, task.PersistCursor),
		watchDone:    make(chan struct{}),
	}
	d.follower.forwardFrom = task.Cursor
	d.follower.cursor = task.Cursor
	go d.watch()
	return d, nil
}

type detachedInteraction struct {
	underlying   driverpkg.RuntimeInteraction
	task         DetachedTask
	runCtx       context.Context
	attachCtx    context.Context
	cancelAttach context.CancelCauseFunc
	follower     *detachFollower
	watchDone    chan struct{}

	sendMu sync.Mutex
	closed bool

	// Recv state; Recv is called from one goroutine. ended is also read by
	// CloseSend from the input side.
	pending []driverpkg.RuntimeOutputFrame
	result  *driverpkg.RuntimeResult
	ended   atomic.Bool
}

// watch turns run-context cancellation into either a disconnect (daemon
// shutdown) or a stop request to the supervisor (user stop, timeout).
func (d *detachedInteraction) watch() {
	defer close(d.watchDone)
	var shutdown <-chan struct{}
	if signal, ok := DaemonShutdown(d.runCtx); ok {
		shutdown = signal
	}
	select {
	case <-d.attachCtx.Done():
		return
	case <-shutdown:
		d.cancelAttach(ErrRunDisconnected)
		return
	case <-d.runCtx.Done():
	}
	if detachIsShutdown(d.runCtx) {
		d.cancelAttach(ErrRunDisconnected)
		return
	}
	reason := detachStopReason(d.runCtx)
	stopCtx, cancelStop := context.WithTimeout(context.WithoutCancel(d.runCtx), detachStopTimeout)
	defer cancelStop()
	stopSpec := execution.BuildDetachStopSpec(d.task.Config, d.task.ExecutionID, reason)
	stopSpec.Env = d.task.Env
	stopResult, stopErr := d.task.Runtime.ExecStream(stopCtx, d.task.Sandbox, d.task.VMState, stopSpec, nil)
	if stopErr != nil || stopResult.ExitCode != 0 {
		stopErr = detachControlExecErr(stopResult, stopErr, "detached execution stop request")
		terminateCtx, cancelTerminate := context.WithTimeout(context.WithoutCancel(d.runCtx), detachStopTimeout)
		terminateErr := terminateDetachedProcessTree(terminateCtx, d.task)
		cancelTerminate()
		if terminateErr != nil {
			stopErr = errors.Join(stopErr, terminateErr)
		}
		d.cancelAttach(stopErr)
	}
	// Otherwise the supervisor stops the task and the attach delivers its exit.
}

func (d *detachedInteraction) disconnected() bool {
	return errors.Is(context.Cause(d.attachCtx), ErrRunDisconnected)
}

func (d *detachedInteraction) Send(frame driverpkg.RuntimeInputFrame) error {
	var encoded []byte
	switch frame.Type {
	case driverpkg.RuntimeInputStdin:
		encoded = encodeDetachStdinFrame(detachStdinData, frame.Data)
	case driverpkg.RuntimeInputStdinEOF:
		encoded = encodeDetachStdinFrame(detachStdinEOF, nil)
	case driverpkg.RuntimeInputSignal:
		encoded = encodeDetachStdinFrame(detachStdinSignal, []byte(strings.TrimSpace(string(frame.Signal))))
	case driverpkg.RuntimeInputCancel:
		encoded = encodeDetachStdinFrame(detachStdinSignal, []byte("SIGTERM"))
	default:
		// No TTY behind the supervisor, so resize has nothing to act on.
		return nil
	}
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	if d.closed {
		return io.ErrClosedPipe
	}
	// Once the daemon is going away nothing more reaches the task: input
	// sent while the run is torn down (an input pump ending the session as it
	// closes) would otherwise end a task that must keep running.
	if d.disconnected() || detachIsShutdown(d.runCtx) {
		return ErrRunDisconnected
	}
	return d.underlying.Send(driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputStdin, Data: encoded})
}

// CloseSend ends the task's input, unless the daemon is shutting down: then
// the attach is just dropped so the next daemon can keep feeding the task.
func (d *detachedInteraction) CloseSend() error {
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	if !d.disconnected() && !detachIsShutdown(d.runCtx) && !d.ended.Load() {
		_ = d.underlying.Send(driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputStdin, Data: encodeDetachStdinFrame(detachStdinEOF, nil)})
	}
	return d.underlying.CloseSend()
}

func (d *detachedInteraction) Recv() (driverpkg.RuntimeOutputFrame, error) {
	for {
		if len(d.pending) > 0 {
			frame := d.pending[0]
			d.pending = d.pending[1:]
			return frame, nil
		}
		if d.ended.Load() {
			return driverpkg.RuntimeOutputFrame{}, io.EOF
		}
		frame, err := d.underlying.Recv()
		if err != nil {
			return driverpkg.RuntimeOutputFrame{}, d.endWithoutExit(err)
		}
		switch frame.Type {
		case driverpkg.RuntimeOutputStdout:
			d.consumeAttachOutput(frame.Data)
		case driverpkg.RuntimeOutputStderr:
			// The attach process's own stderr, kept to explain a failure.
			d.follower.consume(domain.ExecChunk{Stream: domain.StdioStderr, Text: string(frame.Data)})
		case driverpkg.RuntimeOutputResult:
			if d.result == nil {
				return driverpkg.RuntimeOutputFrame{}, d.endWithoutExit(io.EOF)
			}
		default:
			return frame, nil
		}
	}
}

// consumeAttachOutput turns attach records into the task's own output frames
// and, at the exit record, its result.
func (d *detachedInteraction) consumeAttachOutput(data []byte) {
	var out []domain.ExecChunk
	d.follower.writer = func(chunk domain.ExecChunk) { out = append(out, chunk) }
	d.follower.consume(domain.ExecChunk{Stream: domain.StdioStdout, Text: string(data)})
	d.follower.writer = nil
	for _, chunk := range out {
		frameType := driverpkg.RuntimeOutputStdout
		if chunk.Stream == domain.StdioStderr {
			frameType = driverpkg.RuntimeOutputStderr
		}
		d.pending = append(d.pending, driverpkg.RuntimeOutputFrame{Type: frameType, Data: []byte(chunk.Text)})
	}
	if d.follower.exit != nil && d.result == nil {
		exit := execution.DetachExitResultFor(*d.follower.exit)
		mapped, exitErr := execution.ExecResultFromDetachExit(exit)
		result := driverpkg.RuntimeResult{
			OperationID: d.task.ExecutionID,
			ExitCode:    mapped.ExitCode,
			Success:     exitErr == nil && mapped.ExitCode == 0,
			CompletedAt: time.Now().UTC(),
		}
		if exitErr != nil {
			result.Error = exitErr.Error()
		}
		d.result = &result
		d.ended.Store(true)
		d.pending = append(d.pending, driverpkg.RuntimeOutputFrame{Type: driverpkg.RuntimeOutputResult, Result: &result})
		d.finish(true)
	}
}

// endWithoutExit reports an attach that ended before the exit record.
func (d *detachedInteraction) endWithoutExit(err error) error {
	d.ended.Store(true)
	if d.disconnected() || detachIsShutdown(d.runCtx) {
		d.finish(false)
		return ErrRunDisconnected
	}
	cause := context.Cause(d.attachCtx)
	d.finish(false)
	if cause != nil && !isBareContextError(cause) {
		return cause
	}
	detail := strings.TrimSpace(d.follower.attachStderr.String())
	if errors.Is(err, io.EOF) {
		err = errors.New("detached interaction ended without an exit record")
	}
	if detail != "" {
		return fmt.Errorf("%w (attach stderr: %s)", err, detail)
	}
	return err
}

// finish stops the watcher and, once the task's exit was seen, removes its
// guest state.
func (d *detachedInteraction) finish(exited bool) {
	d.cancelAttach(nil)
	<-d.watchDone
	if exited {
		cleanupDetachedExecution(d.runCtx, d.task)
	}
}

func (d *detachedInteraction) Wait() (driverpkg.RuntimeResult, error) {
	if d.result != nil {
		return *d.result, nil
	}
	if d.disconnected() {
		return driverpkg.RuntimeResult{}, ErrRunDisconnected
	}
	return d.underlying.Wait()
}
