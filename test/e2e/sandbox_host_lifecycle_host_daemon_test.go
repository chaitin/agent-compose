package e2e

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
)

// TestE2ESandboxHostSurvivesClientRestart exercises the process-level lifecycle
// the daemon-upgrade design depends on: a long-lived sandbox-host process that
// keeps serving the same unix socket after a daemon (client) disconnects, and a
// second host against the same data root that refuses to start (file lock) so no
// duplicate host ever owns the VMs.
func TestE2ESandboxHostSurvivesClientRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	repoRoot := e2eRepoRoot(t)
	testRoot, err := os.MkdirTemp(repoRoot, ".sandbox-host-e2e-")
	if err != nil {
		t.Fatalf("create test root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(testRoot) })

	binary := e2eDaemonBinary(t, ctx, repoRoot, testRoot)
	dataRoot := filepath.Join(testRoot, "data")
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		t.Fatalf("create data root: %v", err)
	}
	socketPath := filepath.Join(testRoot, "sandbox-host.sock")

	host := startE2ESandboxHost(t, binary, testRoot, dataRoot, socketPath)
	waitForE2ESandboxHostSocket(t, ctx, socketPath)

	// First client attaches and handshakes.
	c1 := driverpkg.NewSandboxHostConnector(socketPath, dataRoot, driverpkg.SandboxHostModeExternal)
	if _, err := c1.Client(ctx); err != nil {
		t.Fatalf("first dial: %v\nhost log:\n%s", err, host.logs.String())
	}
	if v := c1.ProtocolVersion(); v <= 0 {
		t.Fatalf("protocol version = %d, want > 0", v)
	}

	// A daemon restart is just the client transport closing; the host must keep
	// serving the same socket.
	if err := c1.Close(); err != nil {
		t.Fatalf("close first transport: %v", err)
	}
	if !e2eSandboxHostSocketReachable(socketPath) {
		t.Fatalf("host socket %s became unreachable after client close\nhost log:\n%s", socketPath, host.logs.String())
	}

	// A second client re-attaches to the same host without respawning one.
	c2 := driverpkg.NewSandboxHostConnector(socketPath, dataRoot, driverpkg.SandboxHostModeExternal)
	if _, err := c2.Client(ctx); err != nil {
		t.Fatalf("re-attach dial: %v\nhost log:\n%s", err, host.logs.String())
	}
	if v := c2.ProtocolVersion(); v <= 0 {
		t.Fatalf("re-attach protocol version = %d, want > 0", v)
	}
	_ = c2.Close()

	// No duplicate host: a second process against the same data root must fail
	// to take the file lock instead of serving a second host.
	duplicate := spawnE2ESandboxHost(binary, testRoot, dataRoot, socketPath)
	if err := duplicate.wait(ctx); err == nil {
		t.Fatalf("second sandbox-host unexpectedly started\nlog:\n%s", duplicate.logs.String())
	} else if !strings.Contains(duplicate.logs.String(), "already running") {
		t.Fatalf("second sandbox-host exit = %v, want lock-conflict \"already running\"\nlog:\n%s", err, duplicate.logs.String())
	}

	// Cleanup the original host and confirm the socket is released with it.
	host.stop(t)
	if e2eSandboxHostSocketReachable(socketPath) {
		t.Fatalf("host socket %s still reachable after host shutdown", socketPath)
	}
}

type e2eSandboxHostProcess struct {
	cmd      *exec.Cmd
	done     chan error
	logs     synchronizedBuffer
	stopOnce sync.Once
}

func spawnE2ESandboxHost(binary, cwd, dataRoot, socketPath string) *e2eSandboxHostProcess {
	process := &e2eSandboxHostProcess{done: make(chan error, 1)}
	process.cmd = exec.Command(binary, "sandbox-host")
	process.cmd.Dir = cwd
	process.cmd.Env = overrideE2EEnv(os.Environ(), map[string]string{
		"DATA_ROOT":                         dataRoot,
		"SANDBOX_ROOT":                      filepath.Join(dataRoot, "sandboxes"),
		"AGENT_COMPOSE_SANDBOX_HOST_SOCKET": socketPath,
		"DEFAULT_IMAGE":                     "debian:bookworm-slim",
		"RUNTIME_DRIVER":                    "docker",
	})
	process.cmd.Stdout = &process.logs
	process.cmd.Stderr = &process.logs
	if err := process.cmd.Start(); err != nil {
		process.done <- err
		return process
	}
	go func() { process.done <- process.cmd.Wait() }()
	return process
}

func startE2ESandboxHost(t *testing.T, binary, cwd, dataRoot, socketPath string) *e2eSandboxHostProcess {
	t.Helper()
	process := spawnE2ESandboxHost(binary, cwd, dataRoot, socketPath)
	select {
	case err := <-process.done:
		t.Fatalf("sandbox-host exited before ready: %v\nlog:\n%s", err, process.logs.String())
	default:
	}
	t.Cleanup(func() { process.stop(t) })
	return process
}

func (p *e2eSandboxHostProcess) wait(ctx context.Context) error {
	select {
	case err := <-p.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *e2eSandboxHostProcess) stop(t *testing.T) {
	t.Helper()
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	p.stopOnce.Do(func() {
		_ = p.cmd.Process.Signal(os.Interrupt)
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
			_ = p.cmd.Process.Kill()
			<-p.done
			t.Log("sandbox-host required forced termination")
		}
	})
}

func waitForE2ESandboxHostSocket(t *testing.T, ctx context.Context, socketPath string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if e2eSandboxHostSocketReachable(socketPath) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for sandbox-host socket: %v", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("sandbox-host socket %s did not become reachable", socketPath)
}

func e2eSandboxHostSocketReachable(socketPath string) bool {
	conn, err := net.DialTimeout("unix", socketPath, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
