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

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	"github.com/chaitin/agent-compose/pkg/llms"
	"github.com/chaitin/agent-compose/pkg/llms/runtimefacade"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
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
	executor := NewSchedulerCommandExecutor(SchedulerCommandExecutorDeps{Config: config, Store: store, Runtimes: fakeRuntimeProvider{runtime: fakeSchedulerCommandRuntime{}}, Streams: streams})

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
		{name: "unconfirmed termination retains every command token", runtimeErr: domain.ErrExecTerminationUnconfirmed, wantTokenCount: 1, wantTokenExists: true},
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
			// The sandbox declares no upstream of its own: this test is about the
			// managed facade token lifecycle. A command that does declare one is
			// covered by TestSchedulerCommandExecutorHonoursADeclaredCommandUpstream.
			session, err := store.CreateSandbox(ctx, "scheduler facade command", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SandboxTypeScript, nil, []domain.SandboxEnvVar{
				{Name: "CUSTOM_SANDBOX_ENV", Value: "preserved-sandbox-env"},
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
			executor := NewSchedulerCommandExecutor(SchedulerCommandExecutorDeps{Config: config, Store: store, ConfigDB: configDB, Runtimes: fakeRuntimeProvider{runtime: runtime}, Streams: sandboxes.NewStreamBrokerForTest()})
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
			// One dialect is prepared for the agent the command names, so the
			// other provider family's facade must not appear.
			if env["ANTHROPIC_API_KEY"] != "" || env["ANTHROPIC_BASE_URL"] != "" {
				t.Fatalf("command reconstructed another family's startup facade: %#v", env)
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
			if runtimeRequest.Env["ANTHROPIC_API_KEY"] != "request-upstream-anthropic-key" || runtimeRequest.Env["ANTHROPIC_BASE_URL"] != "https://anthropic.request.test" || runtimeRequest.Env["OPENAI_API_KEY"] != "request-upstream-openai-key" {
				t.Fatalf("runtime child request did not preserve explicit request environment: %#v", runtimeRequest.Env)
			}
			if runtimeRequest.Env["AGENT_COMPOSE_SANDBOX_TOKEN"] != "stale-request-facade-token" || runtimeRequest.Env["CUSTOM_REQUEST_ENV"] != "preserved" || runtimeRequest.Env["GOOGLE_API_KEY"] != "preserved-google-key" {
				t.Fatalf("runtime child request token/custom environment = %#v", runtimeRequest.Env)
			}
			if got := countSchedulerCommandFacadeTokens(t, ctx, configDB); got != tt.wantTokenCount {
				t.Fatalf("persisted scheduler command tokens = %d, want %d", got, tt.wantTokenCount)
			}
			assertSchedulerCommandTokenState(t, ctx, configDB, env["AGENT_COMPOSE_SANDBOX_TOKEN"], tt.wantTokenExists)
		})
	}
}

func TestSchedulerCommandExecutorHonoursADeclaredCommandUpstream(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := schedulerCommandFacadeTestConfig(root)
	configDB, store, err := testutil.OpenStores(t, config)
	if err != nil {
		t.Fatalf("OpenStores returned error: %v", err)
	}
	seedSchedulerCommandFacadeProviders(t, ctx, configDB)
	session, err := store.CreateSandbox(ctx, "scheduler declared upstream", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SandboxTypeScript, nil, nil, nil)
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

	runtime := &capturingSchedulerCommandRuntime{}
	executor := NewSchedulerCommandExecutor(SchedulerCommandExecutorDeps{Config: config, Store: store, ConfigDB: configDB, Runtimes: fakeRuntimeProvider{runtime: runtime}, Streams: sandboxes.NewStreamBrokerForTest()})
	if _, err := executor.ExecuteSchedulerCommand(ctx, session, domain.SchedulerCommandRequest{
		Mode:   "shell",
		Script: "echo declared",
		Env: map[string]string{
			"PROJECT_AGENT_LLM_PROVIDER": "codex",
			"CODEX_MODEL":                "declared-model",
		},
		// The command declares the upstream its agent runs against, in the same
		// way a project variable or an agent's own environment would.
		SandboxEnv: []domain.SandboxEnvVar{
			{Name: "OPENAI_BASE_URL", Value: "https://declared.upstream.test/v1"},
			{Name: "OPENAI_API_KEY", Value: "declared-upstream-key"},
		},
	}); err != nil {
		t.Fatalf("ExecuteSchedulerCommand returned error: %v", err)
	}
	if runtime.session == nil {
		t.Fatal("runtime did not receive command Sandbox clone")
	}
	env := domain.SandboxEnvMap(runtime.session.RuntimeEnvItems)
	// The command's declared upstream is imported and proxied: the guest carries
	// the facade token, and the declared key stays on the daemon.
	token := env["AGENT_COMPOSE_SANDBOX_TOKEN"]
	if token == "" {
		t.Fatalf("command did not proxy the declared upstream: %#v", env)
	}
	if env["OPENAI_API_KEY"] != token {
		t.Fatalf("declared command did not present the facade token: %#v", env)
	}
	if env["OPENAI_BASE_URL"] == "https://declared.upstream.test/v1" {
		t.Fatalf("guest still points at the declared upstream: %#v", env)
	}
	for name, value := range env {
		if strings.Contains(value, "declared-upstream-key") {
			t.Fatalf("env[%s] carries the declared upstream key", name)
		}
	}
	if env["CODEX_MODEL"] != "declared-model" {
		t.Fatalf("declared command model = %#v", env)
	}
	if got := countSchedulerCommandFacadeTokens(t, ctx, configDB); got != 0 {
		t.Fatalf("scheduler command tokens still live after completion = %d, want 0", got)
	}
	// The declaration was imported into the daemon's own connection
	// configuration, which is what let the command proxy it.
	var apiKey string
	declaredID := llms.DeclaredConnectionID(session.Summary.ID, llms.ProviderFamilyOpenAI)
	if err := configDB.DB().QueryRowContext(ctx, `SELECT api_key FROM llm_provider WHERE id = ?`, declaredID).Scan(&apiKey); err != nil {
		t.Fatalf("read declared connection %q: %v", declaredID, err)
	}
	if apiKey != "declared-upstream-key" {
		t.Error("the daemon-side connection does not carry the declared credential")
	}
}

func TestSchedulerCommandExecutorRecoversLegacyProviderEnv(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := schedulerCommandFacadeTestConfig(root)
	configDB, store, err := testutil.OpenStores(t, config)
	if err != nil {
		t.Fatalf("OpenStores returned error: %v", err)
	}
	seedSchedulerCommandFacadeProviders(t, ctx, configDB)
	// Provider provenance did not exist when this sandbox was created, so its
	// persisted environment is the only record of the upstream it declared.
	session, err := store.CreateSandbox(ctx, "scheduler legacy provider env", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SandboxTypeScript, nil, []domain.SandboxEnvVar{
		{Name: "OPENAI_BASE_URL", Value: "https://legacy.upstream.test/v1"},
		{Name: "OPENAI_API_KEY", Value: "legacy-upstream-key", Secret: true},
		{Name: "CODEX_MODEL", Value: "legacy-model"},
	}, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	if session.ProviderEnvOverrideNames != nil {
		t.Fatalf("fixture is not provenance-less: %#v", session.ProviderEnvOverrideNames)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSandbox(ctx, session); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}
	if err := store.SaveVMState(session.Summary.ID, domain.VMState{Driver: driverpkg.RuntimeDriverDocker, BoxID: "container-1"}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}

	runtime := &capturingSchedulerCommandRuntime{}
	executor := NewSchedulerCommandExecutor(SchedulerCommandExecutorDeps{Config: config, Store: store, ConfigDB: configDB, Runtimes: fakeRuntimeProvider{runtime: runtime}, Streams: sandboxes.NewStreamBrokerForTest()})
	if _, err := executor.ExecuteSchedulerCommand(ctx, session, domain.SchedulerCommandRequest{
		Mode:   "shell",
		Script: "echo legacy",
		Env:    map[string]string{"PROJECT_AGENT_LLM_PROVIDER": "codex"},
	}); err != nil {
		t.Fatalf("ExecuteSchedulerCommand returned error: %v", err)
	}
	if runtime.session == nil {
		t.Fatal("runtime did not receive command Sandbox clone")
	}
	env := domain.SandboxEnvMap(runtime.session.RuntimeEnvItems)
	// The recovered legacy declaration is proxied too: the command presents the
	// facade token and the recovered key stays on the daemon.
	token := env["AGENT_COMPOSE_SANDBOX_TOKEN"]
	if token == "" {
		t.Fatalf("command did not proxy the recovered legacy upstream: %#v", env)
	}
	if env["OPENAI_API_KEY"] != token {
		t.Fatalf("recovered legacy command did not present the facade token: %#v", env)
	}
	if env["OPENAI_BASE_URL"] == "https://legacy.upstream.test/v1" {
		t.Fatalf("guest still points at the recovered legacy upstream: %#v", env)
	}
	for name, value := range env {
		if strings.Contains(value, "legacy-upstream-key") {
			t.Fatalf("env[%s] carries the recovered legacy key", name)
		}
	}
	if env["CODEX_MODEL"] != "legacy-model" {
		t.Fatalf("command did not use the recovered legacy model: %#v", env)
	}
	if got := countSchedulerCommandFacadeTokens(t, ctx, configDB); got != 0 {
		t.Fatalf("scheduler command tokens still live after a recovered legacy run = %d, want 0", got)
	}
	// The recovered declaration was imported into the daemon's own connection
	// configuration, which is what let the command proxy it.
	var apiKey string
	declaredID := llms.DeclaredConnectionID(session.Summary.ID, llms.ProviderFamilyOpenAI)
	if err := configDB.DB().QueryRowContext(ctx, `SELECT api_key FROM llm_provider WHERE id = ?`, declaredID).Scan(&apiKey); err != nil {
		t.Fatalf("read declared connection %q: %v", declaredID, err)
	}
	if apiKey != "legacy-upstream-key" {
		t.Error("the daemon-side connection does not carry the recovered legacy credential")
	}
}

func TestSchedulerCommandExecutorSkipsFacadeReconstructionWithoutSupportedAgentOverride(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := t.TempDir()
	config := schedulerCommandFacadeTestConfig(root)
	configDB, _, err := testutil.OpenStores(t, config)
	if err != nil {
		t.Fatalf("OpenStores returned error: %v", err)
	}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-shell-only"}}
	got, tokenHashes, err := (&SchedulerCommandExecutor{Config: config, ConfigDB: configDB}).prepareSchedulerCommandLLMFacadeEnv(ctx, session, domain.SchedulerCommandRequest{
		Mode: "shell", Script: "echo shell", Env: map[string]string{"AGENT_PROVIDER": "opencode"},
	}, "cell-shell-only")
	if err != nil {
		t.Fatalf("prepareSchedulerCommandLLMFacadeEnv returned error: %v", err)
	}
	if got != session || len(tokenHashes) != 0 {
		t.Fatalf("shell-only facade preparation = session %p/%p, token hashes %#v; want original session and no tokens", got, session, tokenHashes)
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
