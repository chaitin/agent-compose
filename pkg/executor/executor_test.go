package executor

import (
	"context"
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

type fakeRuntimeProvider struct{}

func (fakeRuntimeProvider) ForDriver(string) (runtimes.BoxRuntime, error) {
	return nil, nil
}

func (fakeRuntimeProvider) ForSession(*model.Session) (runtimes.BoxRuntime, error) {
	return nil, nil
}

type noopStreamPublisher struct{}

func (noopStreamPublisher) PublishCellStarted(string, model.NotebookCell) {}

func (noopStreamPublisher) PublishCellOutput(string, string, string, bool) {}

func (noopStreamPublisher) PublishCellCompleted(string, model.NotebookCell) {}

func (noopStreamPublisher) PublishEventAdded(string, model.SessionEvent) {}
