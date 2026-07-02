//go:build boxlitecgo

package driver

import (
	"context"
	"testing"
	"time"
)

func TestBoxliteAwaiterRegistryDeleteMakesLookupSafe(t *testing.T) {
	registry := &boxliteAwaiterRegistry{}
	awaiter := &boxliteHandleAwaiter{ch: make(chan boxliteHandleResult, 1)}
	handle := registry.register(awaiter)

	got, ok := registry.lookup(handle)
	if !ok {
		t.Fatalf("expected handle %d to be registered", handle)
	}
	if got != awaiter {
		t.Fatalf("lookup returned unexpected awaiter: got %T want %T", got, awaiter)
	}

	registry.delete(handle)
	if _, ok := registry.lookup(handle); ok {
		t.Fatalf("expected handle %d to be removed", handle)
	}
	if handle == 0 {
		t.Fatalf("register returned invalid zero handle")
	}
}

func TestBoxliteExecExitCallbackSignalsExitChannel(t *testing.T) {
	awaiter := &boxliteExecAwaiter{exitCh: make(chan int, 1)}
	handle := globalBoxliteAwaiters.register(awaiter)
	defer globalBoxliteAwaiters.delete(handle)

	notifyBoxliteExecExit(42, handle)

	select {
	case code := <-awaiter.exitCh:
		if code != 42 {
			t.Fatalf("exit code = %d, want 42", code)
		}
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for exit callback")
	}
}

func TestBoxliteExecOutputCallbackSignalsOutputChannel(t *testing.T) {
	awaiter := &boxliteExecAwaiter{outputCh: make(chan struct{}, 1)}

	notifyBoxliteExecOutput(awaiter)

	select {
	case <-awaiter.outputCh:
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for output callback")
	}
}

func TestWaitForExecCompletionReturnsAfterExitIdleGracePeriod(t *testing.T) {
	awaiter := &boxliteExecAwaiter{
		waitCh: make(chan boxliteExecWaitResult, 1),
		exitCh: make(chan int, 1),
	}
	awaiter.exitCh <- 7
	grace := 40 * time.Millisecond
	started := time.Now()

	code, err := waitForExecCompletion(context.Background(), awaiter, grace, func(timeout time.Duration) error {
		time.Sleep(timeout)
		return nil
	})
	if err != nil {
		t.Fatalf("waitForExecCompletion returned error: %v", err)
	}
	if code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
	if elapsed := time.Since(started); elapsed < grace {
		t.Fatalf("wait returned before idle grace period: %s", elapsed)
	}
}

func TestWaitForExecCompletionExtendsGraceOnOutputActivity(t *testing.T) {
	awaiter := &boxliteExecAwaiter{
		waitCh:   make(chan boxliteExecWaitResult, 1),
		exitCh:   make(chan int, 1),
		outputCh: make(chan struct{}, 1),
	}
	awaiter.exitCh <- 7
	grace := 40 * time.Millisecond
	started := time.Now()
	outputAt := make(chan time.Duration, 1)

	go func() {
		time.Sleep(grace / 2)
		notifyBoxliteExecOutput(awaiter)
		outputAt <- time.Since(started)
	}()

	code, err := waitForExecCompletion(context.Background(), awaiter, grace, func(timeout time.Duration) error {
		time.Sleep(10 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatalf("waitForExecCompletion returned error: %v", err)
	}
	if code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
	outputElapsed := <-outputAt
	elapsed := time.Since(started)
	if elapsed < outputElapsed+grace {
		t.Fatalf("wait returned before output idle grace: elapsed=%s output_at=%s grace=%s", elapsed, outputElapsed, grace)
	}
}

func TestWaitForExecCompletionPrefersWaitResult(t *testing.T) {
	awaiter := &boxliteExecAwaiter{
		waitCh: make(chan boxliteExecWaitResult, 1),
		exitCh: make(chan int, 1),
	}
	awaiter.exitCh <- 7
	awaiter.waitCh <- boxliteExecWaitResult{exitCode: 3}

	code, err := waitForExecCompletion(context.Background(), awaiter, time.Second, func(time.Duration) error {
		t.Fatalf("drain should not run when wait result is ready")
		return nil
	})
	if err != nil {
		t.Fatalf("waitForExecCompletion returned error: %v", err)
	}
	if code != 3 {
		t.Fatalf("exit code = %d, want authoritative wait result 3", code)
	}
}
