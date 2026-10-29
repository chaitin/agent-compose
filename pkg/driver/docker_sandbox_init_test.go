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
	root := t.TempDir()
	config := &appconfig.Config{SandboxRoot: root}
	runtime := &dockerRuntime{config: config}
	sandbox := testRuntimeMountSandbox(root)

	mount, err := runtime.prepareSandboxInitMount(context.Background(), nil, sandbox)
	if err != nil {
		t.Fatalf("prepareSandboxInitMount() error = %v", err)
	}
	if mount.Type != mountapi.TypeBind || mount.Target != sandboxInitGuestControlDir || mount.ReadOnly {
		t.Fatalf("mount = %#v, want a writable bind at %s", mount, sandboxInitGuestControlDir)
	}
	if mount.Source != sandboxInitControlHostDir(sandbox) {
		t.Fatalf("mount source = %q, want %q", mount.Source, sandboxInitControlHostDir(sandbox))
	}
	info, err := os.Stat(sandboxInitControlHostDir(sandbox))
	if err != nil {
		t.Fatalf("stat control dir: %v", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("control dir mode = %v, want a 0700 directory", info.Mode())
	}
}

func TestSandboxInitPathsAreInsideTheSandboxDirectory(t *testing.T) {
	sandbox := testRuntimeMountSandbox("/data/sandboxes/session-1")
	dir := sandboxInitControlHostDir(sandbox)
	if dir != filepath.Join("/data/sandboxes/session-1", "init-control") {
		t.Fatalf("control dir = %q", dir)
	}
	if got := sandboxInitSocketHostPath(sandbox); got != filepath.Join(dir, "init.sock") {
		t.Fatalf("socket path = %q", got)
	}
}

func TestDockerSandboxInitBinaryDefaults(t *testing.T) {
	runtime := &dockerRuntime{config: &appconfig.Config{}}
	if got := runtime.sandboxInitBinary(); got != "/usr/local/bin/agent-compose-init" {
		t.Fatalf("binary = %q, want the guest default", got)
	}
	runtime.config.SandboxInitBinary = "/opt/init"
	if got := runtime.sandboxInitBinary(); got != "/opt/init" {
		t.Fatalf("binary = %q, want /opt/init", got)
	}
}

func TestSandboxInitClientPathUsesShortSymlinkForLongSocketPaths(t *testing.T) {
	longDir := filepath.Join(t.TempDir(), strings.Repeat("x", 90))
	sandbox := &Sandbox{Summary: SandboxSummary{
		ID:            "session-1",
		WorkspacePath: filepath.Join(longDir, "workspace"),
	}}
	socket := sandboxInitSocketHostPath(sandbox)
	if len(socket) <= 100 {
		t.Skip("test temp path is too short to exercise the symlink fallback")
	}
	link := sandboxInitClientPath(sandbox)
	if link == socket {
		t.Fatal("long socket path did not use the short symlink")
	}
	t.Cleanup(func() { cleanupSandboxInitClientPath(sandbox) })
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink(%s): %v", link, err)
	}
	if target != socket {
		t.Fatalf("symlink target = %q, want %q", target, socket)
	}
}
