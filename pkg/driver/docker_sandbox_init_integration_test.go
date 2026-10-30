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
// Docker daemon and a guest image that contains /.agent-compose/ac-init
// (the published guest image does). Set AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE.
func TestDockerSandboxInitContainerLifecycle(t *testing.T) {
	image := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE"))
	if image == "" {
		t.Skip("set AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE to a guest image with ac-init")
	}
	root := t.TempDir()
	initDirA, err := os.MkdirTemp("", "ac-init-a-")
	if err != nil {
		t.Fatal(err)
	}
	initDirB, err := os.MkdirTemp("", "ac-init-b-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(initDirA)
		_ = os.RemoveAll(initDirB)
	})
	id := uuid.NewString()
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
		SandboxInitSocketDir: initDirA,
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
		_ = os.RemoveAll(config.SandboxInitSocketDir)
	})

	dockerClient, err := runtime.newClient()
	if err != nil {
		t.Fatalf("new docker client: %v", err)
	}
	defer func() { _ = dockerClient.Close() }()
	info, ok, err := runtime.findContainer(ctx, dockerClient, sandbox, VMState{})
	if err != nil || !ok {
		t.Fatalf("findContainer() = %v, %v; want a running init container", ok, err)
	}
	if len(info.Config.Cmd) != 2 || info.Config.Cmd[0] != "--control-socket" {
		t.Fatalf("container command = %+v, want only the init control socket (no keepalive)", info.Config.Cmd)
	}

	// The second ensure and the liveness check must see the init control mount
	// as expected, not as an extra stale bind. If they do not, the driver
	// removes the running container and the next exec fails with EOF.
	if _, err := runtime.EnsureSandbox(ctx, sandbox, VMState{}, ProxyState{}); err != nil {
		t.Fatalf("second EnsureSandbox() error = %v", err)
	}

	// A changed control directory makes the existing container's mount stale.
	// Recreating it must produce exactly one control mount; appending the init
	// mount to the shared slice and then appending it again on create would make
	// Docker reject the duplicate target.
	config.SandboxInitSocketDir = initDirB
	if _, err := runtime.EnsureSandbox(ctx, sandbox, VMState{}, ProxyState{}); err != nil {
		t.Fatalf("EnsureSandbox() after control-dir change error = %v", err)
	}
	recreated, ok, err := runtime.findContainer(ctx, dockerClient, sandbox, VMState{})
	if err != nil || !ok {
		t.Fatalf("findContainer() after control-dir change = %v, %v", ok, err)
	}
	controlMounts := 0
	for _, mount := range recreated.Mounts {
		if mount.Destination == sandboxInitGuestControlDir {
			controlMounts++
		}
	}
	if controlMounts != 1 {
		t.Fatalf("init control mount count = %d, want exactly one", controlMounts)
	}
	info = recreated
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

	// A background descendant inherits stdout/stderr. The leader's exit frame
	// must not wait for those pipes to close.
	backgroundStarted := time.Now()
	backgroundResult, err := runtime.Exec(ctx, sandbox, VMState{}, ExecSpec{
		Command: "/bin/sh",
		Args:    []string{"-c", "sleep 2 &"},
		Cwd:     "/",
	})
	if err != nil {
		t.Fatalf("Exec(background child) error = %v", err)
	}
	if backgroundResult.ExitCode != 0 {
		t.Fatalf("Exec(background child) exit = %d, want 0", backgroundResult.ExitCode)
	}
	if elapsed := time.Since(backgroundStarted); elapsed > 5*time.Second {
		t.Fatalf("Exec(background child) took %s, want the leader exit without waiting for the pipe", elapsed)
	}

	// A non-interactive exec has no stdin surface. It must still receive EOF
	// like docker exec does; otherwise commands such as cat hang forever.
	stdinResult, err := runtime.Exec(ctx, sandbox, VMState{}, ExecSpec{
		Command: "/bin/cat",
		Cwd:     "/",
	})
	if err != nil {
		t.Fatalf("Exec(cat) error = %v", err)
	}
	if stdinResult.ExitCode != 0 || stdinResult.Stdout != "" {
		t.Fatalf("Exec(cat) = exit %d stdout %q, want a clean EOF", stdinResult.ExitCode, stdinResult.Stdout)
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

	// Canceling an exec must terminate detached descendants, not just the
	// session leader. The workload runtime starts real commands in their own
	// process group, so this uses setsid to reproduce that shape and checks the
	// marker-tree cleanup path.
	cancelCtx, cancelExec := context.WithCancel(ctx)
	execDone := make(chan error, 1)
	go func() {
		_, execErr := runtime.Exec(cancelCtx, sandbox, VMState{}, ExecSpec{
			Command: "/bin/sh",
			Args:    []string{"-c", "setsid /bin/sleep 300 & echo $! > /tmp/init-cancel-child.pid; wait"},
			Cwd:     "/",
		})
		execDone <- execErr
	}()
	time.Sleep(750 * time.Millisecond)
	cancelExec()
	select {
	case err := <-execDone:
		if err == nil {
			t.Fatal("canceled exec returned no error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("canceled exec did not return")
	}
	child, err := runtime.Exec(ctx, sandbox, VMState{}, ExecSpec{
		Command: "/bin/sh",
		Args:    []string{"-c", "pid=$(cat /tmp/init-cancel-child.pid); kill -0 $pid 2>/dev/null && echo ALIVE || echo GONE"},
		Cwd:     "/",
	})
	if err != nil {
		t.Fatalf("Exec(cancellation check) error = %v", err)
	}
	if strings.TrimSpace(child.Stdout) != "GONE" {
		t.Fatalf("detached child after canceled exec = %q, want GONE", strings.TrimSpace(child.Stdout))
	}

	// Start a detached workload that writes a heartbeat into the host-visible
	// workspace. Killing init must kill this PID namespace; otherwise the
	// heartbeat keeps changing after the container is reported exited.
	heartbeat := filepath.Join(workspace, "init-heartbeat")
	if _, err := runtime.Exec(ctx, sandbox, VMState{}, ExecSpec{
		Command: "/bin/sh",
		Args:    []string{"-c", "setsid /bin/sh -c 'while :; do date +%s%N > /workspace/init-heartbeat; sleep 0.2; done' >/dev/null 2>&1 &"},
		Cwd:     "/",
	}); err != nil {
		t.Fatalf("start background workload error = %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, readErr := os.ReadFile(heartbeat)
		if readErr == nil && len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background workload did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// ac-init is PID 1: killing it must terminate the sandbox, not leave a
	// workload tree behind. This is the Stage 1 kill-resilience contract.
	if err := dockerClient.ContainerKill(ctx, info.ID, "KILL"); err != nil {
		t.Fatalf("ContainerKill(init) error = %v", err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for {
		current, inspectErr := dockerClient.ContainerInspect(ctx, info.ID)
		if inspectErr == nil && current.State != nil && !current.State.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sandbox did not exit after ac-init was killed")
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(250 * time.Millisecond)
	afterFirst, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatalf("read heartbeat after kill: %v", err)
	}
	time.Sleep(1 * time.Second)
	afterSecond, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatalf("read heartbeat after kill: %v", err)
	}
	if string(afterFirst) != string(afterSecond) {
		t.Fatalf("background workload kept writing after init was killed: first=%q second=%q", afterFirst, afterSecond)
	}
}
