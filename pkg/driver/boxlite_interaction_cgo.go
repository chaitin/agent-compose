//go:build linux && cgo && boxlitecgo

package driver

/*
#cgo CFLAGS: -I${SRCDIR}/../../build/boxlite/include
#cgo LDFLAGS: ${SRCDIR}/../../build/boxlite/lib/libboxlite.a -ldl -lpthread -lm
#include <stdint.h>
#include <stdlib.h>
#include "boxlite.h"

extern void agentcomposeBoxliteVoidCallback(CBoxliteError *err, uintptr_t handle);
extern void agentcomposeBoxliteExecStdoutCallback(uint8_t *data, size_t len, uintptr_t handle);
extern void agentcomposeBoxliteExecStderrCallback(uint8_t *data, size_t len, uintptr_t handle);
extern void agentcomposeBoxliteExecWaitCallback(int exit_code, CBoxliteError *err, uintptr_t handle);
extern void agentcomposeBoxliteExecExitCallback(int exit_code, uintptr_t handle);

static void agentcomposeBoxliteInteractionVoidCallbackBridge(CBoxliteError *err, void *user_data) {
	agentcomposeBoxliteVoidCallback(err, (uintptr_t)user_data);
}

static void agentcomposeBoxliteInteractionStdoutCallbackBridge(const uint8_t *data, size_t len, void *user_data) {
	agentcomposeBoxliteExecStdoutCallback((uint8_t *)data, len, (uintptr_t)user_data);
}

static void agentcomposeBoxliteInteractionStderrCallbackBridge(const uint8_t *data, size_t len, void *user_data) {
	agentcomposeBoxliteExecStderrCallback((uint8_t *)data, len, (uintptr_t)user_data);
}

static void agentcomposeBoxliteInteractionWaitCallbackBridge(int exit_code, CBoxliteError *err, void *user_data) {
	agentcomposeBoxliteExecWaitCallback(exit_code, err, (uintptr_t)user_data);
}

static void agentcomposeBoxliteInteractionExitCallbackBridge(int exit_code, void *user_data) {
	agentcomposeBoxliteExecExitCallback(exit_code, (uintptr_t)user_data);
}

static enum BoxliteErrorCode agentcompose_boxlite_interaction_on_stdout(CExecutionHandle *execution, uintptr_t user_handle, CBoxliteError *out_error) {
	return boxlite_execution_on_stdout(execution, agentcomposeBoxliteInteractionStdoutCallbackBridge, (void *)user_handle, out_error);
}

static enum BoxliteErrorCode agentcompose_boxlite_interaction_on_stderr(CExecutionHandle *execution, uintptr_t user_handle, CBoxliteError *out_error) {
	return boxlite_execution_on_stderr(execution, agentcomposeBoxliteInteractionStderrCallbackBridge, (void *)user_handle, out_error);
}

static enum BoxliteErrorCode agentcompose_boxlite_interaction_on_exit(CExecutionHandle *execution, uintptr_t user_handle, CBoxliteError *out_error) {
	return boxlite_execution_on_exit(execution, agentcomposeBoxliteInteractionExitCallbackBridge, (void *)user_handle, out_error);
}

static enum BoxliteErrorCode agentcompose_boxlite_interaction_wait(CExecutionHandle *execution, uintptr_t user_handle, CBoxliteError *out_error) {
	return boxlite_execution_wait(execution, agentcomposeBoxliteInteractionWaitCallbackBridge, (void *)user_handle, out_error);
}

static enum BoxliteErrorCode agentcompose_boxlite_interaction_signal(
	CExecutionHandle *execution,
	int sig,
	uintptr_t user_handle,
	CBoxliteError *out_error
) {
	return boxlite_execution_signal(execution, sig, agentcomposeBoxliteInteractionVoidCallbackBridge, (void *)user_handle, out_error);
}

static enum BoxliteErrorCode agentcompose_boxlite_interaction_resize(
	CExecutionHandle *execution,
	int rows,
	int cols,
	uintptr_t user_handle,
	CBoxliteError *out_error
) {
	return boxlite_execution_tty_resize(execution, rows, cols, agentcomposeBoxliteInteractionVoidCallbackBridge, (void *)user_handle, out_error);
}
*/
import "C"

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

type boxliteCommandInteraction struct {
	runtime       *cgoSandboxRuntime
	runtimeHandle *C.CBoxliteRuntime
	box           *cgoBoxHandle
	execution     *C.CExecutionHandle
	awaiter       *boxliteExecAwaiter
	awaiterHandle uintptr

	ctx           context.Context
	cancel        context.CancelFunc
	operationID   string
	tty           bool
	attachStdin   bool
	startedAt     time.Time
	collector     *cgoExecCollector
	output        chan RuntimeOutputFrame
	done          chan struct{}
	outputMu      sync.Mutex
	outputStarted bool
	startupOutput []RuntimeOutputFrame
	startupDone   chan struct{}

	inputMu       sync.Mutex
	stdinClosed   bool
	closeSendOnce sync.Once
	closeSendErr  error
	cleanupOnce   sync.Once
	result        RuntimeResult
	err           error
}

func (r *cgoSandboxRuntime) openBoxliteInteraction(ctx context.Context, sandbox *Sandbox, vmState VMState, spec RuntimeStartSpec) (RuntimeInteraction, error) {
	if err := r.validateBoxliteInteractionSpec(spec); err != nil {
		return nil, err
	}
	if sandbox == nil {
		return nil, fmt.Errorf("boxlite interaction sandbox is required")
	}

	childCtx, cancel := context.WithCancel(ctx)
	if spec.TimeoutMs > 0 {
		childCtx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutMs)*time.Millisecond)
	}
	runtimeHandle, err := r.runtimeHandle()
	if err != nil {
		cancel()
		return nil, err
	}
	if strings.TrimSpace(vmState.BoxID) == "" {
		cancel()
		return nil, fmt.Errorf("sandbox box is not initialized")
	}
	box, err := r.getBox(childCtx, vmState.BoxID)
	if err != nil {
		cancel()
		return nil, err
	}
	info, err := r.boxInfo(childCtx, box)
	if err != nil {
		box.free()
		cancel()
		return nil, err
	}
	if !info.State.Running {
		if err := r.startBox(childCtx, box); err != nil {
			box.free()
			cancel()
			return nil, err
		}
	}
	if err := r.ensureDirectoryOnlyGuestSandboxBootstrap(childCtx, box, sandbox); err != nil {
		box.free()
		cancel()
		return nil, err
	}

	execution, err := boxliteStartInteractionExecution(box, ExecSpecFromRuntimeStartSpec(spec), spec.TTY)
	if err != nil {
		box.free()
		cancel()
		return nil, err
	}
	interaction := &boxliteCommandInteraction{
		runtime:       r,
		runtimeHandle: runtimeHandle,
		box:           box,
		execution:     execution,
		ctx:           childCtx,
		cancel:        cancel,
		operationID:   spec.OperationID,
		tty:           spec.TTY,
		attachStdin:   spec.AttachStdin,
		startedAt:     time.Now(),
		output:        make(chan RuntimeOutputFrame, 64),
		done:          make(chan struct{}),
		startupDone:   make(chan struct{}),
	}
	interaction.collector = &cgoExecCollector{stream: interaction.emitChunk, streamOnly: true}
	interaction.awaiter = &boxliteExecAwaiter{
		collector: interaction.collector,
		waitCh:    make(chan boxliteExecWaitResult, 1),
		exitCh:    make(chan int, 1),
		outputCh:  make(chan struct{}, 1),
	}
	interaction.awaiterHandle = globalBoxliteAwaiters.register(interaction.awaiter)
	if err := interaction.registerCallbacks(); err != nil {
		interaction.cleanupStart()
		return nil, err
	}
	if spec.Rows != 0 || spec.Cols != 0 {
		if err := interaction.resize(spec.Rows, spec.Cols); err != nil {
			interaction.cleanupStart()
			return nil, err
		}
	}
	interaction.startOutput()
	go interaction.run()
	return interaction, nil
}

func (r *cgoSandboxRuntime) validateBoxliteInteractionSpec(spec RuntimeStartSpec) error {
	if err := r.InteractionCapabilities().ValidateStartSpec(RuntimeDriverBoxlite, spec); err != nil {
		return err
	}
	if normalizeRuntimeOperationKind(spec.Kind) != RuntimeOperationCommand {
		return NewRuntimeInteractionUnsupportedError(RuntimeDriverBoxlite, spec, RuntimeCapabilityNativeExec, "boxlite native attach only supports command interactions")
	}
	command := RuntimeCommandSpec{}
	if spec.Command != nil {
		command = *spec.Command
	}
	if strings.TrimSpace(command.Command) == "" {
		return fmt.Errorf("boxlite interaction command is required")
	}
	return validateBoxliteTerminalSize(spec.Rows, spec.Cols)
}

func validateBoxliteTerminalSize(rows, cols uint32) error {
	if rows > math.MaxInt32 || cols > math.MaxInt32 {
		return fmt.Errorf("boxlite terminal size exceeds %d rows or columns", int64(math.MaxInt32))
	}
	return nil
}

func boxliteStartInteractionExecution(box *cgoBoxHandle, spec ExecSpec, tty bool) (*C.CExecutionHandle, error) {
	command := C.CString(spec.Command)
	defer C.free(unsafe.Pointer(command))
	args, argc, freeArgs := cStringArray(spec.Args)
	defer freeArgs()
	envPairs := make([]string, 0, len(spec.Env)*2)
	keys := make([]string, 0, len(spec.Env))
	for key := range spec.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		envPairs = append(envPairs, key, spec.Env[key])
	}
	env, envCount, freeEnv := cStringArray(envPairs)
	defer freeEnv()
	var workdir *C.char
	if strings.TrimSpace(spec.Cwd) != "" {
		workdir = C.CString(spec.Cwd)
		defer C.free(unsafe.Pointer(workdir))
	}
	ttyFlag := C.int(0)
	if tty {
		ttyFlag = 1
	}
	cmd := C.struct_BoxliteCommand{
		command: command, args: args, argc: C.int(argc), env_pairs: env,
		env_count: C.int(envCount), workdir: workdir, tty: ttyFlag,
	}
	var execution *C.CExecutionHandle
	var ffiErr C.CBoxliteError
	code := C.boxlite_box_exec(box.ptr, &cmd, &execution, &ffiErr)
	if err := boxliteStatusError(code, &ffiErr, "start boxlite interaction"); err != nil {
		return nil, err
	}
	if execution == nil {
		return nil, fmt.Errorf("start boxlite interaction: empty execution handle")
	}
	return execution, nil
}

func (i *boxliteCommandInteraction) registerCallbacks() error {
	var ffiErr C.CBoxliteError
	handle := C.uintptr_t(i.awaiterHandle)
	if err := boxliteStatusError(C.agentcompose_boxlite_interaction_on_stdout(i.execution, handle, &ffiErr), &ffiErr, "register boxlite interaction stdout"); err != nil {
		return err
	}
	if err := boxliteStatusError(C.agentcompose_boxlite_interaction_on_stderr(i.execution, handle, &ffiErr), &ffiErr, "register boxlite interaction stderr"); err != nil {
		return err
	}
	if err := boxliteStatusError(C.agentcompose_boxlite_interaction_on_exit(i.execution, handle, &ffiErr), &ffiErr, "register boxlite interaction exit"); err != nil {
		return err
	}
	return boxliteStatusError(C.agentcompose_boxlite_interaction_wait(i.execution, handle, &ffiErr), &ffiErr, "wait for boxlite interaction")
}

func (i *boxliteCommandInteraction) Send(frame RuntimeInputFrame) error {
	switch frame.Type {
	case RuntimeInputStdin:
		return i.writeStdin(frame.Data)
	case RuntimeInputStdinEOF:
		return i.CloseSend()
	case RuntimeInputResize:
		return i.resize(frame.Rows, frame.Cols)
	case RuntimeInputSignal:
		return i.signal(frame.Signal)
	case RuntimeInputCancel:
		i.cancel()
		return nil
	default:
		return ErrRuntimeInteractionUnsupported
	}
}

func (i *boxliteCommandInteraction) writeStdin(data []byte) error {
	if !i.attachStdin {
		return ErrRuntimeInteractionUnsupported
	}
	if len(data) == 0 {
		return nil
	}
	i.inputMu.Lock()
	defer i.inputMu.Unlock()
	if i.stdinClosed {
		return io.ErrClosedPipe
	}
	if err := i.inputStateErrorLocked(); err != nil {
		return err
	}
	var ffiErr C.CBoxliteError
	return boxliteStatusError(C.boxlite_execution_stdin_write(i.execution, (*C.uint8_t)(unsafe.Pointer(&data[0])), C.size_t(len(data)), &ffiErr), &ffiErr, "write boxlite interaction stdin")
}

func (i *boxliteCommandInteraction) CloseSend() error {
	i.closeSendOnce.Do(func() {
		i.inputMu.Lock()
		defer i.inputMu.Unlock()
		i.stdinClosed = true
		if !i.attachStdin {
			return
		}
		var ffiErr C.CBoxliteError
		i.closeSendErr = boxliteStatusError(C.boxlite_execution_stdin_close(i.execution, &ffiErr), &ffiErr, "close boxlite interaction stdin")
	})
	return i.closeSendErr
}

func (i *boxliteCommandInteraction) resize(rows, cols uint32) error {
	i.inputMu.Lock()
	defer i.inputMu.Unlock()
	if err := i.inputStateErrorLocked(); err != nil {
		return err
	}
	if err := validateBoxliteTerminalSize(rows, cols); err != nil {
		return err
	}
	if !i.tty {
		return ErrRuntimeInteractionUnsupported
	}
	if rows == 0 && cols == 0 {
		return nil
	}
	return i.runVoidOperation("resize boxlite interaction terminal", func(handle C.uintptr_t, ffiErr *C.CBoxliteError) C.enum_BoxliteErrorCode {
		return C.agentcompose_boxlite_interaction_resize(i.execution, C.int(rows), C.int(cols), handle, ffiErr)
	})
}

func (i *boxliteCommandInteraction) signal(signal RuntimeSignal) error {
	i.inputMu.Lock()
	defer i.inputMu.Unlock()
	if err := i.inputStateErrorLocked(); err != nil {
		return err
	}
	sig, err := boxliteRuntimeSignal(signal)
	if err != nil {
		return err
	}
	if sig == int(syscall.SIGKILL) {
		return i.runtime.killExecution(i.ctx, i.runtimeHandle, i.execution)
	}
	return i.runVoidOperation("signal boxlite interaction", func(handle C.uintptr_t, ffiErr *C.CBoxliteError) C.enum_BoxliteErrorCode {
		return C.agentcompose_boxlite_interaction_signal(i.execution, C.int(sig), handle, ffiErr)
	})
}

func (i *boxliteCommandInteraction) inputStateErrorLocked() error {
	if i.execution == nil {
		return io.ErrClosedPipe
	}
	return i.ctx.Err()
}

func (i *boxliteCommandInteraction) runVoidOperation(action string, start func(C.uintptr_t, *C.CBoxliteError) C.enum_BoxliteErrorCode) error {
	awaiter := &boxliteVoidAwaiter{ch: make(chan error, 1)}
	handle := globalBoxliteAwaiters.register(awaiter)
	defer globalBoxliteAwaiters.delete(handle)
	var ffiErr C.CBoxliteError
	if err := boxliteStatusError(start(C.uintptr_t(handle), &ffiErr), &ffiErr, action); err != nil {
		return err
	}
	return i.runtime.waitForVoidResult(i.ctx, i.runtimeHandle, awaiter.ch)
}

func (i *boxliteCommandInteraction) Recv() (RuntimeOutputFrame, error) {
	frame, ok := <-i.output
	if !ok {
		return RuntimeOutputFrame{}, io.EOF
	}
	return frame, nil
}

func (i *boxliteCommandInteraction) Wait() (RuntimeResult, error) {
	<-i.done
	return i.result, i.err
}

func (i *boxliteCommandInteraction) run() {
	defer close(i.done)
	defer close(i.output)
	defer i.cleanup()
	exitCode, err := i.runtime.waitForExecCompletion(i.ctx, i.runtimeHandle, i.awaiter)
	if err != nil && i.ctx.Err() != nil {
		terminationErr := i.runtime.killExecution(i.ctx, i.runtimeHandle, i.execution)
		err = execTerminationResultError(RuntimeDriverBoxlite, i.operationID, i.ctx.Err(), terminationErr)
	}
	i.runtime.flushRuntimeCallbacks(i.runtimeHandle)
	i.collector.finish()
	i.finish(exitCode, err)
}

func (i *boxliteCommandInteraction) emitChunk(chunk ExecChunk) {
	stream := NormalizeStdioStream(chunk.Stream)
	if i.tty {
		stream = StdioStdout
	}
	frameType := RuntimeOutputStdout
	if stream == StdioStderr {
		frameType = RuntimeOutputStderr
	}
	i.emit(RuntimeOutputFrame{Type: frameType, Data: []byte(chunk.Text)})
}

func (i *boxliteCommandInteraction) finish(exitCode int, err error) {
	i.result = RuntimeResult{
		OperationID:   i.operationID,
		ExitCode:      exitCode,
		Success:       err == nil && exitCode == 0,
		StartedAt:     i.startedAt,
		CompletedAt:   time.Now(),
		SecurityFacts: i.collector.filter.SecurityFacts().Pointer(),
	}
	if err != nil {
		i.err = err
		i.result.Error = err.Error()
		i.emit(RuntimeOutputFrame{Type: RuntimeOutputError, Error: &RuntimeError{Message: err.Error()}})
	}
	i.emit(RuntimeOutputFrame{Type: RuntimeOutputResult, Result: &i.result})
}

func (i *boxliteCommandInteraction) emit(frame RuntimeOutputFrame) {
	i.outputMu.Lock()
	if !i.outputStarted {
		i.startupOutput = append(i.startupOutput, frame)
		i.outputMu.Unlock()
		return
	}
	startupDone := i.startupDone
	i.outputMu.Unlock()

	select {
	case <-startupDone:
	case <-i.ctx.Done():
		return
	}
	select {
	case i.output <- frame:
	case <-i.ctx.Done():
	}
}

// startOutput establishes the interaction framing boundary before OpenInteraction
// returns. BoxLite starts the guest before its callbacks can be registered, so
// callbacks observed during registration or the initial resize are staged. The
// asynchronous flush lets callers begin receiving before a noisy guest can fill
// the bounded output channel.
func (i *boxliteCommandInteraction) startOutput() {
	i.outputMu.Lock()
	pending := i.startupOutput
	i.startupOutput = nil
	i.outputStarted = true
	i.output <- RuntimeOutputFrame{Type: RuntimeOutputStarted, StartedAt: i.startedAt}
	i.outputMu.Unlock()

	go func() {
		defer close(i.startupDone)
		for _, frame := range pending {
			select {
			case i.output <- frame:
			case <-i.ctx.Done():
				return
			}
		}
	}()
}

func (i *boxliteCommandInteraction) cleanupStart() {
	terminationErr := i.runtime.killExecution(i.ctx, i.runtimeHandle, i.execution)
	if terminationErr != nil {
		slog.Warn("failed to terminate boxlite interaction after startup error", "operation_id", i.operationID, "error", terminationErr)
	}
	i.cleanup()
}

func (i *boxliteCommandInteraction) cleanup() {
	i.cleanupOnce.Do(func() {
		if err := i.CloseSend(); err != nil {
			slog.Warn("failed to close boxlite interaction stdin", "operation_id", i.operationID, "error", err)
		}
		i.inputMu.Lock()
		defer i.inputMu.Unlock()
		globalBoxliteAwaiters.delete(i.awaiterHandle)
		execution := i.execution
		i.execution = nil
		C.boxlite_execution_free(execution)
		i.box.free()
		i.cancel()
	})
}

func boxliteRuntimeSignal(signal RuntimeSignal) (int, error) {
	switch signal {
	case RuntimeSignalInterrupt:
		return int(syscall.SIGINT), nil
	case RuntimeSignalTerminate:
		return int(syscall.SIGTERM), nil
	case RuntimeSignalKill:
		return int(syscall.SIGKILL), nil
	default:
		return 0, fmt.Errorf("unsupported boxlite runtime signal %q", signal)
	}
}

var _ RuntimeInteraction = (*boxliteCommandInteraction)(nil)
