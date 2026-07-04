package execution

import (
	"testing"
	"time"

	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

func TestDriverConversionWorkflows(t *testing.T) {
	if ToDriverSession(nil) != nil {
		t.Fatalf("nil session should map to nil")
	}
	now := time.Date(2026, 7, 4, 8, 0, 0, 0, time.UTC)
	session := &domain.Session{
		Summary: domain.SessionSummary{
			ID: "session-1", Driver: "docker", GuestImage: "guest:latest", RuntimeRef: "runtime-1",
			WorkspacePath: "/workspace", CreatedAt: now, UpdatedAt: now,
		},
		EnvItems:        []domain.SessionEnvVar{{Name: "A", Value: "B", Secret: true}},
		RuntimeEnvItems: []domain.SessionEnvVar{{Name: "R", Value: "V"}},
	}
	driverSession := ToDriverSession(session)
	if driverSession.Summary.ID != "session-1" || len(driverSession.EnvItems) != 1 || !driverSession.EnvItems[0].Secret || len(driverSession.RuntimeEnvItems) != 1 {
		t.Fatalf("driver session = %#v", driverSession)
	}
	vmState := domain.VMState{Driver: "docker", Mode: "runtime", BoxName: "box", BoxID: "box-id", Image: "image", Registry: "registry", RuntimeHome: "/root", StartedAt: now, StoppedAt: now, LastError: "none", BootstrapRef: "boot"}
	driverVMState := ToDriverVMState(vmState)
	if got := FromDriverVMState(driverVMState); got != vmState {
		t.Fatalf("vm state round trip = %#v", got)
	}
	proxyState := domain.ProxyState{ProxyPath: "/jupyter", GuestHost: "127.0.0.1", HostPort: 7410, GuestPort: 8888, JupyterURL: "http://guest", Token: "token"}
	driverProxyState := ToDriverProxyState(proxyState)
	if got := FromDriverProxyState(driverProxyState); got != proxyState {
		t.Fatalf("proxy state round trip = %#v", got)
	}
	spec := ToDriverExecSpec(domain.ExecSpec{Command: "echo", Args: []string{"ok"}, Env: map[string]string{"A": "B"}, Cwd: "/workspace"})
	if spec.Command != "echo" || spec.Args[0] != "ok" || spec.Env["A"] != "B" || spec.Cwd != "/workspace" {
		t.Fatalf("exec spec = %#v", spec)
	}
	info := FromDriverSessionVMInfo(driverpkg.SessionVMInfo{BoxID: "box-id", JupyterURL: "http://jupyter", ProxyState: &driverProxyState})
	if info.BoxID != "box-id" || info.ProxyState == nil || info.ProxyState.Token != "token" {
		t.Fatalf("session vm info = %#v", info)
	}
	result := FromDriverExecResult(driverpkg.ExecResult{ExitCode: 2, Stdout: "out", Stderr: "err", Output: "outerr", Success: false})
	if result.ExitCode != 2 || result.Output != "outerr" || result.Success {
		t.Fatalf("exec result = %#v", result)
	}
}
