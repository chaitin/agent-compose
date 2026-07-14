package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

func TestDriverConversionWorkflows(t *testing.T) {
	if ToDriverSandbox(nil) != nil {
		t.Fatalf("nil sandbox should map to nil")
	}
	now := time.Date(2026, 7, 4, 8, 0, 0, 0, time.UTC)
	session := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID: "sandbox-1", Driver: "docker", GuestImage: "guest:latest", RuntimeRef: "runtime-1",
			WorkspacePath: "/workspace", CreatedAt: now, UpdatedAt: now,
		},
		EnvItems:        []domain.SandboxEnvVar{{Name: "A", Value: "B", Secret: true}},
		RuntimeEnvItems: []domain.SandboxEnvVar{{Name: "R", Value: "V"}},
		NetworkState: &domain.SandboxNetworkState{
			Attachments: []domain.SandboxNetworkEndpoint{{Name: "frontend", RuntimeNetworkName: "project_frontend"}},
			Bindings:    []domain.SandboxPortBinding{{Networks: []string{"frontend"}, HostIP: "10.254.1.1", HostPort: 32000, GuestPort: 8080, Protocol: "tcp", Visibility: "internal", Publisher: "docker"}},
		},
	}
	driverSession := ToDriverSandbox(session)
	if driverSession.Summary.ID != "sandbox-1" || len(driverSession.EnvItems) != 1 || !driverSession.EnvItems[0].Secret || len(driverSession.RuntimeEnvItems) != 1 {
		t.Fatalf("driver sandbox = %#v", driverSession)
	}
	if driverSession.Network == nil || len(driverSession.Network.Attachments) != 1 || len(driverSession.Network.Bindings) != 1 || driverSession.Network.Bindings[0].HostPort != 32000 {
		t.Fatalf("driver network = %#v", driverSession.Network)
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
	info := FromDriverSandboxVMInfo(driverpkg.SandboxVMInfo{BoxID: "box-id", JupyterURL: "http://jupyter", ProxyState: &driverProxyState})
	if info.BoxID != "box-id" || info.ProxyState == nil || info.ProxyState.Token != "token" {
		t.Fatalf("session vm info = %#v", info)
	}
	result := FromDriverExecResult(driverpkg.ExecResult{ExitCode: 2, Stdout: "out", Stderr: "err", Output: "outerr", Success: false})
	if result.ExitCode != 2 || result.Output != "outerr" || result.Success {
		t.Fatalf("exec result = %#v", result)
	}
	metricValue := 12.5
	stats := FromDriverSandboxStats(driverpkg.SandboxStats{
		SandboxID:        "sandbox-1",
		Driver:           "docker",
		SampledAt:        now,
		CPUPercent:       driverpkg.MetricValue{Value: &metricValue, Unit: driverpkg.MetricUnitPercent, Status: driverpkg.MetricStatusOK, Message: "cpu"},
		MemoryUsageBytes: driverpkg.MetricValue{Value: &metricValue, Unit: driverpkg.MetricUnitBytes, Status: driverpkg.MetricStatusOK},
		MemoryLimitBytes: driverpkg.MetricValue{Status: driverpkg.MetricStatusUnknown, Unit: driverpkg.MetricUnitBytes, Message: "unknown"},
		MemoryPercent:    driverpkg.MetricValue{Value: &metricValue, Unit: driverpkg.MetricUnitPercent, Status: driverpkg.MetricStatusOK},
		NetworkRxBytes:   driverpkg.MetricValue{Value: &metricValue, Unit: driverpkg.MetricUnitBytes, Status: driverpkg.MetricStatusOK},
		NetworkTxBytes:   driverpkg.MetricValue{Value: &metricValue, Unit: driverpkg.MetricUnitBytes, Status: driverpkg.MetricStatusOK},
		BlockReadBytes:   driverpkg.MetricValue{Value: &metricValue, Unit: driverpkg.MetricUnitBytes, Status: driverpkg.MetricStatusOK},
		BlockWriteBytes:  driverpkg.MetricValue{Value: &metricValue, Unit: driverpkg.MetricUnitBytes, Status: driverpkg.MetricStatusOK},
		UptimeSeconds:    driverpkg.MetricValue{Value: &metricValue, Unit: driverpkg.MetricUnitSeconds, Status: driverpkg.MetricStatusOK},
	})
	if stats.SandboxID != "sandbox-1" || stats.CPUPercent.Value == nil || *stats.CPUPercent.Value != metricValue || stats.MemoryLimitBytes.Status != driverpkg.MetricStatusUnknown {
		t.Fatalf("sandbox stats = %#v", stats)
	}

	config := &appconfig.Config{GuestWorkspacePath: "/workspace", GuestStateRoot: "/state", GuestRuntimeRoot: "/runtime", Version: "v-test"}
	commandReq := RuntimeCommandRequestPayload(config, domain.LoaderCommandRequest{
		Mode: "SHELL", Script: "echo ok", Env: map[string]string{"A": "B"},
	}, "/state/cells/cell-1")
	if commandReq.Mode != "shell" || commandReq.Cwd != "/workspace" || commandReq.MaxOutputBytes != DefaultLoaderCommandMaxOutputBytes {
		t.Fatalf("runtime command request = %#v", commandReq)
	}
	session.EnvItems = []domain.SandboxEnvVar{{Name: "USER_VAR", Value: "ok"}, {Name: "LLM_API_KEY", Value: "secret"}}
	session.RuntimeEnvItems = []domain.SandboxEnvVar{{Name: "MANAGED", Value: "yes"}}
	env := BuildSandboxExecEnv(config, session, "/home/agent")
	if env["USER_VAR"] != "ok" || env["LLM_API_KEY"] != "" || env["MANAGED"] != "yes" || env["SANDBOX_ID"] != "sandbox-1" {
		t.Fatalf("sandbox exec env = %#v", env)
	}
	if env["SESSION_ID"] != "" {
		t.Fatalf("SESSION_ID should not be injected: %#v", env)
	}
	execSpec := BuildLoaderCommandExecSpec(config, session, "/state/cells/cell-1/request.json", "/home/agent")
	if execSpec.Command != "sh" || len(execSpec.Args) != 2 || !strings.Contains(execSpec.Args[1], "agent-compose-runtime exec") || execSpec.Cwd != "/workspace" {
		t.Fatalf("loader command spec = %#v", execSpec)
	}
	artifactDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(artifactDir, "stdout.txt"), []byte("existing"), 0o644); err != nil {
		t.Fatalf("write existing artifact: %v", err)
	}
	if err := MirrorRuntimeCommandArtifacts(artifactDir, domain.RuntimeCommandResult{Stdout: "new", Stderr: "err", Output: "out"}); err != nil {
		t.Fatalf("MirrorRuntimeCommandArtifacts returned error: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(artifactDir, "stdout.txt")); err != nil || string(got) != "existing" {
		t.Fatalf("stdout artifact = %q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(artifactDir, "stderr.txt")); err != nil || string(got) != "err" {
		t.Fatalf("stderr artifact = %q err=%v", got, err)
	}
}
