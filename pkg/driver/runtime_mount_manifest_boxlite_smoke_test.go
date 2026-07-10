//go:build boxlitecgo

package driver

import (
	"context"
	"testing"
	"time"
)

func TestBoxLiteStoppedStatusIsResumable(t *testing.T) {
	if shouldRecreateBoxForStatus("stopped") {
		t.Fatal("stopped box should be restarted instead of recreated")
	}
	for _, status := range []string{"failed", "dead", "removed", "exited"} {
		if !shouldRecreateBoxForStatus(status) {
			t.Fatalf("box status %q should be recreated", status)
		}
	}
}

func TestSmokeBoxLiteStopResumePreservesWritableLayer(t *testing.T) {
	runtimeSmokeEnabled(t, RuntimeDriverBoxlite)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	config := newRuntimeSmokeConfig(t, RuntimeDriverBoxlite)
	session, vmState, proxyState := newRuntimeSmokeSandbox(t, ctx, config, RuntimeDriverBoxlite)
	runtime := &cgoSandboxRuntime{config: config}
	assertRuntimeStopResumePreservesWritableLayer(t, ctx, config, runtime, session, vmState, proxyState)
}

func TestSmokeBoxLiteRuntimeMountManifestDirectoryOnlyStarts(t *testing.T) {
	runtimeSmokeEnabled(t, RuntimeDriverBoxlite)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	config := newRuntimeSmokeConfig(t, RuntimeDriverBoxlite)
	session, vmState, proxyState := newRuntimeSmokeSandbox(t, ctx, config, RuntimeDriverBoxlite)
	assertDirectoryOnlyRuntimeSmokeManifest(t, session, RuntimeDriverBoxlite)

	runtime := &cgoSandboxRuntime{config: config}
	info, err := runtime.EnsureSandbox(ctx, session, vmState, proxyState)
	if err != nil {
		t.Fatalf("EnsureSandbox returned error: %v", err)
	}
	vmState.BoxID = info.BoxID
	t.Cleanup(func() {
		if t.Failed() && runtimeSmokeKeepTmp() {
			return
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), config.SandboxStopTimeout)
		defer stopCancel()
		_, _ = runtime.StopSandbox(stopCtx, session, vmState)
	})
	assertBoxLiteRuntimeSmokeGuestPaths(t, ctx, runtime, vmState)
	assertRuntimeSmokeHomeFiles(t, ctx, runtime, session, vmState)
}

func TestSmokeBoxLiteUsesGoContainerRegistryOCIImage(t *testing.T) {
	runtimeSmokeEnabled(t, RuntimeDriverBoxlite)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	config := newRuntimeSmokeConfig(t, RuntimeDriverBoxlite)
	config.BoxRootfsPath = ""
	config.DefaultImage = prepareRuntimeSmokeGoContainerRegistryOCIImage(t, ctx, config)
	session, vmState, proxyState := newRuntimeSmokeSandbox(t, ctx, config, RuntimeDriverBoxlite)
	assertDirectoryOnlyRuntimeSmokeManifest(t, session, RuntimeDriverBoxlite)

	runtime := &cgoSandboxRuntime{config: config}
	info, err := runtime.EnsureSandbox(ctx, session, vmState, proxyState)
	if err != nil {
		t.Fatalf("EnsureSandbox returned error: %v", err)
	}
	vmState.BoxID = info.BoxID
	t.Cleanup(func() {
		if t.Failed() && runtimeSmokeKeepTmp() {
			return
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), config.SandboxStopTimeout)
		defer stopCancel()
		_, _ = runtime.StopSandbox(stopCtx, session, vmState)
	})
	assertBoxLiteRuntimeSmokeGuestPaths(t, ctx, runtime, vmState)
	assertRuntimeSmokeHomeFiles(t, ctx, runtime, session, vmState)
}

func assertBoxLiteRuntimeSmokeGuestPaths(t *testing.T, ctx context.Context, runtime *cgoSandboxRuntime, vmState VMState) {
	t.Helper()
	box, err := runtime.getBox(ctx, vmState.BoxID)
	if err != nil {
		t.Fatalf("getBox returned error: %v", err)
	}
	defer box.free()
	result, err := runtime.executeBox(ctx, box, ExecSpec{Command: "sh", Args: []string{"-lc", runtimeSmokeGuestPathAssertionScript()}, Cwd: "/"}, nil)
	if err != nil {
		t.Fatalf("boxlite guest path assertion command returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("boxlite guest path assertion failed: exit=%d stdout=%q stderr=%q", result.ExitCode, result.Stdout, result.Stderr)
	}
}
