package driver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/sandboxsupervisor"
	containerapi "github.com/docker/docker/api/types/container"
)

const (
	// dockerSandboxSupervisorLabel marks a container created with the
	// independent supervisor. A reused container must carry it; otherwise it
	// was created with a wider network and cannot be retrofitted in place.
	dockerSandboxSupervisorLabel = "agent-compose.sandbox_supervisor"
	// sandboxSupervisorGuestSocket is the path init sees through the existing
	// /run/agent-compose bind mount.
	sandboxSupervisorGuestSocket = "/run/agent-compose/supervisor.sock"
	// sandboxSupervisorSocketName is the host-side socket within the mounted
	// sandbox control directory.
	sandboxSupervisorSocketName = "supervisor.sock"
	// sandboxSupervisorTokenFile is the host-only token file name. It lives in
	// a sibling directory that is not bind-mounted into the workload.
	sandboxSupervisorTokenFile = "token"
	// sandboxSupervisorLogFile captures the supervisor's stderr.
	sandboxSupervisorLogFile = "supervisor.log"
	// sandboxSupervisorPIDFile records the process id so a restarted daemon can
	// retire a supervisor it did not start before launching a replacement.
	sandboxSupervisorPIDFile = "supervisor.pid"
	// sandboxSupervisorReadyTimeout bounds waiting for a newly started
	// supervisor to answer on its socket.
	sandboxSupervisorReadyTimeout = 15 * time.Second
	// sandboxSupervisorStopTimeout bounds the graceful stop before SIGKILL.
	sandboxSupervisorStopTimeout = 5 * time.Second
	// sandboxSupervisorEnvName is the operator switch validated by pkg/config.
	sandboxSupervisorEnvName = "SANDBOX_SUPERVISOR"
	// sandboxSupervisorTokenBytes is the entropy of the per-sandbox token.
	sandboxSupervisorTokenBytes = 32
)

// SandboxSupervisorEnabled reports whether the deployment selected the
// independent Docker supervisor. The mode is only valid with init and
// init-mediated egress; configuration validation rejects the partial forms.
func SandboxSupervisorEnabled(config *appconfig.Config) bool {
	return config != nil && config.SandboxSupervisorEnabled
}

// dockerSandboxUsesSupervisor reports whether an existing container was
// created with the supervisor. The label, not the current config, decides:
// turning the switch off must not break a sandbox that already has one.
func dockerSandboxUsesSupervisor(info containerapi.InspectResponse) bool {
	return info.Config != nil && info.Config.Labels != nil && info.Config.Labels[dockerSandboxSupervisorLabel] == "true"
}

// dockerSupervisorLaunch carries the create-time values from the container
// environment builder to the supervisor start that must happen before the
// container is started.
type dockerSupervisorLaunch struct {
	Token           string
	ProxyURL        string
	DirectEndpoints []string
	EngineHostsJSON string
}

// dockerSupervisorProcess is one running supervisor process.
type dockerSupervisorProcess struct {
	cmd  supervisorCommand
	pid  int
	done chan struct{}
}

// supervisorCommand is the tiny part of *exec.Cmd the process code needs.
type supervisorCommand interface {
	Signal(os.Signal) error
	Kill() error
}

// dockerSupervisorManager owns the running supervisors created by this daemon
// process. A supervisor that survives a daemon restart is discovered by
// pinging its socket; the manager only tracks processes this process started.
type dockerSupervisorManager struct {
	mu        sync.Mutex
	processes map[string]*dockerSupervisorProcess
}

func newDockerSupervisorManager() *dockerSupervisorManager {
	return &dockerSupervisorManager{processes: make(map[string]*dockerSupervisorProcess)}
}

// supervisorProcesses returns the manager, initializing it for a runtime built
// as a struct literal in a test or by an older constructor.
func (r *dockerRuntime) supervisorProcesses() *dockerSupervisorManager {
	if r.supervisors == nil {
		r.supervisors = newDockerSupervisorManager()
	}
	return r.supervisors
}

func (m *dockerSupervisorManager) get(sandboxID string) (*dockerSupervisorProcess, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	process, ok := m.processes[sandboxID]
	return process, ok
}

func (m *dockerSupervisorManager) put(sandboxID string, process *dockerSupervisorProcess) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.processes[sandboxID]; ok && existing != process {
		_ = existing.cmd.Kill()
	}
	m.processes[sandboxID] = process
}

func (m *dockerSupervisorManager) take(sandboxID string) (*dockerSupervisorProcess, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	process, ok := m.processes[sandboxID]
	if ok {
		delete(m.processes, sandboxID)
	}
	return process, ok
}

func (p *dockerSupervisorProcess) alive() bool {
	if p == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// sandboxSupervisorSocketHostPath is the host path of the per-sandbox
// supervisor socket inside the mounted control directory.
func (r *dockerRuntime) sandboxSupervisorSocketHostPath(sandbox *Sandbox) string {
	return filepath.Join(r.sandboxInitControlHostDir(sandbox), sandboxSupervisorSocketName)
}

// sandboxSupervisorStateDir is host-only state (token and log). It is a
// sibling of the mounted control directory, so the workload container cannot
// read the token even when it runs as root.
func (r *dockerRuntime) sandboxSupervisorStateDir(sandbox *Sandbox) string {
	root := "/run/agent-compose"
	if r != nil && r.config != nil && strings.TrimSpace(r.config.SandboxInitSocketDir) != "" {
		root = strings.TrimSpace(r.config.SandboxInitSocketDir)
	}
	return filepath.Join(root, "."+sandbox.Summary.ID+".supervisor")
}

func (r *dockerRuntime) sandboxSupervisorTokenPath(sandbox *Sandbox) string {
	return filepath.Join(r.sandboxSupervisorStateDir(sandbox), sandboxSupervisorTokenFile)
}

// sandboxSupervisorToken returns the per-sandbox token, creating it once with
// 0600 permissions. The token is passed to init through its environment and
// removed before any workload process starts.
func (r *dockerRuntime) sandboxSupervisorToken(sandbox *Sandbox) (string, error) {
	path := r.sandboxSupervisorTokenPath(sandbox)
	if data, err := os.ReadFile(path); err == nil {
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", fmt.Errorf("sandbox supervisor token file %s is empty", path)
		}
		return token, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read sandbox supervisor token %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create sandbox supervisor state directory %s: %w", dir, err)
	}
	token, err := randomSupervisorToken()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("write sandbox supervisor token %s: %w", path, err)
	}
	return token, nil
}

func randomSupervisorToken() (string, error) {
	buffer := make([]byte, sandboxSupervisorTokenBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate sandbox supervisor token: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

// ensureSandboxSupervisor starts this sandbox's supervisor when it is not
// already reachable. A launch value from the container-creation path carries
// the credential that was just minted; on reuse the credential is minted here.
func (r *dockerRuntime) ensureSandboxSupervisor(ctx context.Context, sandbox *Sandbox, launch *dockerSupervisorLaunch) error {
	if sandbox == nil {
		return errors.New("sandbox is required")
	}
	sandboxID := sandbox.Summary.ID
	if process, ok := r.supervisorProcesses().get(sandboxID); ok && process.alive() {
		return nil
	}
	socketPath := r.sandboxSupervisorSocketHostPath(sandbox)
	if len(socketPath) > appconfig.MaxSandboxInitSocketPathLength {
		return fmt.Errorf("sandbox supervisor socket path is too long (%d bytes, limit %d): %s; set SANDBOX_INIT_SOCKET_DIR to a shorter runtime directory", len(socketPath), appconfig.MaxSandboxInitSocketPathLength, socketPath)
	}
	token := ""
	proxyURL := ""
	var direct []string
	if launch != nil {
		token, proxyURL, direct = launch.Token, launch.ProxyURL, launch.DirectEndpoints
	}
	if token == "" {
		value, err := r.sandboxSupervisorToken(sandbox)
		if err != nil {
			return err
		}
		token = value
	}
	if proxyURL == "" {
		items, err := sandboxProxyEnvItems(ctx, sandbox)
		if err != nil {
			return err
		}
		proxyURL = sandboxSupervisorProxyURL(items)
	}
	if proxyURL == "" {
		return fmt.Errorf("sandbox supervisor for sandbox %s has no daemon proxy credential; the workload has no network and would be unable to reach any destination", sandboxID)
	}
	if direct == nil {
		endpoints, _, err := buildSandboxSupervisorEndpoints(proxyURL, r.config.AgentTelemetry.Endpoint, nil)
		if err != nil {
			return err
		}
		direct = endpoints
	}
	stateDir := r.sandboxSupervisorStateDir(sandbox)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create sandbox supervisor state directory %s: %w", stateDir, err)
	}
	binary, err := r.sandboxSupervisorBinary()
	if err != nil {
		return err
	}
	if err := r.retireStaleSandboxSupervisor(sandbox); err != nil {
		slog.Warn("failed to retire a stale sandbox supervisor before starting a replacement", "sandbox_id", sandboxID, "error", err)
	}
	uid, gid := 0, 0
	if r.config != nil {
		uid, gid = r.config.SandboxSupervisorUID, r.config.SandboxSupervisorGID
	}
	if uid > 0 {
		controlDir := filepath.Dir(socketPath)
		if err := os.Chmod(filepath.Dir(controlDir), 0o711); err != nil {
			return fmt.Errorf("make the supervisor runtime root traversable: %w", err)
		}
		if err := os.Chown(controlDir, uid, gid); err != nil {
			return fmt.Errorf("chown the supervisor control directory: %w", err)
		}
		if err := os.Chmod(controlDir, 0o700); err != nil {
			return fmt.Errorf("set supervisor control directory mode: %w", err)
		}
	}
	process, err := startDockerSupervisorProcess(dockerSupervisorStart{
		Binary:          binary,
		SandboxID:       sandboxID,
		SocketPath:      socketPath,
		Token:           token,
		ProxyURL:        proxyURL,
		DirectEndpoints: direct,
		LogPath:         filepath.Join(stateDir, sandboxSupervisorLogFile),
		UID:             uid,
		GID:             gid,
	})
	if err != nil {
		return err
	}
	if err := writeSandboxSupervisorPID(filepath.Join(stateDir, sandboxSupervisorPIDFile), process.pid); err != nil {
		_ = stopDockerSupervisorProcess(process, sandboxSupervisorStopTimeout)
		return err
	}
	if err := waitForSandboxSupervisor(ctx, process, socketPath, token, sandboxID); err != nil {
		_ = stopDockerSupervisorProcess(process, sandboxSupervisorStopTimeout)
		return err
	}
	if uid > 0 {
		// The socket is bound; the supervisor no longer needs directory access.
		// Restore the root-only control directory before any workload starts.
		_ = os.Chown(filepath.Dir(socketPath), 0, 0)
	}
	r.supervisorProcesses().put(sandboxID, process)
	return nil
}

// sandboxSupervisorBinary locates ac-supervisor: explicit configuration first,
// then the directory of the running daemon, then the image path.
func (r *dockerRuntime) sandboxSupervisorBinary() (string, error) {
	if r != nil && r.config != nil && strings.TrimSpace(r.config.SandboxSupervisorBinary) != "" {
		return strings.TrimSpace(r.config.SandboxSupervisorBinary), nil
	}
	if executable, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(executable), "ac-supervisor")
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	const imagePath = "/app/ac-supervisor"
	if info, err := os.Stat(imagePath); err == nil && !info.IsDir() {
		return imagePath, nil
	}
	return "", fmt.Errorf("ac-supervisor executable was not found next to the daemon or at %s; set SANDBOX_SUPERVISOR_BINARY", imagePath)
}

// waitForSandboxSupervisor pings the socket until the process answers or the
// bounded wait expires. A process that exits early fails immediately.
func waitForSandboxSupervisor(ctx context.Context, process *dockerSupervisorProcess, socketPath, token, sandboxID string) error {
	waitCtx, cancel := context.WithTimeout(ctx, sandboxSupervisorReadyTimeout)
	defer cancel()
	client := &sandboxsupervisor.Client{
		SocketPath: socketPath,
		Token:      token,
		SandboxID:  sandboxID,
		Timeout:    time.Second,
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := client.Ping(waitCtx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-process.done:
			return fmt.Errorf("sandbox supervisor for sandbox %s exited before becoming ready: %s", sandboxID, supervisorLogTail(socketPath))
		case <-waitCtx.Done():
			return fmt.Errorf("sandbox supervisor for sandbox %s did not become ready within %s: %w%s", sandboxID, sandboxSupervisorReadyTimeout, lastErr, supervisorLogTail(socketPath))
		case <-ticker.C:
		}
	}
}

// supervisorLogTail returns the tail of the matching supervisor log so a start
// failure names the actual cause. It never includes a token.
func supervisorLogTail(socketPath string) string {
	// The log is a sibling of the mounted directory that holds the socket.
	dir := filepath.Dir(socketPath)
	base := filepath.Base(dir)
	root := filepath.Dir(dir)
	logPath := filepath.Join(root, "."+base+".supervisor", sandboxSupervisorLogFile)
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(data))
	if len(text) > 4096 {
		text = text[len(text)-4096:]
	}
	if text == "" {
		return ""
	}
	return "\nsupervisor log:\n" + text
}

// retireStaleSandboxSupervisor kills a supervisor left by an earlier daemon
// process. It never touches the token, so the replacement uses the same token
// the running init process was configured with.
func (r *dockerRuntime) retireStaleSandboxSupervisor(sandbox *Sandbox) error {
	stateDir := r.sandboxSupervisorStateDir(sandbox)
	pidPath := filepath.Join(stateDir, sandboxSupervisorPIDFile)
	data, err := os.ReadFile(pidPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read sandbox supervisor pid file %s: %w", pidPath, err)
	}
	if err == nil {
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr != nil {
			return fmt.Errorf("parse sandbox supervisor pid file %s: %w", pidPath, parseErr)
		}
		if pid > 0 && supervisorProcessLooksLikeOurs(pid) {
			if process, findErr := os.FindProcess(pid); findErr == nil {
				_ = process.Kill()
			}
		}
		_ = os.Remove(pidPath)
	}
	_ = os.Remove(r.sandboxSupervisorSocketHostPath(sandbox))
	return nil
}

// supervisorProcessLooksLikeOurs prevents a recycled pid from making the
// daemon kill an unrelated process. The state directory is root-owned, but pid
// reuse across a daemon restart is still possible.
func supervisorProcessLooksLikeOurs(pid int) bool {
	commandLine, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	return strings.Contains(string(commandLine), "ac-supervisor")
}

// writeSandboxSupervisorPID persists the process id for a later daemon start.
func writeSandboxSupervisorPID(path string, pid int) error {
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		return fmt.Errorf("write sandbox supervisor pid file %s: %w", path, err)
	}
	return nil
}

// stopSandboxSupervisor stops the tracked process and removes its socket. The
// token file is intentionally kept: a stopped-and-resumed container keeps the
// token init read at creation, so the supervisor can restart with it.
func (r *dockerRuntime) stopSandboxSupervisor(sandbox *Sandbox) {
	if sandbox == nil {
		return
	}
	if process, ok := r.supervisorProcesses().take(sandbox.Summary.ID); ok {
		_ = stopDockerSupervisorProcess(process, sandboxSupervisorStopTimeout)
	} else if err := r.retireStaleSandboxSupervisor(sandbox); err != nil {
		slog.Warn("failed to stop an untracked sandbox supervisor", "sandbox_id", sandbox.Summary.ID, "error", err)
	}
	_ = os.Remove(filepath.Join(r.sandboxSupervisorStateDir(sandbox), sandboxSupervisorPIDFile))
	_ = os.Remove(r.sandboxSupervisorSocketHostPath(sandbox))
}

// removeSandboxSupervisorRuntime stops the process and deletes the host-only
// token and log as well. It is used when the sandbox itself is removed.
func (r *dockerRuntime) removeSandboxSupervisorRuntime(sandbox *Sandbox) {
	if sandbox == nil {
		return
	}
	r.stopSandboxSupervisor(sandbox)
	_ = os.RemoveAll(r.sandboxSupervisorStateDir(sandbox))
}

// dockerSupervisorStart is the process-start request.
type dockerSupervisorStart struct {
	Binary          string
	SandboxID       string
	SocketPath      string
	Token           string
	ProxyURL        string
	DirectEndpoints []string
	LogPath         string
	UID             int
	GID             int
}

// startDockerSupervisorProcess starts ac-supervisor in its own session. The
// implementation is platform-specific because the supervisor isolation mode is
// a Linux feature.
func startDockerSupervisorProcess(request dockerSupervisorStart) (*dockerSupervisorProcess, error) {
	return startSupervisorProcess(request)
}

// stopDockerSupervisorProcess terminates one supervisor and waits for it.
func stopDockerSupervisorProcess(process *dockerSupervisorProcess, timeout time.Duration) error {
	return stopSupervisorProcess(process, timeout)
}
