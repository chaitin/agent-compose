//go:build boxlitecgo && driversmoke && driversmoke && driversmoke

package drivermount

import (
	. "agent-compose/pkg/driver/types"
	"context"
	"testing"
	"time"
)

func TestSmokeBoxLiteRuntimeMountManifestDirectoryOnlyStarts(t *testing.T) {
	runtimeSmokeEnabled(t, RuntimeDriverBoxlite)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	config := newRuntimeSmokeConfig(t, RuntimeDriverBoxlite)
	session, vmState, proxyState := newRuntimeSmokeSession(t, ctx, config, RuntimeDriverBoxlite)
	assertDirectoryOnlyRuntimeSmokeManifest(t, session, RuntimeDriverBoxlite)

	runtime := &cgoBoxRuntime{config: config}
	box, created, err := runtime.getOrCreateBox(ctx, session, vmState, proxyState)
	if err != nil {
		t.Fatalf("getOrCreateBox returned error: %v", err)
	}
	defer box.free()
	boxID, err := runtime.boxID(box)
	if err != nil {
		t.Fatalf("boxID returned error: %v", err)
	}
	vmState.BoxID = boxID
	t.Cleanup(func() {
		if t.Failed() && runtimeSmokeKeepTmp() {
			return
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), config.SessionStopTimeout)
		defer stopCancel()
		_, _ = runtime.StopSession(stopCtx, session, vmState)
	})
	if created {
		if err := runtime.startBox(ctx, box); err != nil {
			t.Fatalf("startBox returned error: %v", err)
		}
	}
	assertRuntimeSmokeHomeFiles(t, ctx, runtime, session, vmState)
}

func TestSmokeBoxLiteUsesGoContainerRegistryOCIImage(t *testing.T) {
	runtimeSmokeEnabled(t, RuntimeDriverBoxlite)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	config := newRuntimeSmokeConfig(t, RuntimeDriverBoxlite)
	config.BoxRootfsPath = ""
	config.DefaultImage = prepareRuntimeSmokeGoContainerRegistryOCIImage(t, ctx, config)
	session, vmState, proxyState := newRuntimeSmokeSession(t, ctx, config, RuntimeDriverBoxlite)
	assertDirectoryOnlyRuntimeSmokeManifest(t, session, RuntimeDriverBoxlite)

	runtime := &cgoBoxRuntime{config: config}
	box, created, err := runtime.getOrCreateBox(ctx, session, vmState, proxyState)
	if err != nil {
		t.Fatalf("getOrCreateBox returned error: %v", err)
	}
	defer box.free()
	boxID, err := runtime.boxID(box)
	if err != nil {
		t.Fatalf("boxID returned error: %v", err)
	}
	vmState.BoxID = boxID
	t.Cleanup(func() {
		if t.Failed() && runtimeSmokeKeepTmp() {
			return
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), config.SessionStopTimeout)
		defer stopCancel()
		_, _ = runtime.StopSession(stopCtx, session, vmState)
	})
	if created {
		if err := runtime.startBox(ctx, box); err != nil {
			t.Fatalf("startBox returned error: %v", err)
		}
	}
	assertRuntimeSmokeHomeFiles(t, ctx, runtime, session, vmState)
}
