//go:build linux

package driver

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/chaitin/agent-compose/pkg/sandboxsupervisor"
)

// startSupervisorProcess starts ac-supervisor in its own session. It does not
// use a parent-death signal: a supervisor that outlives a crashing daemon can
// be adopted on the next start because its token is persisted host-side.
func startSupervisorProcess(request dockerSupervisorStart) (*dockerSupervisorProcess, error) {
	if strings.TrimSpace(request.Binary) == "" {
		return nil, errors.New("ac-supervisor binary is required")
	}
	logFile, err := os.OpenFile(request.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open sandbox supervisor log %s: %w", request.LogPath, err)
	}
	command := exec.Command(request.Binary, "--socket", request.SocketPath, "--sandbox-id", request.SandboxID)
	command.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		sandboxsupervisor.EnvToken + "=" + request.Token,
		sandboxsupervisor.EnvProxy + "=" + request.ProxyURL,
		sandboxsupervisor.EnvDirectEndpoints + "=" + strings.Join(request.DirectEndpoints, ","),
	}
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if request.UID > 0 {
		command.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(request.UID), Gid: uint32(request.GID), Groups: []uint32{uint32(request.GID)}}
	}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("start sandbox supervisor %s: %w", request.Binary, err)
	}
	process := &dockerSupervisorProcess{cmd: command.Process, pid: command.Process.Pid, done: make(chan struct{})}
	go func() {
		_ = command.Wait()
		_ = logFile.Close()
		close(process.done)
	}()
	return process, nil
}

// stopSupervisorProcess asks the supervisor to exit, then kills it if the
// bounded wait expires.
func stopSupervisorProcess(process *dockerSupervisorProcess, timeout time.Duration) error {
	if process == nil {
		return nil
	}
	select {
	case <-process.done:
		return nil
	default:
	}
	_ = process.cmd.Signal(syscall.SIGTERM)
	if timeout <= 0 {
		timeout = sandboxSupervisorStopTimeout
	}
	select {
	case <-process.done:
		return nil
	case <-time.After(timeout):
	}
	_ = process.cmd.Kill()
	select {
	case <-process.done:
	case <-time.After(time.Second):
	}
	return nil
}
