package driver

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/chaitin/agent-compose/pkg/driver/sandboxhost"
	"google.golang.org/grpc"
)

// SandboxHostConnector owns the daemon's lazily-established connection to the
// sandbox-host process. The connection is made on first use so a daemon whose
// selected driver is Docker or k8s never dials or spawns a host, and so a
// compose-deployed daemon simply attaches to the pre-started host.
type SandboxHostConnector struct {
	socketPath string
	dataRoot   string
	mu         sync.Mutex
	client     sandboxhost.SandboxHostClient
	conn       *grpc.ClientConn
}

func NewSandboxHostConnector(socketPath, dataRoot string) *SandboxHostConnector {
	return &SandboxHostConnector{socketPath: socketPath, dataRoot: dataRoot}
}

// Client returns the shared sandbox-host client, dialing and (for a native
// deployment) spawning the host on first use.
func (c *SandboxHostConnector) Client(ctx context.Context) (sandboxhost.SandboxHostClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		return c.client, nil
	}
	client, conn, err := c.dialOrSpawn(ctx)
	if err != nil {
		return nil, err
	}
	c.client = client
	c.conn = conn
	return client, nil
}

func (c *SandboxHostConnector) dialOrSpawn(ctx context.Context) (sandboxhost.SandboxHostClient, *grpc.ClientConn, error) {
	if !sandboxHostSocketReachable(c.socketPath) {
		if err := spawnSandboxHost(c.socketPath, c.dataRoot); err != nil {
			return nil, nil, err
		}
		if err := waitSandboxHostSocket(ctx, c.socketPath); err != nil {
			return nil, nil, err
		}
	}
	return dialSandboxHost(ctx, c.socketPath)
}

// Close closes the shared transport, if any. It is safe to call more than once.
func (c *SandboxHostConnector) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	c.client = nil
	return err
}

func sandboxHostSocketReachable(socketPath string) bool {
	conn, err := net.DialTimeout("unix", socketPath, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func waitSandboxHostSocket(ctx context.Context, socketPath string) error {
	deadline := time.Now().Add(10 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if sandboxHostSocketReachable(socketPath) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("timed out waiting for sandbox-host socket %q", socketPath)
			}
		}
	}
}

// spawnSandboxHost starts `agent-compose sandbox-host` detached from the
// daemon's process group and session, with output redirected to a log file. The
// host itself takes a file lock at startup, so concurrent spawns collapse into
// one host (see acquireSandboxHostLock).
func spawnSandboxHost(socketPath, dataRoot string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate agent-compose executable: %w", err)
	}
	logPath := filepath.Join(dataRoot, "sandbox-host.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open sandbox-host log %q: %w", logPath, err)
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(exe, "sandbox-host")
	cmd.Env = os.Environ()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	// Detach from the daemon's process group and session so the host survives
	// the daemon's shutdown and container stop. The daemon never Waits on it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start sandbox-host: %w", err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release sandbox-host process: %w", err)
	}
	return nil
}
