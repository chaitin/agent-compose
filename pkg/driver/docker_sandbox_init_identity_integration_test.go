package driver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/sandboxinit"
	"github.com/google/uuid"
)

// These are opt-in integration tests: they need a Docker daemon and a guest
// image containing the identity-aware ac-init. The legacy image is optional and
// proves that a guest image built before the handshake keeps working.

// TestDockerSandboxInitIdentityHandshakeIntegration starts a sandbox through the real
// Docker driver, then checks the handshake the driver's readiness wait depends
// on: the init names its sandbox, advertises the identity capability, rejects a
// different sandbox ID, and refuses to let a workload read its memory or
// environment.
func TestDockerSandboxInitIdentityHandshakeIntegration(t *testing.T) {
	image := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE"))
	if image == "" {
		t.Skip("set AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE to a guest image with the identity-aware ac-init")
	}
	hostTmp := hostVisibleTestDir(t)
	runtime, sandbox := startDockerInitSandboxForTest(t, hostTmp, image)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	client := sandboxinit.NewClient(runtime.sandboxInitSocketHostPath(sandbox))
	info, err := client.VerifySandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("VerifySandbox() error = %v", err)
	}
	if info.SandboxID != sandbox.Summary.ID {
		t.Fatalf("SandboxID = %q, want %q", info.SandboxID, sandbox.Summary.ID)
	}
	if !info.Supports(sandboxinit.CapabilitySandboxIdentity) {
		t.Fatalf("capabilities = %v, want %q", info.Capabilities, sandboxinit.CapabilitySandboxIdentity)
	}
	if _, err := client.VerifySandbox(ctx, "another-"+sandbox.Summary.ID); !errors.Is(err, sandboxinit.ErrSandboxMismatch) {
		t.Fatalf("VerifySandbox(other) error = %v, want ErrSandboxMismatch", err)
	}

	// ac-init is not dumpable, and the exec child has the container's default
	// capability set (no CAP_SYS_PTRACE), so even a root workload cannot read
	// init's memory or environment.
	for _, path := range []string{"/proc/1/mem", "/proc/1/environ"} {
		result, err := runtime.Exec(ctx, sandbox, VMState{}, ExecSpec{
			Command: "/bin/sh",
			Args:    []string{"-c", "dd if=" + path + " of=/dev/null bs=1 count=1 2>&1; printf '\\nstatus:%d' $?"},
			Cwd:     "/",
		})
		if err != nil {
			t.Fatalf("Exec(dd %s) error = %v", path, err)
		}
		if !strings.Contains(result.Stdout, "Permission denied") || !strings.Contains(result.Stdout, "status:1") {
			t.Fatalf("read %s output = %q, want Permission denied and status:1", path, result.Stdout)
		}
	}
}

// TestDockerSandboxInitLegacyHandshakeAcceptedIntegration starts a sandbox from a guest
// image built before the identity handshake and proves the new daemon keeps
// accepting its empty pong. The driver's readiness wait now calls
// VerifySandbox, so this test exercises the legacy branch of that check.
func TestDockerSandboxInitLegacyHandshakeAcceptedIntegration(t *testing.T) {
	image := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_TEST_DOCKER_INIT_LEGACY_IMAGE"))
	if image == "" {
		t.Skip("set AGENT_COMPOSE_TEST_DOCKER_INIT_LEGACY_IMAGE to a guest image with a pre-handshake ac-init")
	}
	hostTmp := hostVisibleTestDir(t)
	runtime, sandbox := startDockerInitSandboxForTest(t, hostTmp, image)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	info, err := sandboxinit.NewClient(runtime.sandboxInitSocketHostPath(sandbox)).Info(ctx)
	if err != nil {
		t.Fatalf("Info() error = %v", err)
	}
	if info.SandboxID != "" {
		t.Fatalf("legacy init reported sandbox %q, want an empty handshake", info.SandboxID)
	}
}

// hostVisibleTestDir returns the directory the test may use for bind-mount
// sources. The Docker daemon runs outside the test process's temporary
// namespace, so a directory under the default TMPDIR may not exist for it.
func hostVisibleTestDir(t *testing.T) string {
	t.Helper()
	dir := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_TEST_HOST_TMP"))
	if dir == "" {
		t.Skip("set AGENT_COMPOSE_TEST_HOST_TMP to a short host-visible directory for Docker bind mounts")
	}
	return dir
}

// startDockerInitSandboxForTest creates one init-backed Docker sandbox through
// the driver and returns it. EnsureSandbox itself exercises the daemon-side
// handshake because the readiness wait calls VerifySandbox.
func startDockerInitSandboxForTest(t *testing.T, hostTmp, image string) (*dockerRuntime, *Sandbox) {
	t.Helper()
	root, err := os.MkdirTemp(hostTmp, "data-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	initDir, err := os.MkdirTemp(hostTmp, "init-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(initDir) })
	// A short ID keeps the host-side init socket under the Linux sun_path
	// limit, which the driver validates against the configured directory.
	id := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	config := &appconfig.Config{
		DataRoot:             root,
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		GuestWorkspacePath:   "/workspace",
		GuestHomePath:        "/root",
		GuestStateRoot:       "/data/state",
		GuestRuntimeRoot:     "/data/runtime",
		GuestLogRoot:         "/data/logs",
		DockerDefaultImage:   image,
		SandboxInitEnabled:   true,
		SandboxInitBinary:    "/.agent-compose/ac-init",
		SandboxInitSocketDir: initDir,
		SandboxStartTimeout:  2 * time.Minute,
		SandboxStopTimeout:   30 * time.Second,
		JupyterReadyTimeout:  time.Second,
		JupyterGuestPort:     8888,
	}
	appconfig.ApplyDefaultGuestPaths(config)
	workspace := filepath.Join(config.SandboxRoot, id, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	sandbox := &Sandbox{Summary: SandboxSummary{
		ID:            id,
		WorkspacePath: workspace,
		GuestImage:    image,
		Driver:        "docker",
	}}
	if _, err := PrepareRuntimeMountManifest(config, sandbox, "docker"); err != nil {
		t.Fatalf("PrepareRuntimeMountManifest() error = %v", err)
	}
	runtime := &dockerRuntime{config: config}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := runtime.EnsureSandbox(ctx, sandbox, VMState{}, ProxyState{}); err != nil {
		t.Fatalf("EnsureSandbox() error = %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		_, _ = runtime.StopSandbox(cleanupCtx, sandbox, VMState{})
		_ = runtime.RemoveSandbox(cleanupCtx, sandbox, VMState{})
	})
	return runtime, sandbox
}
