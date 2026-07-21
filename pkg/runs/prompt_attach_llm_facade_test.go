package runs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/llms/runtimefacade"
	domain "agent-compose/pkg/model"
)

func TestPromptAttachLLMFacadeMatchesOrdinaryCodexResponsesIngress(t *testing.T) {
	for _, key := range []string{"LLM_API_ENDPOINT", "LLM_API_PROTOCOL", "LLM_API_KEY", "OPENAI_API_KEY", "LLM_MODEL"} {
		t.Setenv(key, "")
	}
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:       root,
		LLMAPIEndpoint: "https://chat.example.test/v1",
		LLMAPIProtocol: llms.APIProtocolChatCompletions,
		LLMAPIKey:      "provider-key",
		LLMModel:       "chat-model",
		RuntimeBaseURL: "http://agent-compose.test:7410",
	}
	store := newPromptAttachLLMStore()
	controller := &Controller{config: config, configDB: store}
	promptSession := promptAttachLLMTestSandbox(root, "prompt-attach")
	ordinarySession := promptAttachLLMTestSandbox(root, "ordinary")

	promptEnv, err := controller.ensurePromptAttachLLMFacadeEnv(ctx, promptSession, execution.AgentConfig{Provider: "codex"}, "run-prompt")
	if err != nil {
		t.Fatalf("ensurePromptAttachLLMFacadeEnv returned error: %v", err)
	}
	ordinaryConfig, err := runtimefacade.EnsureSessionAgentRuntimeConfig(ctx, config, store, ordinarySession, "codex", "", runtimefacade.TokenSourceAgent, "run-ordinary")
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig returned error: %v", err)
	}

	assertCodexResponsesFacadeConfig(t, store, promptSession, promptEnv, "run-prompt")
	assertCodexResponsesFacadeConfig(t, store, ordinarySession, ordinaryConfig.Env, "run-ordinary")
	if strings.ReplaceAll(promptEnv["LLM_API_ENDPOINT"], promptSession.Summary.ID, "sandbox") != strings.ReplaceAll(ordinaryConfig.Env["LLM_API_ENDPOINT"], ordinarySession.Summary.ID, "sandbox") {
		t.Fatalf("prompt endpoint %q does not match ordinary endpoint %q", promptEnv["LLM_API_ENDPOINT"], ordinaryConfig.Env["LLM_API_ENDPOINT"])
	}
}

func promptAttachLLMTestSandbox(root, id string) *domain.Sandbox {
	return &domain.Sandbox{Summary: domain.SandboxSummary{
		ID:            id,
		Driver:        driverpkg.RuntimeDriverDocker,
		WorkspacePath: filepath.Join(root, "sandboxes", id, "workspace"),
	}}
}

func assertCodexResponsesFacadeConfig(t *testing.T, store *promptAttachLLMStore, session *domain.Sandbox, env map[string]string, runID string) {
	t.Helper()
	if env["LLM_API_PROTOCOL"] != llms.APIProtocolResponses || env["AGENT_COMPOSE_SANDBOX_TOKEN"] == "" {
		t.Fatalf("facade env = %#v", env)
	}
	token, ok := store.facadeToken(env["AGENT_COMPOSE_SANDBOX_TOKEN"])
	if !ok {
		t.Fatalf("facade token was not saved")
	}
	if token.WireAPI != llms.APIProtocolResponses || token.Source != runtimefacade.TokenSourceAgent || token.RunID != runID {
		t.Fatalf("facade token = %#v", token)
	}
	data, err := os.ReadFile(filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read Codex config: %v", err)
	}
	if !strings.Contains(string(data), `wire_api = "responses"`) {
		t.Fatalf("Codex config = %s", data)
	}
}

type promptAttachLLMStore struct {
	ControllerStore
	providers []llms.Provider
	models    []llms.Model
	wireAPI   map[string]string
	tokens    map[string]llms.FacadeToken
}

func newPromptAttachLLMStore() *promptAttachLLMStore {
	return &promptAttachLLMStore{
		wireAPI: make(map[string]string),
		tokens:  make(map[string]llms.FacadeToken),
	}
}

func (s *promptAttachLLMStore) UpsertDefaultLLMConfig(_ context.Context, provider llms.Provider, model llms.Model) error {
	provider.Enabled = true
	model.Enabled = true
	s.providers = replacePromptAttachProvider(s.providers, provider)
	s.models = replacePromptAttachModel(s.models, model)
	s.wireAPI[provider.ID+"\x00"+model.ID] = llms.NormalizeWireAPI(provider.DefaultWireAPI)
	return nil
}

func (s *promptAttachLLMStore) ListEnabledLLMProviders(context.Context) ([]llms.Provider, error) {
	return append([]llms.Provider(nil), s.providers...), nil
}

func (s *promptAttachLLMStore) ListEnabledLLMModels(context.Context) ([]llms.Model, error) {
	return append([]llms.Model(nil), s.models...), nil
}

func (s *promptAttachLLMStore) LLMProviderModelWireAPI(_ context.Context, providerID, modelID string) (string, bool, error) {
	wireAPI, ok := s.wireAPI[providerID+"\x00"+modelID]
	return wireAPI, ok, nil
}

func (s *promptAttachLLMStore) ListGlobalEnv(context.Context) ([]domain.SandboxEnvVar, error) {
	return nil, nil
}

func (s *promptAttachLLMStore) SaveLLMFacadeToken(_ context.Context, token llms.FacadeToken) error {
	s.tokens[token.TokenHash] = token
	return nil
}

func (s *promptAttachLLMStore) SaveLLMFacadeGrant(_ context.Context, grant llms.FacadeGrant) error {
	s.tokens[grant.Token.TokenHash] = grant.Token
	return nil
}

func (s *promptAttachLLMStore) facadeToken(raw string) (llms.FacadeToken, bool) {
	hash, _ := llms.HashFacadeToken(raw)
	token, ok := s.tokens[hash]
	return token, ok
}

func replacePromptAttachProvider(providers []llms.Provider, replacement llms.Provider) []llms.Provider {
	for index := range providers {
		if providers[index].ID == replacement.ID {
			providers[index] = replacement
			return providers
		}
	}
	return append(providers, replacement)
}

func replacePromptAttachModel(models []llms.Model, replacement llms.Model) []llms.Model {
	for index := range models {
		if models[index].ID == replacement.ID {
			if replacement.Scope != llms.ProviderScopeSessionEnv || models[index].Scope == llms.ProviderScopeSessionEnv {
				models[index] = replacement
			}
			return models
		}
	}
	return append(models, replacement)
}
