//go:build linux && cgo && microsandboxcgo

package driver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

func TestSmokeMicrosandboxRootfsIsolation(t *testing.T) {
	runtimeSmokeEnabled(t, RuntimeDriverMicrosandbox)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	config := newRuntimeSmokeConfig(t, RuntimeDriverMicrosandbox)
	runtimeDriver := &microsandboxRuntime{config: config, lifecycleHandles: map[string]*microsandbox.Sandbox{}}

	first, firstState, firstProxy := newRuntimeSmokeSandbox(t, ctx, config, RuntimeDriverMicrosandbox)
	second, secondState, secondProxy := newRuntimeSmokeSandbox(t, ctx, config, RuntimeDriverMicrosandbox)
	first.Summary.PullPolicy = "never"
	second.Summary.PullPolicy = "never"
	firstInfo, err := runtimeDriver.EnsureSandbox(ctx, first, firstState, firstProxy)
	if err != nil {
		t.Fatalf("create first microsandbox: %v", err)
	}
	firstState.BoxID = firstInfo.BoxID
	cleanupRuntimeSmokeSandbox(t, config, runtimeDriver, first, firstState)
	secondInfo, err := runtimeDriver.EnsureSandbox(ctx, second, secondState, secondProxy)
	if err != nil {
		t.Fatalf("create second microsandbox: %v", err)
	}
	secondState.BoxID = secondInfo.BoxID
	cleanupRuntimeSmokeSandbox(t, config, runtimeDriver, second, secondState)
	assertMicrosandboxLegacyDockerDiskDirectoryAbsent(t, config.MicrosandboxHome)

	type execution struct {
		name    string
		runtime *microsandboxRuntime
		session *Sandbox
		state   VMState
		value   string
	}
	executions := []execution{
		{name: "first", runtime: runtimeDriver, session: first, state: firstState, value: "first-private"},
		{name: "second", runtime: runtimeDriver, session: second, state: secondState, value: "second-private"},
	}
	var wg sync.WaitGroup
	errors := make(chan error, len(executions))
	for _, item := range executions {
		wg.Add(1)
		go func(item execution) {
			defer wg.Done()
			command := fmt.Sprintf("set -eu; printf %%s %q >/tmp/rootfs-isolation; printf %%s %q >/etc/rootfs-isolation; printf %%s %q >/var/tmp/rootfs-isolation", item.value, item.value, item.value)
			result, err := item.runtime.Exec(ctx, item.session, item.state, ExecSpec{Command: "sh", Args: []string{"-lc", command}, Cwd: "/"})
			if err != nil || !result.Success {
				errors <- fmt.Errorf("%s write result=%#v: %w", item.name, result, err)
			}
		}(item)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	for _, item := range executions {
		result, err := item.runtime.Exec(ctx, item.session, item.state, ExecSpec{Command: "sh", Args: []string{"-lc", "cat /tmp/rootfs-isolation /etc/rootfs-isolation /var/tmp/rootfs-isolation"}, Cwd: "/"})
		if err != nil || !result.Success || result.Stdout != item.value+item.value+item.value {
			t.Fatalf("%s isolated read result=%#v err=%v", item.name, result, err)
		}
	}
	assertMicrosandboxDockerUsesRootDiskAcrossRestart(t, ctx, runtimeDriver, first, firstState, firstProxy)

	firstDisk := runtimeDriver.rootfsDiskPath(first.Summary.ID)
	secondDisk := runtimeDriver.rootfsDiskPath(second.Summary.ID)
	for _, path := range []string{firstDisk, firstDisk + ".owner.json", secondDisk, secondDisk + ".owner.json"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("rootfs resource %s missing: %v", path, err)
		}
	}
	baseMatches, err := filepath.Glob(filepath.Join(config.DataRoot, "image-cache", "*", "microsandbox", "base-v*.qcow2"))
	if err != nil || len(baseMatches) != 1 {
		t.Fatalf("base disks = %#v err=%v", baseMatches, err)
	}
	baseBefore := allocatedBytes(t, baseMatches[0])

	if err := runtimeDriver.RemoveSandbox(ctx, first, firstState); err != nil {
		t.Fatalf("remove first microsandbox: %v", err)
	}
	legacyDockerPaths := []string{
		runtimeDriver.legacyHashedDockerDiskPath(first.Summary.ID),
		runtimeDriver.legacyUnhashedDockerDiskPath(first.Summary.ID),
	}
	removedPaths := []string{firstDisk, firstDisk + ".owner.json"}
	for _, path := range legacyDockerPaths {
		removedPaths = append(removedPaths, path, path+".owner.json")
	}
	for _, path := range removedPaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("removed sandbox resource %s remains: %v", path, err)
		}
	}
	assertMicrosandboxLegacyDockerDiskDirectoryAbsent(t, config.MicrosandboxHome)

	third, thirdState, thirdProxy := newRuntimeSmokeSandbox(t, ctx, config, RuntimeDriverMicrosandbox)
	third.Summary.PullPolicy = "never"
	thirdInfo, err := runtimeDriver.EnsureSandbox(ctx, third, thirdState, thirdProxy)
	if err != nil {
		t.Fatalf("create third microsandbox: %v", err)
	}
	thirdState.BoxID = thirdInfo.BoxID
	cleanupRuntimeSmokeSandbox(t, config, runtimeDriver, third, thirdState)
	assertMicrosandboxLegacyDockerDiskDirectoryAbsent(t, config.MicrosandboxHome)
	result, err := runtimeDriver.Exec(ctx, third, thirdState, ExecSpec{Command: "sh", Args: []string{"-lc", "test ! -e /tmp/rootfs-isolation && test ! -e /etc/rootfs-isolation && test ! -e /var/tmp/rootfs-isolation"}, Cwd: "/"})
	if err != nil || !result.Success {
		t.Fatalf("new sandbox inherited rootfs pollution: result=%#v err=%v", result, err)
	}
	baseAfter := allocatedBytes(t, baseMatches[0])
	if baseAfter != baseBefore {
		t.Fatalf("base allocated bytes changed from %d to %d", baseBefore, baseAfter)
	}
	t.Logf("microsandbox rootfs isolation: base=%s allocated=%d child_a=%d child_b=%d", baseMatches[0], baseAfter, allocatedBytes(t, secondDisk), allocatedBytes(t, runtimeDriver.rootfsDiskPath(third.Summary.ID)))
}

func assertMicrosandboxDockerUsesRootDiskAcrossRestart(
	t *testing.T,
	ctx context.Context,
	runtimeDriver *microsandboxRuntime,
	session *Sandbox,
	vmState VMState,
	proxyState ProxyState,
) {
	t.Helper()
	const marker = "/var/lib/docker/agent-compose-rootfs-smoke-state"
	const assertDockerRootDisk = `
set -eu
driver="$(docker info --format '{{.Driver}}')"
case "$driver" in
  overlay2|overlayfs) ;;
  *) echo "unexpected Docker storage driver: $driver" >&2; exit 1 ;;
esac
test "$(stat -c %d /)" = "$(stat -c %d /var/lib/docker)"
`
	writeResult, err := runtimeDriver.Exec(ctx, session, vmState, ExecSpec{
		Command: "sh",
		Args:    []string{"-lc", assertDockerRootDisk + "printf %s docker-rootfs-state > " + marker},
		Cwd:     "/",
	})
	if err != nil || !writeResult.Success {
		t.Fatalf("verify Docker root disk before restart: result=%#v err=%v", writeResult, err)
	}

	missing, err := runtimeDriver.StopSandbox(ctx, session, vmState)
	if err != nil || missing {
		t.Fatalf("stop microsandbox for Docker root disk check: missing=%v err=%v", missing, err)
	}
	resumeState := vmState
	resumeState.StoppedAt = time.Now().UTC()
	info, err := runtimeDriver.EnsureSandbox(ctx, session, resumeState, proxyState)
	if err != nil {
		t.Fatalf("resume microsandbox for Docker root disk check: %v", err)
	}
	if info.BoxID != vmState.BoxID {
		t.Fatalf("resumed microsandbox = %q, want %q", info.BoxID, vmState.BoxID)
	}
	resumeState.BoxID = info.BoxID
	resumeState.StoppedAt = time.Time{}
	readResult, err := runtimeDriver.Exec(ctx, session, resumeState, ExecSpec{
		Command: "sh",
		Args:    []string{"-lc", assertDockerRootDisk + "test \"$(cat " + marker + ")\" = docker-rootfs-state"},
		Cwd:     "/",
	})
	if err != nil || !readResult.Success {
		t.Fatalf("verify Docker root disk after restart: result=%#v err=%v", readResult, err)
	}
}

func assertMicrosandboxLegacyDockerDiskDirectoryAbsent(t *testing.T, microsandboxHome string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(microsandboxHome, "docker-disks")); !os.IsNotExist(err) {
		t.Fatalf("new Microsandbox creation produced docker-disks, err=%v", err)
	}
}
