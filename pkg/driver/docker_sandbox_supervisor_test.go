package driver

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/sandboxinit"
	containerapi "github.com/docker/docker/api/types/container"
)

func TestSandboxSupervisorEnabled(t *testing.T) {
	if SandboxSupervisorEnabled(nil) {
		t.Fatal("SandboxSupervisorEnabled(nil) = true, want false")
	}
	if SandboxSupervisorEnabled(&appconfig.Config{}) {
		t.Fatal("SandboxSupervisorEnabled(zero config) = true, want false")
	}
	if !SandboxSupervisorEnabled(&appconfig.Config{SandboxSupervisorEnabled: true}) {
		t.Fatal("SandboxSupervisorEnabled(enabled config) = false, want true")
	}
}

func TestDockerSandboxUsesSupervisorLabel(t *testing.T) {
	withLabel := containerapi.InspectResponse{Config: &containerapi.Config{Labels: map[string]string{dockerSandboxSupervisorLabel: "true"}}}
	if !dockerSandboxUsesSupervisor(withLabel) {
		t.Fatal("dockerSandboxUsesSupervisor() = false for the supervisor label")
	}
	withoutLabel := containerapi.InspectResponse{Config: &containerapi.Config{Labels: map[string]string{}}}
	if dockerSandboxUsesSupervisor(withoutLabel) {
		t.Fatal("dockerSandboxUsesSupervisor() = true without the supervisor label")
	}
	if dockerSandboxUsesSupervisor(containerapi.InspectResponse{}) {
		t.Fatal("dockerSandboxUsesSupervisor(zero) = true")
	}
}

func TestValidateDockerSandboxSupervisorNetwork(t *testing.T) {
	hostConfig := func(mode string) *containerapi.HostConfig {
		return &containerapi.HostConfig{NetworkMode: containerapi.NetworkMode(mode)}
	}
	none := containerapi.InspectResponse{ContainerJSONBase: &containerapi.ContainerJSONBase{HostConfig: hostConfig("none")}}
	if err := validateDockerSandboxSupervisorNetwork(none); err != nil {
		t.Fatalf("validateDockerSandboxSupervisorNetwork(none) error = %v", err)
	}
	bridge := containerapi.InspectResponse{ContainerJSONBase: &containerapi.ContainerJSONBase{HostConfig: hostConfig("bridge")}}
	if err := validateDockerSandboxSupervisorNetwork(bridge); err == nil {
		t.Fatal("validateDockerSandboxSupervisorNetwork(bridge) = nil, want a refusal")
	}
}

func TestSandboxSupervisorProxyURL(t *testing.T) {
	items := []SandboxEnvVar{
		{Name: "HTTP_PROXY", Value: "http://first"},
		{Name: "HTTPS_PROXY", Value: "http://second"},
	}
	if got := sandboxSupervisorProxyURL(items); got != "http://second" {
		t.Fatalf("sandboxSupervisorProxyURL() = %q, want the HTTPS_PROXY value", got)
	}
	if got := sandboxSupervisorProxyURL(nil); got != "" {
		t.Fatalf("sandboxSupervisorProxyURL(nil) = %q, want empty", got)
	}
}

func TestBuildSandboxSupervisorEndpointsResolvesEngineHosts(t *testing.T) {
	resolve := func(host string) ([]net.IP, error) {
		switch host {
		case "agent-compose":
			return []net.IP{net.ParseIP("10.0.0.2")}, nil
		case "otel":
			return []net.IP{net.ParseIP("10.0.0.3")}, nil
		default:
			return nil, errors.New("unexpected host " + host)
		}
	}
	direct, hostsJSON, err := buildSandboxSupervisorEndpoints(
		"http://sandbox-1:secret@agent-compose:7410",
		"http://otel:4318/v1/traces",
		resolve,
	)
	if err != nil {
		t.Fatalf("buildSandboxSupervisorEndpoints() error = %v", err)
	}
	for _, want := range []string{"agent-compose:7410", "10.0.0.2:7410", "otel:4318", "10.0.0.3:4318"} {
		found := false
		for _, endpoint := range direct {
			if endpoint == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("direct endpoints %v do not contain %q", direct, want)
		}
	}
	var hosts map[string][]string
	if err := json.Unmarshal([]byte(hostsJSON), &hosts); err != nil {
		t.Fatalf("decode hosts JSON %q: %v", hostsJSON, err)
	}
	if got := hosts["agent-compose"]; len(got) != 1 || got[0] != "10.0.0.2" {
		t.Fatalf("hosts[agent-compose] = %v, want [10.0.0.2]", got)
	}
}

func TestBuildSandboxSupervisorEndpointsFailsClosedOnUnresolvableHost(t *testing.T) {
	resolve := func(string) ([]net.IP, error) { return nil, errors.New("no such host") }
	if _, _, err := buildSandboxSupervisorEndpoints("http://user:pass@missing.example:7410", "", resolve); err == nil {
		t.Fatal("buildSandboxSupervisorEndpoints() = nil, want a refusal for an unresolvable engine host")
	}
}

func TestSandboxSupervisorInitEnv(t *testing.T) {
	env := sandboxSupervisorInitEnv(`{"agent-compose":["10.0.0.2"]}`)
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		sandboxinit.EgressRelaySocketEnvVar + "=" + sandboxSupervisorGuestSocket,
		sandboxinit.EgressEngineHostsEnvVar + "=",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("init env %v does not contain %q", env, want)
		}
	}
	if strings.Contains(joined, "SANDBOX_INIT_EGRESS_RELAY_TOKEN") {
		t.Fatalf("init env %v carries the supervisor token; it must travel over the control socket", env)
	}
}

func TestSandboxSupervisorTokenIsPersistedWithPrivatePermissions(t *testing.T) {
	root := t.TempDir()
	runtime := &dockerRuntime{config: &appconfig.Config{SandboxInitSocketDir: root}, supervisors: newDockerSupervisorManager()}
	sandbox := &Sandbox{Summary: SandboxSummary{ID: "sandbox-1"}}
	first, err := runtime.sandboxSupervisorToken(sandbox)
	if err != nil {
		t.Fatalf("sandboxSupervisorToken() error = %v", err)
	}
	if len(first) != 2*sandboxSupervisorTokenBytes {
		t.Fatalf("token length = %d, want %d hex characters", len(first), 2*sandboxSupervisorTokenBytes)
	}
	second, err := runtime.sandboxSupervisorToken(sandbox)
	if err != nil {
		t.Fatalf("sandboxSupervisorToken() second call error = %v", err)
	}
	if second != first {
		t.Fatalf("token changed between calls: %q then %q", first, second)
	}
	info, err := os.Stat(runtime.sandboxSupervisorTokenPath(sandbox))
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("token mode = %o, want 600", mode)
	}
	dirInfo, err := os.Stat(filepath.Dir(runtime.sandboxSupervisorTokenPath(sandbox)))
	if err != nil {
		t.Fatalf("stat token directory: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Fatalf("token directory mode = %o, want 700", mode)
	}
}

func TestDockerSupervisorManagerTracksRunningProcesses(t *testing.T) {
	manager := newDockerSupervisorManager()
	command := &fakeSupervisorCommand{}
	process := &dockerSupervisorProcess{cmd: command, done: make(chan struct{})}
	manager.put("sandbox-1", process)
	if got, ok := manager.get("sandbox-1"); !ok || got != process {
		t.Fatalf("manager.get() = %v, %v, want the stored process", got, ok)
	}
	if !process.alive() {
		t.Fatal("process.alive() = false for a running process")
	}
	close(process.done)
	if process.alive() {
		t.Fatal("process.alive() = true after done closed")
	}
	if got, ok := manager.take("sandbox-1"); !ok || got != process {
		t.Fatalf("manager.take() = %v, %v, want the stored process", got, ok)
	}
	if _, ok := manager.get("sandbox-1"); ok {
		t.Fatal("manager.get() returned a taken process")
	}
}

func TestWaitForSandboxSupervisorFailsWhenTheProcessExits(t *testing.T) {
	done := make(chan struct{})
	close(done)
	process := &dockerSupervisorProcess{cmd: &fakeSupervisorCommand{}, done: done}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	socket := filepath.Join(t.TempDir(), "supervisor.sock")
	err := waitForSandboxSupervisor(ctx, process, socket, "token", "sandbox-1")
	if err == nil || !strings.Contains(err.Error(), "exited before becoming ready") {
		t.Fatalf("waitForSandboxSupervisor() error = %v, want an exited-process refusal", err)
	}
}

type fakeSupervisorCommand struct {
	mu      sync.Mutex
	signals []os.Signal
	killed  bool
}

func (f *fakeSupervisorCommand) Signal(signal os.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signals = append(f.signals, signal)
	return nil
}

func (f *fakeSupervisorCommand) Kill() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = true
	return nil
}
