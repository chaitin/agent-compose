//go:build linux && cgo && microsandboxcgo

package driver

import (
	"context"
	"testing"
	"time"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

func TestSmokeMicrosandboxStopResumePreservesWritableLayer(t *testing.T) {
	runtimeSmokeEnabled(t, RuntimeDriverMicrosandbox)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	config := newRuntimeSmokeConfig(t, RuntimeDriverMicrosandbox)
	session, vmState, proxyState := newRuntimeSmokeSandbox(t, ctx, config, RuntimeDriverMicrosandbox)
	runtime := &microsandboxRuntime{config: config, lifecycleHandles: map[string]*microsandbox.Sandbox{}}
	assertRuntimeStopResumePreservesWritableLayer(t, ctx, config, runtime, session, vmState, proxyState)
}

func TestSmokeMicrosandboxRuntimeMountManifestDirectoryOnlyStarts(t *testing.T) {
	runtimeSmokeEnabled(t, RuntimeDriverMicrosandbox)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	config := newRuntimeSmokeConfig(t, RuntimeDriverMicrosandbox)
	session, vmState, proxyState := newRuntimeSmokeSandbox(t, ctx, config, RuntimeDriverMicrosandbox)
	assertDirectoryOnlyRuntimeSmokeManifest(t, session, RuntimeDriverMicrosandbox)

	runtime := &microsandboxRuntime{config: config, lifecycleHandles: map[string]*microsandbox.Sandbox{}}
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
