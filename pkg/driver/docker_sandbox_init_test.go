package driver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	containerapi "github.com/docker/docker/api/types/container"
	mountapi "github.com/docker/docker/api/types/mount"
)

func TestDockerSandboxUsesInitReadsTheContainerLabel(t *testing.T) {
	legacy := containerapi.InspectResponse{Config: &containerapi.Config{Labels: map[string]string{}}}
	if dockerSandboxUsesInit(legacy) {
		t.Fatal("legacy container reported as init-based")
	}
	initContainer := containerapi.InspectResponse{Config: &containerapi.Config{Labels: map[string]string{dockerSandboxInitLabel: "true"}}}
	if !dockerSandboxUsesInit(initContainer) {
		t.Fatal("init container not detected")
	}
	if dockerSandboxUsesInit(containerapi.InspectResponse{}) {
		t.Fatal("container without Config reported as init-based")
	}
}

func TestPrepareSandboxInitMountCreatesHostDirectory(t *testing.T) {
	runtimeRoot := t.TempDir()
	config := &appconfig.Config{SandboxInitSocketDir: runtimeRoot}
	runtime := &dockerRuntime{config: config}
	sandbox := testRuntimeMountSandbox(t.TempDir())

	mount, err := runtime.prepareSandboxInitMount(context.Background(), nil, sandbox)
	if err != nil {
		t.Fatalf("prepareSandboxInitMount() error = %v", err)
	}
	if mount.Type != mountapi.TypeBind || mount.Target != sandboxInitGuestControlDir || mount.ReadOnly {
		t.Fatalf("mount = %#v, want a writable bind at %s", mount, sandboxInitGuestControlDir)
	}
	if mount.Source != runtime.sandboxInitControlHostDir(sandbox) {
		t.Fatalf("mount source = %q, want %q", mount.Source, runtime.sandboxInitControlHostDir(sandbox))
	}
	info, err := os.Stat(runtime.sandboxInitControlHostDir(sandbox))
	if err != nil {
		t.Fatalf("stat control dir: %v", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("control dir mode = %v, want a 0700 directory", info.Mode())
	}
}

func TestSandboxInitPathsUseTheRuntimeDirectoryAndSandboxID(t *testing.T) {
	runtime := &dockerRuntime{config: &appconfig.Config{SandboxInitSocketDir: "/run/agent-compose"}}
	sandbox := testRuntimeMountSandbox("/data/sandboxes/session-1")
	dir := runtime.sandboxInitControlHostDir(sandbox)
	if dir != filepath.Join("/run/agent-compose", "session-1") {
		t.Fatalf("control dir = %q", dir)
	}
	if got := runtime.sandboxInitSocketHostPath(sandbox); got != filepath.Join(dir, "init.sock") {
		t.Fatalf("socket path = %q", got)
	}
}

func TestDockerSandboxInitBinaryDefaults(t *testing.T) {
	runtime := &dockerRuntime{config: &appconfig.Config{}}
	if got := runtime.sandboxInitBinary(); got != "/.agent-compose/ac-init" {
		t.Fatalf("binary = %q, want the guest default", got)
	}
	runtime.config.SandboxInitBinary = "/opt/init"
	if got := runtime.sandboxInitBinary(); got != "/opt/init" {
		t.Fatalf("binary = %q, want /opt/init", got)
	}
}

func TestPrepareSandboxInitMountRejectsAnOverlongSocketPath(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), strings.Repeat("x", 90))
	runtime := &dockerRuntime{config: &appconfig.Config{SandboxInitSocketDir: runtimeRoot}}
	sandbox := testRuntimeMountSandbox(t.TempDir())
	if _, err := runtime.prepareSandboxInitMount(context.Background(), nil, sandbox); err == nil {
		t.Fatal("prepareSandboxInitMount accepted a socket path that exceeds the Unix socket limit")
	}
}
