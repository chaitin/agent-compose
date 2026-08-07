package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/internal/testutil"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/llms/runtimefacade"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sandboxes"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sandboxstore"
)

type fakeSchedulerCommandRuntime struct{}

type capturingSchedulerCommandRuntime struct {
	session *domain.Sandbox
	err     error
}

func (r fakeSchedulerCommandRuntime) EnsureSandbox(context.Context, *domain.Sandbox, domain.VMState, domain.ProxyState) (domain.SandboxVMInfo, error) {
	return domain.SandboxVMInfo{}, nil
}

func (r fakeSchedulerCommandRuntime) StopSandbox(context.Context, *domain.Sandbox, domain.VMState) (bool, error) {
	return false, nil
}

func (r fakeSchedulerCommandRuntime) RemoveSandbox(context.Context, *domain.Sandbox, domain.VMState) error {
	return nil
}

func (r fakeSchedulerCommandRuntime) Exec(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec) (domain.ExecResult, error) {
	return domain.ExecResult{}, nil
}

func (r fakeSchedulerCommandRuntime) ExecStream(_ context.Context, _ *domain.Sandbox, _ domain.VMState, _ domain.ExecSpec, stream domain.ExecStreamWriter) (domain.ExecResult, error) {
	commandResult := domain.RuntimeCommandResult{
		Stdout:   "scheduler stdout\n",
		Stderr:   "scheduler stderr\n",
		Output:   "scheduler stdout\nscheduler stderr\n",
		ExitCode: 0,
		Success:  true,
	}
	payloadBytes, _ := json.Marshal(commandResult)
	payload := execution.CommandResultPrefix + string(payloadBytes) + "\n"
	if stream != nil {
		stream(domain.ExecChunk{Text: "scheduler stdout\n"})
		stream(domain.ExecChunk{Text: "scheduler stderr\n", Stream: domain.StdioStderr})
		stream(domain.ExecChunk{Text: payload})
	}
	return domain.ExecResult{
		Stdout:   "scheduler stdout\n" + payload,
		Stderr:   "scheduler stderr\n",
		Output:   "scheduler stdout\nscheduler stderr\n" + payload,
		ExitCode: 0,
		Success:  true,
	}, nil
}

func (r *capturingSchedulerCommandRuntime) EnsureSandbox(context.Context, *domain.Sandbox, domain.VMState, domain.ProxyState) (domain.SandboxVMInfo, error) {
	return domain.SandboxVMInfo{}, nil
}

func (r *capturingSchedulerCommandRuntime) StopSandbox(context.Context, *domain.Sandbox, domain.VMState) (bool, error) {
	return false, nil
}

func (r *capturingSchedulerCommandRuntime) RemoveSandbox(context.Context, *domain.Sandbox, domain.VMState) error {
	return nil
}

func (r *capturingSchedulerCommandRuntime) Exec(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec) (domain.ExecResult, error) {
	return domain.ExecResult{}, nil
}

func (r *capturingSchedulerCommandRuntime) ExecStream(ctx context.Context, session *domain.Sandbox, state domain.VMState, spec domain.ExecSpec, stream domain.ExecStreamWriter) (domain.ExecResult, error) {
	r.session = session
	if r.err != nil {
		return domain.ExecResult{}, r.err
	}
	return (fakeSchedulerCommandRuntime{}).ExecStream(ctx, session, state, spec, stream)
}

func TestSchedulerCommandExecutorFiltersCommandPayloadFromStreamingCellOutput(t *testing.T) {
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
	store, err := sandboxstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "scheduler command sandbox", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeScript, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSandbox(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	streams := sandboxes.NewStreamBrokerForTest()
	ch, unsubscribe := streams.Subscribe(session.Summary.ID)
	defer unsubscribe()
	executor := NewSchedulerCommandExecutor(config, store, nil, fakeRuntimeProvider{runtime: fakeSchedulerCommandRuntime{}}, streams)

	result, err := executor.ExecuteSchedulerCommand(ctx, session, domain.SchedulerCommandRequest{
		Mode:   "shell",
		Script: "echo scheduler",
	})
	if err != nil {
		t.Fatalf("ExecuteSchedulerCommand returned error: %v", err)
	}
	if !result.Success || result.Stdout != "scheduler stdout\n" || result.Stderr != "scheduler stderr\n" {
		t.Fatalf("scheduler result = %#v", result)
	}

	var outputText strings.Builder
	for {
		select {
		case event := <-ch:
			if event.EventType == sandboxes.WatchEventTypeCellOutput {
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
	if got := outputText.String(); !strings.Contains(got, "scheduler stdout\n") || !strings.Contains(got, "scheduler stderr\n") {
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

func TestSchedulerCommandExecutorRebuildsAndOwnsCommandFacadeTokens(t *testing.T) {
	tests := []struct {
		name            string
		runtimeErr      error
		wantTokenCount  int
		wantTokenExists bool
	}{
		{name: "normal completion cleans every command token"},
		{name: "unconfirmed termination retains every command token", runtimeErr: domain.ErrExecTerminationUnconfirmed, wantTokenCount: 3, wantTokenExists: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			config := schedulerCommandFacadeTestConfig(root)
			configDB, store, err := testutil.OpenStores(t, config)
			if err != nil {
				t.Fatalf("OpenStores returned error: %v", err)
			}
			seedSchedulerCommandFacadeProviders(t, ctx, configDB)
			session, err := store.CreateSandbox(ctx, "scheduler facade command", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SandboxTypeScript, nil, []domain.SandboxEnvVar{
				{Name: "ANTHROPIC_BASE_URL", Value: "https://anthropic.persisted.test"},
				{Name: "ANTHROPIC_API_KEY", Value: "persisted-upstream-key", Secret: true},
			}, nil)
			if err != nil {
				t.Fatalf("CreateSandbox returned error: %v", err)
			}
			session.Summary.VMStatus = domain.VMStatusRunning
			if err := store.UpdateSandbox(ctx, session); err != nil {
				t.Fatalf("UpdateSandbox returned error: %v", err)
			}
			if err := store.SaveVMState(session.Summary.ID, domain.VMState{Driver: driverpkg.RuntimeDriverDocker, BoxID: "container-1"}); err != nil {
				t.Fatalf("SaveVMState returned error: %v", err)
			}

			runtime := &capturingSchedulerCommandRuntime{err: tt.runtimeErr}
			executor := NewSchedulerCommandExecutor(config, store, configDB, fakeRuntimeProvider{runtime: runtime}, sandboxes.NewStreamBrokerForTest())
			result, err := executor.ExecuteSchedulerCommand(ctx, session, domain.SchedulerCommandRequest{
				Mode:   "shell",
				Script: "echo scheduler",
				Env: map[string]string{
					"PROJECT_AGENT_LLM_PROVIDER":  "codex",
					"CODEX_MODEL":                 "openai-model",
					"ANTHROPIC_API_KEY":           "request-upstream-anthropic-key",
					"ANTHROPIC_BASE_URL":          "https://anthropic.request.test",
					"OPENAI_API_KEY":              "request-upstream-openai-key",
					"OPENAI_BASE_URL":             "https://openai.request.test/v1",
					"AGENT_COMPOSE_SANDBOX_TOKEN": "stale-request-facade-token",
					"CUSTOM_REQUEST_ENV":          "preserved",
					"GOOGLE_API_KEY":              "preserved-google-key",
				},
			})
			if tt.runtimeErr == nil && err != nil {
				t.Fatalf("ExecuteSchedulerCommand returned error: %v", err)
			}
			if tt.runtimeErr != nil && !errors.Is(err, tt.runtimeErr) {
				t.Fatalf("ExecuteSchedulerCommand error = %v, want %v", err, tt.runtimeErr)
			}
			if runtime.session == nil {
				t.Fatal("runtime did not receive command Sandbox clone")
			}
			env := domain.SandboxEnvMap(runtime.session.RuntimeEnvItems)
			if env["ANTHROPIC_API_KEY"] == "" || env["ANTHROPIC_API_KEY"] == "persisted-upstream-key" || env["ANTHROPIC_BASE_URL"] == "https://anthropic.persisted.test" {
				t.Fatalf("command did not reconstruct Anthropic startup facade environment: %#v", env)
			}
			if env["OPENAI_API_KEY"] == "" || env["OPENAI_API_KEY"] != env["AGENT_COMPOSE_SANDBOX_TOKEN"] {
				t.Fatalf("selected Codex facade environment = %#v", env)
			}
			requestBytes, readErr := os.ReadFile(result.Artifacts["request"])
			if readErr != nil {
				t.Fatalf("read runtime command request: %v", readErr)
			}
			var runtimeRequest execution.RuntimeCommandRequest
			if err := json.Unmarshal(requestBytes, &runtimeRequest); err != nil {
				t.Fatalf("decode runtime command request: %v", err)
			}
			if runtimeRequest.Env["ANTHROPIC_API_KEY"] != env["ANTHROPIC_API_KEY"] || runtimeRequest.Env["ANTHROPIC_BASE_URL"] != env["ANTHROPIC_BASE_URL"] || runtimeRequest.Env["OPENAI_API_KEY"] != env["OPENAI_API_KEY"] {
				t.Fatalf("runtime child request did not preserve managed facade precedence: request=%#v managed=%#v", runtimeRequest.Env, env)
			}
			if runtimeRequest.Env["AGENT_COMPOSE_SANDBOX_TOKEN"] != env["AGENT_COMPOSE_SANDBOX_TOKEN"] || runtimeRequest.Env["CUSTOM_REQUEST_ENV"] != "preserved" || runtimeRequest.Env["GOOGLE_API_KEY"] != "preserved-google-key" {
				t.Fatalf("runtime child request token/custom environment = %#v", runtimeRequest.Env)
			}
			if got := countSchedulerCommandFacadeTokens(t, ctx, configDB); got != tt.wantTokenCount {
				t.Fatalf("persisted scheduler command tokens = %d, want %d", got, tt.wantTokenCount)
			}
			assertSchedulerCommandTokenState(t, ctx, configDB, env["ANTHROPIC_API_KEY"], tt.wantTokenExists)
			assertSchedulerCommandTokenState(t, ctx, configDB, env["AGENT_COMPOSE_SANDBOX_TOKEN"], tt.wantTokenExists)
		})
	}
}

func schedulerCommandFacadeTestConfig(root string) *appconfig.Config {
	return &appconfig.Config{
		DataRoot:             root,
		DbAddr:               filepath.Join(root, "data.db"),
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverDocker,
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		GuestStateRoot:       "/data/state",
		GuestHomePath:        "/root",
		RuntimeBaseURL:       "http://agent-compose.test:7410",
		JupyterProxyBasePath: "/agent-compose/session",
		SandboxStartTimeout:  2 * time.Second,
	}
}

func seedSchedulerCommandFacadeProviders(t *testing.T, ctx context.Context, store *configstore.ConfigStore) {
	t.Helper()
	if err := store.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID:             "anthropic-primary",
		Name:           "Anthropic",
		ProviderType:   llms.ProviderFamilyAnthropic,
		DefaultWireAPI: llms.APIProtocolMessages,
		BaseURL:        "https://anthropic.upstream.test",
		APIKey:         "anthropic-upstream-secret",
		Scope:          llms.ProviderScopeSystem,
		Weight:         1,
	}, llms.Model{ID: "claude-model", Name: "claude-model", Enabled: true, DefaultModel: true, Scope: llms.ProviderScopeSystem}); err != nil {
		t.Fatalf("save Anthropic provider: %v", err)
	}
	if err := store.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID:             "openai-primary",
		Name:           "OpenAI",
		ProviderType:   llms.ProviderFamilyOpenAI,
		DefaultWireAPI: llms.APIProtocolResponses,
		BaseURL:        "https://openai.upstream.test",
		APIKey:         "openai-upstream-secret",
		Scope:          llms.ProviderScopeSystem,
		Weight:         2,
	}, llms.Model{ID: "openai-model", Name: "openai-model", Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
		t.Fatalf("save OpenAI provider: %v", err)
	}
}

func countSchedulerCommandFacadeTokens(t *testing.T, ctx context.Context, store *configstore.ConfigStore) int {
	t.Helper()
	var count int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(1) FROM llm_facade_token WHERE source = ?`, runtimefacade.TokenSourceSchedulerCommand).Scan(&count); err != nil {
		t.Fatalf("count scheduler command facade tokens: %v", err)
	}
	return count
}

func assertSchedulerCommandTokenState(t *testing.T, ctx context.Context, store *configstore.ConfigStore, rawToken string, wantPresent bool) {
	t.Helper()
	_, err := store.GetLLMFacadeToken(ctx, rawToken)
	if wantPresent && err != nil {
		t.Fatalf("scheduler command facade token was removed: %v", err)
	}
	if !wantPresent && !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("scheduler command facade token error = %v, want not found", err)
	}
}
