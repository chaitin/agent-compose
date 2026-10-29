package driver

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/google/uuid"
)

// TestDockerSandboxInitContainerLifecycle is an opt-in smoke test. It needs a
// Docker daemon and a guest image that contains /usr/local/bin/agent-compose-init
// (the published guest image does). Set AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE.
func TestDockerSandboxInitContainerLifecycle(t *testing.T) {
	image := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE"))
	if image == "" {
		t.Skip("set AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE to a guest image with agent-compose-init")
	}
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:            root,
		SandboxRoot:         filepath.Join(root, "sandboxes"),
		GuestWorkspacePath:  "/workspace",
		GuestHomePath:       "/root",
		GuestStateRoot:      "/data/state",
		GuestRuntimeRoot:    "/data/runtime",
		GuestLogRoot:        "/data/logs",
		DockerDefaultImage:  image,
		SandboxInitEnabled:  true,
		SandboxInitBinary:   "/usr/local/bin/agent-compose-init",
		SandboxStartTimeout: 2 * time.Minute,
		SandboxStopTimeout:  30 * time.Second,
		JupyterReadyTimeout: time.Second,
		JupyterGuestPort:    8888,
	}
	appconfig.ApplyDefaultGuestPaths(config)
	id := uuid.NewString()
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

	// The second ensure and the liveness check must see the init control mount
	// as expected, not as an extra stale bind. If they do not, the driver
	// removes the running container and the next exec fails with EOF.
	if _, err := runtime.EnsureSandbox(ctx, sandbox, VMState{}, ProxyState{}); err != nil {
		t.Fatalf("second EnsureSandbox() error = %v", err)
	}
	alive, err := runtime.IsSandboxAlive(ctx, sandbox, VMState{})
	if err != nil || !alive {
		t.Fatalf("IsSandboxAlive() = %v, %v; want true, nil", alive, err)
	}

	result, err := runtime.Exec(ctx, sandbox, VMState{}, ExecSpec{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf init-exec-ok"},
		Cwd:     "/",
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if !strings.Contains(result.Stdout, "init-exec-ok") {
		t.Fatalf("Exec() stdout = %q, want init-exec-ok", result.Stdout)
	}

	// The exec process must be a direct child of PID 1 (the init process), not
	// a docker-exec process reparented outside the sandbox.
	parent, err := runtime.Exec(ctx, sandbox, VMState{}, ExecSpec{
		Command: "/bin/sh",
		Args:    []string{"-c", "awk '/^PPid:/{print $2}' /proc/$$/status"},
		Cwd:     "/",
	})
	if err != nil {
		t.Fatalf("Exec(PPid) error = %v", err)
	}
	if strings.TrimSpace(parent.Stdout) != "1" {
		t.Fatalf("exec PPid = %q, want 1", strings.TrimSpace(parent.Stdout))
	}

	interaction, err := runtime.OpenInteraction(ctx, sandbox, VMState{}, RuntimeStartSpec{
		OperationID: "init-interaction",
		Kind:        RuntimeOperationCommand,
		Command:     &RuntimeCommandSpec{Command: "/bin/cat"},
		AttachStdin: true,
	})
	if err != nil {
		t.Fatalf("OpenInteraction() error = %v", err)
	}
	if err := interaction.Send(RuntimeInputFrame{Type: RuntimeInputStdin, Data: []byte("stdin-ok\n")}); err != nil {
		t.Fatalf("interaction.Send() error = %v", err)
	}
	if err := interaction.Send(RuntimeInputFrame{Type: RuntimeInputStdinEOF}); err != nil {
		t.Fatalf("interaction.Send(EOF) error = %v", err)
	}
	var output strings.Builder
	for {
		frame, err := interaction.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("interaction.Recv() error = %v", err)
		}
		if frame.Type == RuntimeOutputStdout {
			output.Write(frame.Data)
		}
		if frame.Type == RuntimeOutputError {
			t.Fatalf("interaction error frame: %#v", frame.Error)
		}
	}
	interactionResult, err := interaction.Wait()
	if err != nil {
		t.Fatalf("interaction.Wait() error = %v", err)
	}
	if interactionResult.ExitCode != 0 || !strings.Contains(output.String(), "stdin-ok") {
		t.Fatalf("interaction result = %#v output=%q", interactionResult, output.String())
	}
}
