//go:build linux && cgo && boxlitecgo

package driver

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestClassifyBoxliteStopError(t *testing.T) {
	otherErr := errors.New("stop failed")
	tests := []struct {
		name        string
		err         error
		wantMissing bool
		wantErr     error
	}{
		{name: "success"},
		{name: "already stopped", err: &boxliteCallError{code: boxliteStatusStopped}},
		{name: "not found", err: &boxliteCallError{code: boxliteStatusNotFound}, wantMissing: true},
		{name: "other error", err: otherErr, wantErr: otherErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			missing, err := classifyBoxliteStopError(tt.err)
			if missing != tt.wantMissing || !errors.Is(err, tt.wantErr) {
				t.Fatalf("classifyBoxliteStopError() = (%v, %v), want (%v, %v)", missing, err, tt.wantMissing, tt.wantErr)
			}
		})
	}
}

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

func TestSmokeBoxLiteRuntimeRetentionAndRelease(t *testing.T) {
	runtimeSmokeEnabled(t, RuntimeDriverBoxlite)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	config := newRuntimeSmokeConfig(t, RuntimeDriverBoxlite)
	session, vmState, proxyState := newRuntimeSmokeSandbox(t, ctx, config, RuntimeDriverBoxlite)
	runtime := &cgoSandboxRuntime{config: config}
	assertRuntimeRetentionAndRelease(t, ctx, config, runtime, session, vmState, proxyState)
}

// TestSmokeBoxLiteReusesNamedBoxAfterBoxIDLoss guards the daemon-restart
// scenario behind boxlite_options_set_name: when a daemon restarts after
// creating a box but before persisting its BoxID, the sandbox store still
// holds BoxName (RuntimeRef at create), so lookup must resolve the box by that
// name and reuse it instead of issuing a second, orphaned create. A reused
// runtime must report the same BoxID; a divergent BoxID proves get-by-name did
// not find the named box and a duplicate was created.
func TestSmokeBoxLiteReusesNamedBoxAfterBoxIDLoss(t *testing.T) {
	runtimeSmokeEnabled(t, RuntimeDriverBoxlite)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	config := newRuntimeSmokeConfig(t, RuntimeDriverBoxlite)
	session, vmState, proxyState := newRuntimeSmokeSandbox(t, ctx, config, RuntimeDriverBoxlite)
	proxyState.Enabled = false
	runtime := &cgoSandboxRuntime{config: config}

	info, err := runtime.EnsureSandbox(ctx, session, vmState, proxyState)
	if err != nil {
		t.Fatalf("EnsureSandbox() error = %v", err)
	}
	originalBoxID := info.BoxID
	vmState.BoxID = originalBoxID
	cleanupRuntimeSmokeSandbox(t, config, runtime, session, vmState)

	// Simulate a daemon restart that lost the unpersisted BoxID while BoxName
	// remained. Lookup must fall back to the name assigned at create and reuse
	// the existing box rather than creating a duplicate.
	vmState.BoxID = ""
	resumed, err := runtime.EnsureSandbox(ctx, session, vmState, proxyState)
	if err != nil {
		t.Fatalf("EnsureSandbox() after BoxID loss error = %v", err)
	}
	if resumed.BoxID != originalBoxID {
		t.Fatalf("resumed box = %q, want original %q (must be found by name)", resumed.BoxID, originalBoxID)
	}
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
	cleanupRuntimeSmokeSandbox(t, config, runtime, session, vmState)
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
	cleanupRuntimeSmokeSandbox(t, config, runtime, session, vmState)
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
