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

// SandboxHostMode controls whether the daemon may spawn the sandbox-host
// process itself or must only attach to one that is managed outside it.
type SandboxHostMode string

const (
	// SandboxHostModeExternal means the host is managed outside the daemon
	// (for example by Docker Compose) and the daemon must only dial it, never
	// spawn it. Spawning here would create an unprivileged host without
	// /dev/kvm inside the daemon's container, which could steal the data-root
	// lock from the real host and put it into a restart loop.
	SandboxHostModeExternal SandboxHostMode = "external"
	// SandboxHostModeManaged means the daemon may spawn the host on first use.
	// It is intended for local development and native deployments.
	SandboxHostModeManaged SandboxHostMode = "managed"
)

// SandboxHostConnector owns the daemon's lazily-established connection to the
// sandbox-host process. The connection is made on first use so a daemon whose
// selected driver is Docker or k8s never dials or spawns a host, and so a
// compose-deployed daemon simply attaches to the pre-started host.
type SandboxHostConnector struct {
	socketPath      string
	dataRoot        string
	mode            SandboxHostMode
	mu              sync.Mutex
	client          sandboxhost.SandboxHostClient
	conn            *grpc.ClientConn
	protocolVersion int64
}

func NewSandboxHostConnector(socketPath, dataRoot string, mode SandboxHostMode) *SandboxHostConnector {
	if mode == "" {
		mode = SandboxHostModeExternal
	}
	return &SandboxHostConnector{socketPath: socketPath, dataRoot: dataRoot, mode: mode}
}

// Client returns the shared sandbox-host client, dialing and (in managed mode)
// spawning the host on first use. External mode only dials; it never spawns.
func (c *SandboxHostConnector) Client(ctx context.Context) (sandboxhost.SandboxHostClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		return c.client, nil
	}
	client, conn, version, err := c.dialOrSpawn(ctx)
	if err != nil {
		return nil, err
	}
	c.client = client
	c.conn = conn
	c.protocolVersion = version
	return client, nil
}

// ProtocolVersion reports the protocol version negotiated with the sandbox-host
// during the last successful dial. It is zero until the host is first reached,
// which matches "no deny support" for a host the daemon has never spoken to.
//
// The version is negotiated once per daemon lifetime, at first dial: the daemon
// and host are the same binary, so a host upgrade is what changes the version,
// and the daemon re-negotiates it when it next starts (the #805 flow upgrades
// the daemon, which restarts and re-dials). A host upgraded in place under a
// running daemon keeps the daemon's cached version until the daemon restarts;
// because capability gating (see requireDenyProtocolVersion) fails closed on a
// stale version, that is safe, only conservative.
func (c *SandboxHostConnector) ProtocolVersion() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.protocolVersion
}

func (c *SandboxHostConnector) dialOrSpawn(ctx context.Context) (sandboxhost.SandboxHostClient, *grpc.ClientConn, int64, error) {
	if !sandboxHostSocketReachable(c.socketPath) {
		if c.mode == SandboxHostModeExternal {
			return nil, nil, 0, fmt.Errorf("sandbox-host socket %q is not reachable (mode %q refuses to spawn; the host must be started externally)", c.socketPath, c.mode)
		}
		if err := spawnSandboxHost(c.socketPath, c.dataRoot); err != nil {
			return nil, nil, 0, err
		}
		if err := waitSandboxHostSocket(ctx, c.socketPath); err != nil {
			return nil, nil, 0, err
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
	c.protocolVersion = 0
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
	// the daemon's shutdown and container stop.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start sandbox-host: %w", err)
	}
	// Reap the host when it eventually exits so a crash does not leave a zombie
	// under the daemon. Setsid already detached it, so this only collects its
	// exit status; it does not make the host die with the daemon.
	go func() { _ = cmd.Wait() }()
	return nil
}
