package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/model"
	"agent-compose/pkg/runtimes"
	"agent-compose/pkg/storage"
)

func TestNewExecutorUsesOptionalLLMFacadeEnvPreparer(t *testing.T) {
	di := newExecutorTestInjector(t)
	preparerCalled := false
	do.ProvideValue[LLMFacadeEnvPreparer](di, func(ctx context.Context, config *appconfig.Config, configDB *storage.ConfigStore, session *model.Session, agent, modelName, source, runID string) (map[string]string, error) {
		preparerCalled = true
		return map[string]string{"AGENT_COMPOSE_SESSION_TOKEN": "token"}, nil
	})

	executor, err := NewExecutor(di)
	if err != nil {
		t.Fatalf("NewExecutor returned error: %v", err)
	}
	if executor.prepareLLM == nil {
		t.Fatalf("prepareLLM is nil")
	}
	env, err := executor.prepareLLM(context.Background(), executor.config, executor.configDB, &model.Session{}, "codex", "", "agent", "run-1")
	if err != nil {
		t.Fatalf("prepareLLM returned error: %v", err)
	}
	if !preparerCalled || env["AGENT_COMPOSE_SESSION_TOKEN"] != "token" {
		t.Fatalf("prepareLLM env=%#v called=%v", env, preparerCalled)
	}
}

func TestNewExecutorAllowsMissingLLMFacadeEnvPreparer(t *testing.T) {
	di := newExecutorTestInjector(t)

	executor, err := NewExecutor(di)
	if err != nil {
		t.Fatalf("NewExecutor returned error: %v", err)
	}
	if executor.prepareLLM != nil {
		t.Fatalf("prepareLLM = %p, want nil", executor.prepareLLM)
	}
}

func TestExecuteAgentRunMergesManagedLLMFacadeEnv(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sessionID := "session-agent-managed-env"
	config := &appconfig.Config{
		DataRoot:           root,
		SessionRoot:        root,
		GuestStateRoot:     "/data/state",
		GuestWorkspacePath: "/workspace",
		GuestRuntimeRoot:   "/data/runtime",
		GuestLogRoot:       "/data/logs",
	}
	store, err := storage.NewStoreFromConfig(config)
	if err != nil {
		t.Fatalf("NewStoreFromConfig returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, sessionID, "vm"), 0o755); err != nil {
		t.Fatalf("MkdirAll(vm) returned error: %v", err)
	}
	if err := store.SaveVMState(sessionID, model.VMState{Driver: "docker"}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}
	configDB, err := storage.NewConfigStoreFromConfig(config)
	if err != nil {
		t.Fatalf("NewConfigStoreFromConfig returned error: %v", err)
	}
	runtime := &managedEnvRuntime{}
	executor := New(config, store, configDB, fakeRuntimeProvider{runtime: runtime}, noopStreamPublisher{}, func(ctx context.Context, config *appconfig.Config, configDB *storage.ConfigStore, session *model.Session, agent, modelName, source, runID string) (map[string]string, error) {
		return map[string]string{
			"AGENT_COMPOSE_SESSION_TOKEN": "agent-token",
			"LLM_API_KEY":                 "agent-token",
		}, nil
	})
	session := &model.Session{Summary: model.SessionSummary{
		ID:            sessionID,
		VMStatus:      model.VMStatusRunning,
		WorkspacePath: filepath.Join(root, sessionID, "workspace"),
	}}

	result, parsed, err := executor.executeAgentRun(ctx, session, "codex", "", "", "run-agent", "hello", "", nil)
	if err != nil {
		t.Fatalf("executeAgentRun returned error: %v", err)
	}
	if !result.Success || !parsed.Success {
		t.Fatalf("executeAgentRun success = (%v, %v), want true", result.Success, parsed.Success)
	}
	if runtime.lastSpec.Env["AGENT_COMPOSE_SESSION_TOKEN"] != "agent-token" || runtime.lastSpec.Env["LLM_API_KEY"] != "agent-token" {
		t.Fatalf("agent exec env missing managed facade env: %#v", runtime.lastSpec.Env)
	}
}

func TestPrepareLoaderCommandLLMFacadeEnvAddsManagedRuntimeEnv(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	configDB, err := storage.NewConfigStoreFromConfig(&appconfig.Config{DataRoot: root})
	if err != nil {
		t.Fatalf("NewConfigStoreFromConfig returned error: %v", err)
	}
	executor := &Executor{
		config:   &appconfig.Config{DataRoot: root},
		configDB: configDB,
		prepareLLM: func(ctx context.Context, config *appconfig.Config, configDB *storage.ConfigStore, session *model.Session, agent, modelName, source, runID string) (map[string]string, error) {
			return map[string]string{
				"AGENT_COMPOSE_SESSION_TOKEN": "loader-token",
				"LLM_API_KEY":                 "loader-token",
			}, nil
		},
	}
	session := &model.Session{Summary: model.SessionSummary{ID: "session-loader-managed-env"}}

	execSession, token, err := executor.prepareLoaderCommandLLMFacadeEnv(ctx, session, model.LoaderCommandRequest{}, "cell-1")
	if err != nil {
		t.Fatalf("prepareLoaderCommandLLMFacadeEnv returned error: %v", err)
	}
	if token != "loader-token" {
		t.Fatalf("facade token = %q, want loader-token", token)
	}
	env := model.SessionEnvMap(execSession.RuntimeEnvItems)
	if env["AGENT_COMPOSE_SESSION_TOKEN"] != "loader-token" || env["LLM_API_KEY"] != "loader-token" {
		t.Fatalf("loader runtime env missing managed facade env: %#v", execSession.RuntimeEnvItems)
	}
	if len(session.RuntimeEnvItems) != 0 {
		t.Fatalf("source session RuntimeEnvItems mutated: %#v", session.RuntimeEnvItems)
	}
}

func newExecutorTestInjector(t *testing.T) do.Injector {
	t.Helper()
	root := t.TempDir()
	config := &appconfig.Config{DataRoot: root, SessionRoot: filepath.Join(root, "sessions")}
	store, err := storage.NewStoreFromConfig(config)
	if err != nil {
		t.Fatalf("NewStoreFromConfig returned error: %v", err)
	}
	configDB, err := storage.NewConfigStoreFromConfig(config)
	if err != nil {
		t.Fatalf("NewConfigStoreFromConfig returned error: %v", err)
	}

	di := do.New()
	do.ProvideValue(di, config)
	do.ProvideValue[*Store](di, store)
	do.ProvideValue[*ConfigStore](di, configDB)
	do.ProvideValue[RuntimeProvider](di, fakeRuntimeProvider{})
	do.ProvideValue[StreamPublisher](di, noopStreamPublisher{})
	return di
}

type fakeRuntimeProvider struct {
	runtime runtimes.BoxRuntime
}

func (p fakeRuntimeProvider) ForDriver(string) (runtimes.BoxRuntime, error) {
	return p.runtime, nil
}

func (p fakeRuntimeProvider) ForSession(*model.Session) (runtimes.BoxRuntime, error) {
	return p.runtime, nil
}

type noopStreamPublisher struct{}

func (noopStreamPublisher) PublishCellStarted(string, model.NotebookCell) {}

func (noopStreamPublisher) PublishCellOutput(string, string, string, bool) {}

func (noopStreamPublisher) PublishCellCompleted(string, model.NotebookCell) {}

func (noopStreamPublisher) PublishEventAdded(string, model.SessionEvent) {}

type managedEnvRuntime struct {
	lastSpec ExecSpec
}

func (r *managedEnvRuntime) EnsureSession(context.Context, *model.Session, model.VMState, model.ProxyState) (model.SessionVMInfo, error) {
	return model.SessionVMInfo{}, nil
}

func (r *managedEnvRuntime) StopSession(context.Context, *model.Session, model.VMState) (bool, error) {
	return false, nil
}

func (r *managedEnvRuntime) Exec(context.Context, *model.Session, model.VMState, model.ExecSpec) (model.ExecResult, error) {
	return model.ExecResult{}, nil
}

func (r *managedEnvRuntime) ExecStream(_ context.Context, _ *model.Session, _ model.VMState, spec model.ExecSpec, _ model.ExecStreamWriter) (model.ExecResult, error) {
	r.lastSpec = spec
	payload, _ := json.Marshal(model.AgentRunResult{
		Agent:         "codex",
		SessionID:     "agent-runtime-session",
		StopReason:    "completed",
		FinalText:     "done",
		Transcript:    "done",
		DisplayOutput: "done",
		Success:       true,
	})
	return model.ExecResult{Stdout: AgentResultPrefix + string(payload) + "\n", Success: true}, nil
}
