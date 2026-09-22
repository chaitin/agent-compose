package runs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

type guestFileControllerRuntime struct {
	fakeControllerRuntime
	writtenPath string
	writtenData []byte
	execStarted bool
	pulledDir   string
}

func (r *guestFileControllerRuntime) WriteGuestFile(_ context.Context, _ *domain.Sandbox, _ domain.VMState, guestPath string, content []byte) error {
	if r.execStarted {
		return os.ErrInvalid
	}
	r.writtenPath = guestPath
	r.writtenData = append([]byte(nil), content...)
	return nil
}

func (r *guestFileControllerRuntime) ExecStream(ctx context.Context, sandbox *domain.Sandbox, state domain.VMState, spec domain.ExecSpec, writer domain.ExecStreamWriter) (domain.ExecResult, error) {
	r.execStarted = true
	return r.fakeControllerRuntime.ExecStream(ctx, sandbox, state, spec, writer)
}

func (r *guestFileControllerRuntime) ReadGuestDir(_ context.Context, _ *domain.Sandbox, _ domain.VMState, guestDir, hostDestDir string) error {
	if !r.execStarted {
		return os.ErrInvalid
	}
	r.pulledDir = guestDir
	return os.WriteFile(filepath.Join(hostDestDir, "command-result.json"), []byte("{\"success\":true}\n"), 0o644)
}

func TestExecuteProjectRunCommandTransfersGuestRequestAndArtifacts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:           root,
		SandboxRoot:        filepath.Join(root, "sandboxes"),
		RuntimeDriver:      driverpkg.RuntimeDriverK8s,
		DefaultImage:       "guest:latest",
		GuestWorkspacePath: "/workspace",
		GuestStateRoot:     "/state",
		GuestHomePath:      "/root",
	}
	store, err := sandboxstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	sandbox, err := store.CreateSandbox(ctx, "k8s run command", "", driverpkg.RuntimeDriverK8s, "guest:latest", "", domain.SandboxTypeScript, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	if err := store.SaveVMState(sandbox.Summary.ID, domain.VMState{Driver: driverpkg.RuntimeDriverK8s, BoxID: sandbox.Summary.ID}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}
	runtime := &guestFileControllerRuntime{}
	controller := &Controller{config: config, store: store, runtime: func(*domain.Sandbox) (Runtime, error) { return runtime, nil }}
	run := domain.ProjectRunRecord{RunID: "run-k8s", ProjectID: "project-1", AgentName: "worker"}
	transition, err := controller.executeProjectRunCommand(ctx, projectRunCommandExecution{Run: run, Sandbox: sandbox, CommandText: "echo run"})
	if err != nil {
		t.Fatalf("executeProjectRunCommand returned error: %v", err)
	}
	if transition.ExitCode != 0 || !strings.HasSuffix(runtime.writtenPath, "/runs/run-k8s/command-request.json") {
		t.Fatalf("transition = %#v, guest request path = %q", transition, runtime.writtenPath)
	}
	if !strings.Contains(string(runtime.writtenData), "echo run") {
		t.Fatalf("guest request content = %q", runtime.writtenData)
	}
	if runtime.pulledDir != filepath.Dir(runtime.writtenPath) {
		t.Fatalf("pulled guest dir = %q, want %q", runtime.pulledDir, filepath.Dir(runtime.writtenPath))
	}
	if data, readErr := os.ReadFile(filepath.Join(projectRunCommandArtifactsDir(run, sandbox), "command-result.json")); readErr != nil || !strings.Contains(string(data), `"success":true`) {
		t.Fatalf("pulled result artifact = %q, err = %v", data, readErr)
	}
}

func TestExecuteProjectRunCommandForwardsTraceContext(t *testing.T) {
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	ctx := domain.NewContextWithTraceContext(context.Background(), domain.TraceContext{Traceparent: traceparent})
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:           root,
		SandboxRoot:        filepath.Join(root, "sandboxes"),
		RuntimeDriver:      driverpkg.RuntimeDriverK8s,
		DefaultImage:       "guest:latest",
		GuestWorkspacePath: "/workspace",
		GuestStateRoot:     "/state",
		GuestHomePath:      "/root",
		AgentTelemetry:     appconfig.AgentTelemetryConfig{Endpoint: "http://collector:4318"},
	}
	store, err := sandboxstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	sandbox, err := store.CreateSandbox(ctx, "k8s run command", "", driverpkg.RuntimeDriverK8s, "guest:latest", "", domain.SandboxTypeScript, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	if err := store.SaveVMState(sandbox.Summary.ID, domain.VMState{Driver: driverpkg.RuntimeDriverK8s, BoxID: sandbox.Summary.ID}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}
	runtime := &guestFileControllerRuntime{}
	controller := &Controller{config: config, store: store, runtime: func(*domain.Sandbox) (Runtime, error) { return runtime, nil }}
	run := domain.ProjectRunRecord{RunID: "run-trace", ProjectID: "project-1", AgentName: "worker"}

	if _, err := controller.executeProjectRunCommand(ctx, projectRunCommandExecution{Run: run, Sandbox: sandbox, CommandText: "echo run"}); err != nil {
		t.Fatalf("executeProjectRunCommand returned error: %v", err)
	}

	var payload struct {
		Traceparent string            `json:"traceparent"`
		Attributes  map[string]string `json:"attributes"`
	}
	if err := json.Unmarshal([]byte(runtime.spec.Env["AGENT_COMPOSE_TELEMETRY"]), &payload); err != nil {
		t.Fatalf("decode agent telemetry payload: %v", err)
	}
	if payload.Traceparent != traceparent || payload.Attributes["agent_compose.sandbox.id"] != sandbox.Summary.ID {
		t.Fatalf("trace context was not relayed to the execution: %+v", payload)
	}
}
