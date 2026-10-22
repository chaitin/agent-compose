package runs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestDetachFollowerReconstructsResultAcrossChunks(t *testing.T) {
	var forwarded []domain.ExecChunk
	var persisted []uint64
	f := newDetachFollower(
		func(chunk domain.ExecChunk) { forwarded = append(forwarded, chunk) },
		func(offset uint64) error { persisted = append(persisted, offset); return nil },
	)

	// Split a record across chunk boundaries to exercise line buffering.
	f.consume(domain.ExecChunk{Text: `{"t":"out","s":"stdout","text":"hi\n","off`})
	f.consume(domain.ExecChunk{Text: `set":0}` + "\n" + `{"t":"out","s":"stderr","text":"err\n","offset":3}` + "\n" + `{"t":"ex`})
	f.consume(domain.ExecChunk{Text: `it","code":0,"reason":"completed"}` + "\n"})

	res := f.result(nil, context.Background())
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if res.Result.Stdout != "hi\n" || res.Result.Stderr != "err\n" || res.Result.Output != "hi\nerr\n" {
		t.Fatalf("result = %+v", res.Result)
	}
	if res.Result.ExitCode != 0 || !res.Result.Success {
		t.Fatalf("exit = %+v", res.Result)
	}
	if f.cursor != 7 {
		t.Fatalf("cursor = %d, want 7", f.cursor)
	}
	if len(persisted) == 0 || persisted[len(persisted)-1] != 7 {
		t.Fatalf("persisted cursor = %v, want last 7", persisted)
	}
	if len(forwarded) != 2 {
		t.Fatalf("forwarded %d chunks, want 2", len(forwarded))
	}
	if forwarded[0].Stream != domain.StdioStdout || forwarded[1].Stream != domain.StdioStderr {
		t.Fatalf("forwarded streams = %v", forwarded)
	}
}

func TestDetachFollowerExitMapping(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantErr    bool
		wantStop   string
		wantCode   int
		wantCancel bool
	}{
		{"completed", `{"t":"exit","code":0,"reason":"completed"}`, false, "", 0, false},
		{"stopped user", `{"t":"exit","code":143,"reason":"stopped","stopReason":"user"}`, true, "user", 143, true},
		{"stopped timeout", `{"t":"exit","code":124,"reason":"stopped","stopReason":"timeout"}`, true, "timeout", 124, false},
		{"abnormal", `{"t":"exit","code":-1,"reason":"abnormal"}`, true, "", -1, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newDetachFollower(nil, nil)
			f.consume(domain.ExecChunk{Text: tc.line + "\n"})
			res := f.result(nil, context.Background())
			if (res.Err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", res.Err, tc.wantErr)
			}
			if res.StopReason != tc.wantStop {
				t.Fatalf("stop reason = %q, want %q", res.StopReason, tc.wantStop)
			}
			if res.Result.ExitCode != tc.wantCode {
				t.Fatalf("exit code = %d, want %d", res.Result.ExitCode, tc.wantCode)
			}
			if tc.wantCancel && !strings.Contains(res.Err.Error(), "stopped") {
				t.Fatalf("expected stopped error, got %v", res.Err)
			}
		})
	}
}

func TestDetachFollowerDisconnectedWithoutExit(t *testing.T) {
	f := newDetachFollower(nil, nil)
	attachCtx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrRunDisconnected)
	res := f.result(context.Canceled, attachCtx)
	if !errors.Is(res.Err, ErrRunDisconnected) {
		t.Fatalf("err = %v, want ErrRunDisconnected", res.Err)
	}
}

func TestDetachStopReason(t *testing.T) {
	if got := detachStopReason(context.Background()); got != "user" {
		t.Fatalf("got %q, want user", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	_ = cancel
	<-ctx.Done()
	if got := detachStopReason(ctx); got != "timeout" {
		t.Fatalf("got %q, want timeout", got)
	}
}

func TestDetachIsShutdown(t *testing.T) {
	if detachIsShutdown(context.Background()) {
		t.Fatal("background context reported as shutdown")
	}
	// Fresh-run path: the exec context is cancelled with ErrRunDisconnected when
	// the daemon shutdown signal fires.
	execCtx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrRunDisconnected)
	if !detachIsShutdown(execCtx) {
		t.Fatal("ErrRunDisconnected cause not reported as shutdown")
	}
	// Re-attach path: the run context stays alive and only carries the fired
	// shutdown signal as a value.
	shutdown := make(chan struct{})
	close(shutdown)
	if !detachIsShutdown(WithDaemonShutdown(context.Background(), shutdown)) {
		t.Fatal("fired shutdown signal not reported as shutdown")
	}
}

// detachStopFakeRuntime records stop requests and marker terminations, and blocks
// the attach exec until its context is cancelled so followDetachedCommand stays
// in the follow loop long enough to exercise the stop/shutdown paths.
type detachStopFakeRuntime struct {
	mu               sync.Mutex
	stopCalls        int
	terminated       bool
	terminatedMarker string
	stopErr          error
	// stopExitCode, when non-zero, makes the stop exec return a non-zero exit
	// code with a nil error, as the drivers do for a guest CLI that exited
	// non-zero (writeStop failed).
	stopExitCode int
	stopStderr   string
	terminateErr error
	// stopBlocks makes the stop request hang until its context expires, as
	// when the guest or supervisor does not respond.
	stopBlocks bool
	// attachRecords, when non-nil, is emitted by the attach exec, which then
	// ends normally instead of waiting for cancellation.
	attachRecords []string
	// terminateCtxErr is the state of the context TerminateProcessTree got.
	terminateCtxErr error
}

func (r *detachStopFakeRuntime) ExecStream(ctx context.Context, _ *domain.Sandbox, _ domain.VMState, spec domain.ExecSpec, writer domain.ExecStreamWriter) (domain.ExecResult, error) {
	if len(spec.Args) > 0 && spec.Args[0] == "stop" {
		r.mu.Lock()
		r.stopCalls++
		r.mu.Unlock()
		if r.stopBlocks {
			<-ctx.Done()
			return domain.ExecResult{}, ctx.Err()
		}
		if r.stopErr != nil {
			return domain.ExecResult{}, r.stopErr
		}
		if r.stopExitCode != 0 {
			return domain.ExecResult{ExitCode: r.stopExitCode, Stderr: r.stopStderr}, nil
		}
		return domain.ExecResult{}, nil
	}
	if len(spec.Args) > 0 && spec.Args[0] == "attach" {
		if r.attachRecords != nil {
			for _, record := range r.attachRecords {
				if writer != nil {
					writer(domain.ExecChunk{Stream: domain.StdioStdout, Text: record + "\n"})
				}
			}
			return domain.ExecResult{}, nil
		}
		<-ctx.Done()
		return domain.ExecResult{}, ctx.Err()
	}
	return domain.ExecResult{}, nil
}

func (r *detachStopFakeRuntime) TerminateProcessTree(ctx context.Context, _ *domain.Sandbox, _ domain.VMState, marker string) error {
	r.mu.Lock()
	r.terminateCtxErr = ctx.Err()
	r.terminated = true
	r.terminatedMarker = marker
	r.mu.Unlock()
	return r.terminateErr
}

func (r *detachStopFakeRuntime) stopCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopCalls
}

func (r *detachStopFakeRuntime) termination() (bool, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.terminated, r.terminatedMarker
}

func TestFollowDetachedCommandDoesNotStopOnShutdown(t *testing.T) {
	shutdown := make(chan struct{})
	runCtx := WithDaemonShutdown(context.Background(), shutdown)
	execCtx, cancel := runExecContext(runCtx)
	defer cancel()

	rt := &detachStopFakeRuntime{}
	opts := DetachedTask{
		Config:      &config.Config{},
		Runtime:     rt,
		Sandbox:     &domain.Sandbox{},
		VMState:     domain.VMState{},
		ExecutionID: "exec-1",
	}

	done := make(chan detachedExecutionResult, 1)
	go func() { done <- followDetachedCommand(execCtx, opts) }()

	close(shutdown) // daemon shutdown

	result := <-done
	if !errors.Is(result.Err, ErrRunDisconnected) {
		t.Fatalf("err = %v, want ErrRunDisconnected", result.Err)
	}
	if n := rt.stopCount(); n != 0 {
		t.Fatalf("stop was sent %d times on shutdown, want 0", n)
	}
}

func TestFollowDetachedCommandTerminatesTreeAfterAbnormalExit(t *testing.T) {
	previous := detachStopTimeout
	detachStopTimeout = 50 * time.Millisecond
	t.Cleanup(func() { detachStopTimeout = previous })

	// The supervisor died without writing an exit record; attach reports the
	// task as abnormally ended and the process tree may still be running.
	rt := &detachStopFakeRuntime{attachRecords: []string{`{"t":"out","s":"stdout","text":"partial\n"}`, `{"t":"exit","code":-1,"reason":"abnormal"}`}}
	opts := DetachedTask{
		Config:      &config.Config{},
		Runtime:     rt,
		Sandbox:     &domain.Sandbox{},
		VMState:     domain.VMState{},
		ExecutionID: "exec-1",
	}

	result := followDetachedCommand(context.Background(), opts)
	if !result.Abnormal {
		t.Fatalf("result = %+v, want an abnormal exit", result)
	}
	if result.Err == nil {
		t.Fatal("abnormal exit reported no error")
	}
	terminated, marker := rt.termination()
	if !terminated {
		t.Fatal("TerminateProcessTree was not called after the supervisor died")
	}
	if marker != "exec-1" {
		t.Fatalf("terminated marker = %q, want exec-1", marker)
	}
	if rt.terminateCtxErr != nil {
		t.Fatalf("termination context was already done: %v", rt.terminateCtxErr)
	}
}

func TestFollowDetachedCommandTerminatesTreeWhenStopFails(t *testing.T) {
	rt := &detachStopFakeRuntime{stopErr: errors.New("stop failed")}
	ctx, cancel := context.WithCancel(context.Background())
	opts := DetachedTask{
		Config:      &config.Config{},
		Runtime:     rt,
		Sandbox:     &domain.Sandbox{},
		VMState:     domain.VMState{},
		ExecutionID: "exec-1",
	}

	done := make(chan detachedExecutionResult, 1)
	go func() { done <- followDetachedCommand(ctx, opts) }()

	cancel() // user stop

	result := <-done
	if result.Err == nil || !strings.Contains(result.Err.Error(), "stop request") {
		t.Fatalf("err = %v, want stop-failed error", result.Err)
	}
	terminated, marker := rt.termination()
	if !terminated {
		t.Fatal("TerminateProcessTree was not called after stop failed")
	}
	if marker != "exec-1" {
		t.Fatalf("terminated marker = %q, want exec-1", marker)
	}
}

func TestDetachFollowerIgnoresAttachStderr(t *testing.T) {
	f := newDetachFollower(nil, nil)
	// A stderr chunk landing in the middle of a stdout record must neither
	// break the record nor be parsed as one.
	f.consume(domain.ExecChunk{Stream: domain.StdioStdout, Text: `{"t":"out","s":"stdout","text":"hi\n",`})
	f.consume(domain.ExecChunk{Stream: domain.StdioStderr, Text: "(node:1) ExperimentalWarning: something\n"})
	f.consume(domain.ExecChunk{Stream: domain.StdioStdout, Text: `"offset":0}` + "\n" + `{"t":"exit","code":0,"reason":"completed"}` + "\n"})

	res := f.result(nil, context.Background())
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if res.Result.Stdout != "hi\n" || res.Result.ExitCode != 0 || !res.Result.Success {
		t.Fatalf("result = %+v", res.Result)
	}
}

func TestDetachFollowerReportsAttachStderrWhenAttachFails(t *testing.T) {
	f := newDetachFollower(nil, nil)
	f.consume(domain.ExecChunk{Stream: domain.StdioStderr, Text: "Error: cannot read journal\n"})

	res := f.result(errors.New("exit status 1"), context.Background())
	if res.Err == nil || !strings.Contains(res.Err.Error(), "cannot read journal") {
		t.Fatalf("err = %v, want attach stderr in the error", res.Err)
	}
}

func TestTruncateUTF8KeepsRunesWhole(t *testing.T) {
	if got := truncateUTF8("中文", 4); got != "中" {
		t.Fatalf("truncateUTF8 = %q, want %q", got, "中")
	}
	if got := truncateUTF8("abc", 10); got != "abc" {
		t.Fatalf("truncateUTF8 = %q, want %q", got, "abc")
	}
}

func TestFollowDetachedCommandTerminatesTreeWithFreshContextAfterStopTimeout(t *testing.T) {
	previous := detachStopTimeout
	detachStopTimeout = 50 * time.Millisecond
	t.Cleanup(func() { detachStopTimeout = previous })

	rt := &detachStopFakeRuntime{stopBlocks: true}
	ctx, cancel := context.WithCancel(context.Background())
	opts := DetachedTask{
		Config:      &config.Config{},
		Runtime:     rt,
		Sandbox:     &domain.Sandbox{},
		VMState:     domain.VMState{},
		ExecutionID: "exec-1",
	}

	done := make(chan detachedExecutionResult, 1)
	go func() { done <- followDetachedCommand(ctx, opts) }()
	cancel() // user stop; the stop request then times out

	result := <-done
	if result.Err == nil || !strings.Contains(result.Err.Error(), "stop request") {
		t.Fatalf("err = %v, want stop-failed error", result.Err)
	}
	rt.mu.Lock()
	terminated, ctxErr := rt.terminated, rt.terminateCtxErr
	rt.mu.Unlock()
	if !terminated {
		t.Fatal("TerminateProcessTree was not called after the stop request timed out")
	}
	if ctxErr != nil {
		t.Fatalf("TerminateProcessTree got an expired context: %v", ctxErr)
	}
}

func TestFollowDetachedCommandTerminatesTreeWhenStopExitsNonZero(t *testing.T) {
	// A guest whose writeStop fails exits non-zero; the driver returns that as a
	// nil error, so the daemon must read the exit code to notice the stop did
	// not land and fall back to marker termination.
	rt := &detachStopFakeRuntime{stopExitCode: 1, stopStderr: "no space left on device"}
	ctx, cancel := context.WithCancel(context.Background())
	opts := DetachedTask{
		Config:      &config.Config{},
		Runtime:     rt,
		Sandbox:     &domain.Sandbox{},
		VMState:     domain.VMState{},
		ExecutionID: "exec-1",
	}

	done := make(chan detachedExecutionResult, 1)
	go func() { done <- followDetachedCommand(ctx, opts) }()
	cancel() // user stop; the stop exec then reports a non-zero exit

	result := <-done
	if result.Err == nil || !strings.Contains(result.Err.Error(), "stop request") {
		t.Fatalf("err = %v, want stop-failed error", result.Err)
	}
	if !strings.Contains(result.Err.Error(), "no space left on device") {
		t.Fatalf("err = %v, want guest stderr surfaced", result.Err)
	}
	if !strings.Contains(result.Err.Error(), "guest exited 1") {
		t.Fatalf("err = %v, want the exit code surfaced", result.Err)
	}
	terminated, marker := rt.termination()
	if !terminated {
		t.Fatal("TerminateProcessTree was not called after the stop exited non-zero")
	}
	if marker != "exec-1" {
		t.Fatalf("terminated marker = %q, want exec-1", marker)
	}
}

func TestFollowDetachedCommandTerminatesTreeWhenStopDoesNotLand(t *testing.T) {
	previous := detachStopTimeout
	detachStopTimeout = 50 * time.Millisecond
	t.Cleanup(func() { detachStopTimeout = previous })

	// The stop request "succeeds" (nil error, zero exit) but the supervisor
	// never stops the task, so no exit record arrives: the follower must time
	// out, terminate the tree by marker, and finalize the run instead of
	// blocking forever on the outcome channel.
	rt := &detachStopFakeRuntime{}
	ctx, cancel := context.WithCancel(context.Background())
	opts := DetachedTask{
		Config:      &config.Config{},
		Runtime:     rt,
		Sandbox:     &domain.Sandbox{},
		VMState:     domain.VMState{},
		ExecutionID: "exec-1",
	}

	done := make(chan detachedExecutionResult, 1)
	go func() { done <- followDetachedCommand(ctx, opts) }()
	cancel() // user stop; the stop lands but the task never exits

	select {
	case result := <-done:
		if result.Err == nil || !strings.Contains(result.Err.Error(), "did not stop within") {
			t.Fatalf("err = %v, want stop-timeout error", result.Err)
		}
		terminated, marker := rt.termination()
		if !terminated {
			t.Fatal("TerminateProcessTree was not called after the stop did not land")
		}
		if marker != "exec-1" {
			t.Fatalf("terminated marker = %q, want exec-1", marker)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("followDetachedCommand blocked on a stop that never lands")
	}
}

// startFakeRuntime reports a fixed exit code for any exec, so
// startDetachedCommand can be exercised without a real driver. It also records
// marker termination so the orphan-prevention path can be asserted.
type startFakeRuntime struct {
	exitCode int
	stderr   string

	mu               sync.Mutex
	terminated       bool
	terminatedMarker string
}

func (r *startFakeRuntime) ExecStream(_ context.Context, _ *domain.Sandbox, _ domain.VMState, _ domain.ExecSpec, _ domain.ExecStreamWriter) (domain.ExecResult, error) {
	return domain.ExecResult{ExitCode: r.exitCode, Stderr: r.stderr}, nil
}

func (r *startFakeRuntime) TerminateProcessTree(_ context.Context, _ *domain.Sandbox, _ domain.VMState, marker string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.terminated = true
	r.terminatedMarker = marker
	return nil
}

func (r *startFakeRuntime) termination() (bool, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.terminated, r.terminatedMarker
}

func TestStartDetachedCommandSurfacesNonZeroExit(t *testing.T) {
	// `start --detach` failure (ready timeout, spawn failure, unwritable state
	// dir) surfaces only as a non-zero exit code with a nil error; the daemon
	// must surface the guest stderr instead of proceeding to a doomed attach.
	rt := &startFakeRuntime{exitCode: 1, stderr: "spawn supervisor: EACCES"}
	result := startDetachedCommand(context.Background(), DetachedTask{
		Config:      &config.Config{},
		Runtime:     rt,
		Sandbox:     &domain.Sandbox{},
		VMState:     domain.VMState{},
		ExecutionID: "exec-1",
		TaskArgs:    []string{"exec", "--request-file", "/tmp/req.json"},
	})
	if result.Err == nil {
		t.Fatal("start exited non-zero but reported no error")
	}
	if !strings.Contains(result.Err.Error(), "start detached execution") {
		t.Fatalf("err = %v, want start context", result.Err)
	}
	if !strings.Contains(result.Err.Error(), "spawn supervisor: EACCES") {
		t.Fatalf("err = %v, want guest stderr surfaced", result.Err)
	}
}

func TestStartDetachedCommandTerminatesTreeOnFailure(t *testing.T) {
	// A non-zero start exit after the guest spawned the detached supervisor
	// (ready timeout, a crash after spawn) must not leave an untracked tree:
	// the daemon terminates it by marker before giving up.
	rt := &startFakeRuntime{exitCode: 1, stderr: "timed out waiting for ready"}
	result := startDetachedCommand(context.Background(), DetachedTask{
		Config:      &config.Config{},
		Runtime:     rt,
		Sandbox:     &domain.Sandbox{},
		VMState:     domain.VMState{},
		ExecutionID: "exec-2",
		TaskArgs:    []string{"exec", "--request-file", "/tmp/req.json"},
	})
	if result.Err == nil {
		t.Fatal("start exited non-zero but reported no error")
	}
	terminated, marker := rt.termination()
	if !terminated {
		t.Fatal("TerminateProcessTree was not called after start failed")
	}
	if marker != "exec-2" {
		t.Fatalf("termination marker = %q, want %q", marker, "exec-2")
	}
}

func TestStartDetachedCommandDoesNotTerminateTreeOnShutdown(t *testing.T) {
	// A daemon shutdown during the start window must leave the (possibly
	// spawned) task running for re-attach instead of terminating it, even when
	// the start exec failed.
	shutdown := make(chan struct{})
	close(shutdown)
	rt := &startFakeRuntime{exitCode: 1, stderr: "killed"}
	result := startDetachedCommand(WithDaemonShutdown(context.Background(), shutdown), DetachedTask{
		Config:      &config.Config{},
		Runtime:     rt,
		Sandbox:     &domain.Sandbox{},
		VMState:     domain.VMState{},
		ExecutionID: "exec-3",
		TaskArgs:    []string{"exec", "--request-file", "/tmp/req.json"},
	})
	if !errors.Is(result.Err, ErrRunDisconnected) {
		t.Fatalf("err = %v, want ErrRunDisconnected", result.Err)
	}
	if terminated, _ := rt.termination(); terminated {
		t.Fatal("TerminateProcessTree was called on daemon shutdown; the task must survive")
	}
}
