//go:build linux && cgo && microsandboxcgo

package driver

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSmokeMicrosandboxExecStreamRecoversLostExitAfterLiveSilenceProbe(t *testing.T) {
	runtimeSmokeEnabled(t, RuntimeDriverMicrosandbox)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	config := newRuntimeSmokeConfig(t, RuntimeDriverMicrosandbox)
	session, vmState, proxyState := newRuntimeSmokeSandbox(t, ctx, config, RuntimeDriverMicrosandbox)
	runtime := &microsandboxRuntime{config: config}
	info, err := runtime.EnsureSandbox(ctx, session, vmState, proxyState)
	if err != nil {
		t.Fatalf("EnsureSandbox returned error: %v", err)
	}
	vmState.BoxID = info.BoxID
	cleanupRuntimeSmokeSandbox(t, config, runtime, session, vmState)

	execCtx, execCancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer execCancel()
	startedAt := time.Now()
	result, err := runtime.ExecStream(execCtx, session, vmState, ExecSpec{
		Command: "sh",
		Args:    []string{"-lc", "sleep 125; echo foreground-done; sleep 600 & exit 7"},
		Cwd:     "/",
	}, func(ExecChunk) {})
	elapsed := time.Since(startedAt)
	if err == nil || !strings.Contains(err.Error(), "exited without reporting its status") {
		t.Fatalf("ExecStream result=%#v error=%v elapsed=%s, want a bounded lost-exit error", result, err, elapsed)
	}
	if !strings.Contains(result.Stdout, "foreground-done") {
		t.Fatalf("ExecStream stdout=%q, want foreground output preserved", result.Stdout)
	}
	if elapsed < 4*time.Minute || elapsed >= 6*time.Minute {
		t.Fatalf("ExecStream elapsed=%s, want recovery after the live and gone silence probes", elapsed)
	}
}

func TestSmokeMicrosandboxRuntimeRetentionAndRelease(t *testing.T) {
	runtimeSmokeEnabled(t, RuntimeDriverMicrosandbox)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	config := newRuntimeSmokeConfig(t, RuntimeDriverMicrosandbox)
	session, vmState, proxyState := newRuntimeSmokeSandbox(t, ctx, config, RuntimeDriverMicrosandbox)
	runtime := &microsandboxRuntime{config: config}
	assertRuntimeRetentionAndRelease(t, ctx, config, runtime, session, vmState, proxyState)
}

func TestSmokeMicrosandboxRuntimeMountManifestDirectoryOnlyStarts(t *testing.T) {
	runtimeSmokeEnabled(t, RuntimeDriverMicrosandbox)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	config := newRuntimeSmokeConfig(t, RuntimeDriverMicrosandbox)
	session, vmState, proxyState := newRuntimeSmokeSandbox(t, ctx, config, RuntimeDriverMicrosandbox)
	assertDirectoryOnlyRuntimeSmokeManifest(t, session, RuntimeDriverMicrosandbox)

	runtime := &microsandboxRuntime{config: config}
	info, err := runtime.EnsureSandbox(ctx, session, vmState, proxyState)
	if err != nil {
		t.Fatalf("EnsureSandbox returned error: %v", err)
	}
	vmState.BoxID = info.BoxID
	cleanupRuntimeSmokeSandbox(t, config, runtime, session, vmState)
	assertMicrosandboxRuntimeSmokeGuestPaths(t, ctx, runtime, session, vmState)
	assertRuntimeSmokeHomeFiles(t, ctx, runtime, session, vmState)
}

func assertMicrosandboxRuntimeSmokeGuestPaths(t *testing.T, ctx context.Context, runtime *microsandboxRuntime, session *Sandbox, vmState VMState) {
	t.Helper()
	result, err := runtime.Exec(ctx, session, vmState, ExecSpec{Command: "sh", Args: []string{"-lc", runtimeSmokeGuestPathAssertionScript()}, Cwd: "/"})
	if err != nil {
		t.Fatalf("microsandbox guest path assertion command returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("microsandbox guest path assertion failed: exit=%d stdout=%q stderr=%q", result.ExitCode, result.Stdout, result.Stderr)
	}
}
