package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
)

type fakeLoaderCommandRuntime struct {
	capturedSpec    *domain.ExecSpec
	capturedSession **domain.Sandbox
}

func (r fakeLoaderCommandRuntime) EnsureSandbox(context.Context, *domain.Sandbox, domain.VMState, domain.ProxyState) (domain.SandboxVMInfo, error) {
	return domain.SandboxVMInfo{}, nil
}

func (r fakeLoaderCommandRuntime) StopSandbox(context.Context, *domain.Sandbox, domain.VMState) (bool, error) {
	return false, nil
}

func (r fakeLoaderCommandRuntime) RemoveSandbox(context.Context, *domain.Sandbox, domain.VMState) error {
	return nil
}

func (r fakeLoaderCommandRuntime) Exec(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec) (domain.ExecResult, error) {
	return domain.ExecResult{}, nil
}

func (r fakeLoaderCommandRuntime) ExecStream(_ context.Context, session *domain.Sandbox, _ domain.VMState, spec domain.ExecSpec, stream domain.ExecStreamWriter) (domain.ExecResult, error) {
	if r.capturedSpec != nil {
		*r.capturedSpec = spec
	}
	if r.capturedSession != nil {
		*r.capturedSession = session
	}
	commandResult := domain.RuntimeCommandResult{
		Stdout:   "loader stdout\n",
		Stderr:   "loader stderr\n",
		Output:   "loader stdout\nloader stderr\n",
		ExitCode: 0,
		Success:  true,
	}
	payloadBytes, _ := json.Marshal(commandResult)
	payload := execution.CommandResultPrefix + string(payloadBytes) + "\n"
	if stream != nil {
		stream(domain.ExecChunk{Text: "loader stdout\n"})
		stream(domain.ExecChunk{Text: "loader stderr\n", Stream: domain.StdioStderr})
		stream(domain.ExecChunk{Text: payload})
	}
	return domain.ExecResult{
		Stdout:   "loader stdout\n" + payload,
		Stderr:   "loader stderr\n",
		Output:   "loader stdout\nloader stderr\n" + payload,
		ExitCode: 0,
		Success:  true,
	}, nil
}

func TestLoaderCommandExecutorProjectsOnlyFacadeCredentials(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:            root,
		DbAddr:              filepath.Join(root, "config.db"),
		SandboxRoot:         filepath.Join(root, "sandboxes"),
		RuntimeDriver:       driverpkg.RuntimeDriverDocker,
		DefaultImage:        "guest:latest",
		GuestWorkspacePath:  "/workspace",
		GuestStateRoot:      "/data/state",
		GuestHomePath:       "/root",
		RuntimeBaseURL:      "http://facade.internal:7410",
		SandboxStartTimeout: 2 * time.Second,
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	di := do.New()
	do.ProvideValue(di, config)
	configDB, err := configstore.NewConfigStore(di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "loader command sandbox", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SandboxTypeScript, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSandbox(ctx, session); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}

	const upstreamKey = "upstream-secret-must-not-enter-guest"
	var capturedSpec domain.ExecSpec
	var capturedSession *domain.Sandbox
	runtime := fakeLoaderCommandRuntime{capturedSpec: &capturedSpec, capturedSession: &capturedSession}
	executor := NewLoaderCommandExecutor(config, store, configDB, fakeRuntimeProvider{runtime: runtime}, sessions.NewStreamBrokerForTest())
	result, err := executor.ExecuteLoaderCommand(ctx, session, domain.LoaderCommandRequest{
		Mode:   "shell",
		Script: "echo loader",
		Env: map[string]string{
			"AGENT_PROVIDER": "codex",
			"CODEX_MODEL":    "test-model",
			"LLM_API_KEY":    upstreamKey,
		},
		SandboxEnv: []domain.SandboxEnvVar{
			{Name: "LLM_API_ENDPOINT", Value: "https://upstream.example/v1"},
			{Name: "LLM_API_PROTOCOL", Value: "chat_completions"},
			{Name: "LLM_API_KEY", Value: upstreamKey, Secret: true},
		},
	})
	if err != nil {
		t.Fatalf("ExecuteLoaderCommand returned error: %v", err)
	}

	requestBytes, err := os.ReadFile(result.Artifacts["request"])
	if err != nil {
		t.Fatalf("read command request artifact: %v", err)
	}
	if strings.Contains(string(requestBytes), upstreamKey) {
		t.Fatal("command request artifact contains the upstream LLM key")
	}
	var request execution.RuntimeCommandRequest
	if err := json.Unmarshal(requestBytes, &request); err != nil {
		t.Fatalf("decode command request artifact: %v", err)
	}
	token := request.Env["AGENT_COMPOSE_SANDBOX_TOKEN"]
	if token == "" || request.Env["LLM_API_KEY"] != token || request.Env["OPENAI_API_KEY"] != token {
		t.Fatalf("managed request credentials were not projected consistently: %#v", request.Env)
	}
	if request.Env["LLM_API_ENDPOINT"] != "http://facade.internal:7410/api/runtime/sandboxes/"+session.Summary.ID+"/llm/openai/v1" {
		t.Fatalf("request facade endpoint = %q", request.Env["LLM_API_ENDPOINT"])
	}
	if strings.Contains(fmt.Sprint(capturedSpec.Env), upstreamKey) {
		t.Fatal("child exec environment contains the upstream LLM key")
	}
	if capturedSpec.Env["LLM_API_KEY"] != token || capturedSession == nil {
		t.Fatalf("child exec did not receive the managed facade token: env=%#v session=%#v", capturedSpec.Env, capturedSession)
	}
}

func TestLoaderCommandExecutorFiltersCommandPayloadFromStreamingCellOutput(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		GuestStateRoot:       "/data/state",
		GuestHomePath:        "/root",
		JupyterProxyBasePath: "/agent-compose/session",
		SandboxStartTimeout:  2 * time.Second,
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "loader command sandbox", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeScript, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSandbox(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	streams := sessions.NewStreamBrokerForTest()
	ch, unsubscribe := streams.Subscribe(session.Summary.ID)
	defer unsubscribe()
	executor := NewLoaderCommandExecutor(config, store, nil, fakeRuntimeProvider{runtime: fakeLoaderCommandRuntime{}}, streams)

	result, err := executor.ExecuteLoaderCommand(ctx, session, domain.LoaderCommandRequest{
		Mode:   "shell",
		Script: "echo loader",
	})
	if err != nil {
		t.Fatalf("ExecuteLoaderCommand returned error: %v", err)
	}
	if !result.Success || result.Stdout != "loader stdout\n" || result.Stderr != "loader stderr\n" {
		t.Fatalf("loader result = %#v", result)
	}

	var outputText strings.Builder
	for {
		select {
		case event := <-ch:
			if event.EventType == sessions.WatchEventTypeCellOutput {
				outputText.WriteString(event.Chunk)
				if strings.Contains(event.Chunk, execution.CommandResultPrefix) {
					t.Fatalf("stream event leaked command payload: %#v", event)
				}
			}
		default:
			goto drained
		}
	}

drained:
	if got := outputText.String(); !strings.Contains(got, "loader stdout\n") || !strings.Contains(got, "loader stderr\n") {
		t.Fatalf("stream output = %q", got)
	}
	cells, err := store.ListCells(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListCells returned error: %v", err)
	}
	if len(cells) == 0 {
		t.Fatalf("no cells stored")
	}
	for _, cell := range cells {
		if strings.Contains(cell.Output, execution.CommandResultPrefix) {
			t.Fatalf("cell leaked command payload: %#v", cell)
		}
	}
}
