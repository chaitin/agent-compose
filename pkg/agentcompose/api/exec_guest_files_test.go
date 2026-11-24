package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// recordingGuestFileRuntime is an ExecRuntime that also accepts guest file
// writes, like the k8s driver does. Drivers backed by a shared mount (docker,
// boxlite, microsandbox) deliberately do not implement WriteGuestFile.
type recordingGuestFileRuntime struct {
	apiExecRuntime
	guestPath string
	content   []byte
	writeErr  error
}

func (r *recordingGuestFileRuntime) WriteGuestFile(_ context.Context, _ *domain.Sandbox, _ domain.VMState, guestPath string, content []byte) error {
	r.guestPath = guestPath
	r.content = append([]byte(nil), content...)
	return r.writeErr
}

// The exec command request is persisted on the daemon and then read back by the
// runtime at a guest path. A daemon-local write is only visible when the driver
// has a shared mount, so drivers without one must receive the exact artifact.
// Before this, k8s exec always failed with a missing command-request.json.
func TestPrepareExecCommandPushesRequestToDriversWithoutSharedMount(t *testing.T) {
	sandboxRoot := t.TempDir()
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{
		ID: "sandbox-guest-file", VMStatus: domain.VMStatusRunning, WorkspacePath: filepath.Join(sandboxRoot, "workspace"),
	}}
	runtime := &recordingGuestFileRuntime{}
	handler := NewExecHandler(ExecHandlerDeps{
		Config: &appconfig.Config{DataRoot: t.TempDir()},
		Store:  &apiExecSandboxStore{sandbox: sandbox, vm: domain.VMState{Driver: "k8s"}},
		Runtime: func(*domain.Sandbox) (ExecRuntime, error) {
			return runtime, nil
		},
	})

	prep, err := handler.prepareExecCommand(context.Background(), sandbox, &agentcomposev2.ExecRequest{
		Command: &agentcomposev2.ExecCommand{Command: "echo", Args: []string{"hi"}},
	}, "exec-guest-file")
	if err != nil {
		t.Fatalf("prepareExecCommand: %v", err)
	}

	wantGuestPath := filepath.Join(prep.GuestExecDir, "command-request.json")
	if runtime.guestPath != wantGuestPath {
		t.Fatalf("guest file path = %q, want %q", runtime.guestPath, wantGuestPath)
	}
	hostArtifact, err := os.ReadFile(filepath.Join(prep.HostExecDir, "command-request.json"))
	if err != nil {
		t.Fatalf("read host command request artifact: %v", err)
	}
	if string(runtime.content) != string(hostArtifact) {
		t.Fatalf("guest content = %s, want the host artifact %s", runtime.content, hostArtifact)
	}
	var payload map[string]any
	if err := json.Unmarshal(runtime.content, &payload); err != nil {
		t.Fatalf("guest content is not the runtime command request: %v", err)
	}
	if len(payload) == 0 {
		t.Fatalf("guest content = %s, want a non-empty runtime command request", runtime.content)
	}
}

// A driver with a shared mount must not receive a copy: it reads the daemon
// artifact directly, and pushing it would be redundant work on every exec.
func TestPrepareExecCommandSkipsGuestPushForSharedMountDrivers(t *testing.T) {
	sandboxRoot := t.TempDir()
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{
		ID: "sandbox-shared", VMStatus: domain.VMStatusRunning, WorkspacePath: filepath.Join(sandboxRoot, "workspace"),
	}}
	handler := NewExecHandler(ExecHandlerDeps{
		Config: &appconfig.Config{DataRoot: t.TempDir()},
		Store:  &apiExecSandboxStore{sandbox: sandbox, vm: domain.VMState{Driver: "docker"}},
		Runtime: func(*domain.Sandbox) (ExecRuntime, error) {
			return &apiExecRuntime{}, nil
		},
	})

	if _, err := handler.prepareExecCommand(context.Background(), sandbox, &agentcomposev2.ExecRequest{
		Command: &agentcomposev2.ExecCommand{Command: "echo"},
	}, "exec-shared"); err != nil {
		t.Fatalf("prepareExecCommand: %v", err)
	}
}

// Exec is non-persistent, so a request that fails before the command starts
// must not leave its exec dir (and the env in command-request.json) behind.
func TestPrepareExecCommandRemovesExecDirOnFailure(t *testing.T) {
	sandboxRoot := t.TempDir()
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{
		ID: "sandbox-guest-file-fail", VMStatus: domain.VMStatusRunning, WorkspacePath: filepath.Join(sandboxRoot, "workspace"),
	}}
	runtime := &recordingGuestFileRuntime{writeErr: errors.New("push failed")}
	handler := NewExecHandler(ExecHandlerDeps{
		Config: &appconfig.Config{DataRoot: t.TempDir()},
		Store:  &apiExecSandboxStore{sandbox: sandbox, vm: domain.VMState{Driver: "k8s"}},
		Runtime: func(*domain.Sandbox) (ExecRuntime, error) {
			return runtime, nil
		},
	})

	if _, err := handler.prepareExecCommand(context.Background(), sandbox, &agentcomposev2.ExecRequest{
		Command: &agentcomposev2.ExecCommand{Command: "echo", Args: []string{"hi"}},
	}, "exec-guest-file-fail"); err == nil {
		t.Fatal("prepareExecCommand succeeded, want the guest push error")
	}
	if runtime.guestPath == "" {
		t.Fatal("guest push was not attempted")
	}
	entries, err := os.ReadDir(filepath.Join(sandboxRoot, "state", "exec"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read exec root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("exec dir left behind after failure: %v", entries)
	}
}
