package runs

import (
	"context"
	"testing"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
)

type promptAttachFacadeStore struct {
	ControllerStore
	providers []llms.Provider
	models    []llms.Model
	tokens    []llms.FacadeToken
}

func (s *promptAttachFacadeStore) UpsertDefaultLLMConfig(context.Context, llms.Provider, llms.Model) error {
	return nil
}

func (s *promptAttachFacadeStore) ListEnabledLLMProviders(context.Context) ([]llms.Provider, error) {
	return s.providers, nil
}

func (s *promptAttachFacadeStore) ListEnabledLLMModels(context.Context) ([]llms.Model, error) {
	return s.models, nil
}

func (s *promptAttachFacadeStore) LLMProviderModelWireAPI(context.Context, string, string) (string, bool, error) {
	return llms.APIProtocolMessages, true, nil
}

func (s *promptAttachFacadeStore) ListGlobalEnv(context.Context) ([]domain.SandboxEnvVar, error) {
	return nil, nil
}

func (s *promptAttachFacadeStore) SaveLLMFacadeToken(_ context.Context, token llms.FacadeToken) error {
	s.tokens = append(s.tokens, token)
	return nil
}

func TestEnsurePromptAttachLLMFacadeEnvClaudeUsesControllerStore(t *testing.T) {
	ctx := context.Background()
	config := &appconfig.Config{
		RuntimeBaseURL: "http://agent-compose.test:7410",
		GuestHomePath:  "/root",
	}
	store := &promptAttachFacadeStore{
		providers: []llms.Provider{{
			ID:             "anthropic-test",
			ProviderType:   llms.ProviderFamilyAnthropic,
			DefaultWireAPI: llms.APIProtocolMessages,
			BaseURL:        "https://anthropic.example.test",
			APIKey:         "anthropic-key",
			Enabled:        true,
		}},
		models: []llms.Model{{ID: "claude-test", Name: "claude-test", DefaultModel: true, Enabled: true}},
	}
	sandbox := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:     "sandbox-claude-attach",
			Driver: driver.RuntimeDriverDocker,
		},
	}
	controller := &Controller{config: config, configDB: store}

	env, err := controller.ensurePromptAttachLLMFacadeEnv(ctx, sandbox, execution.AgentConfig{Provider: "claude"}, "run-claude-attach")
	if err != nil {
		t.Fatalf("ensurePromptAttachLLMFacadeEnv returned error: %v", err)
	}
	if env["LLM_API_PROTOCOL"] != llms.APIProtocolMessages || env["ANTHROPIC_MODEL"] != "claude-test" {
		t.Fatalf("Claude facade env = %#v", env)
	}
	if env["ANTHROPIC_BASE_URL"] != "http://agent-compose.test:7410/api/runtime/sandboxes/sandbox-claude-attach/llm/anthropic" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", env["ANTHROPIC_BASE_URL"])
	}
	if env["AGENT_COMPOSE_SANDBOX_TOKEN"] == "" || len(store.tokens) != 1 {
		t.Fatalf("Claude facade token env = %q, saved tokens = %#v", env["AGENT_COMPOSE_SANDBOX_TOKEN"], store.tokens)
	}
	token := store.tokens[0]
	if token.SandboxID != sandbox.Summary.ID || token.Model != "claude-test" || token.Source != "agent" || token.RunID != "run-claude-attach" {
		t.Fatalf("stored token = %#v", token)
	}
}

func TestEnsurePromptAttachClaudeLLMFacadeEnvPreservesRequestedModelWithoutConfiguredProvider(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("LLM_API_KEY", "")

	store := &promptAttachFacadeStore{}
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-claude-attach"}}
	env, err := ensurePromptAttachClaudeLLMFacadeEnv(
		context.Background(),
		&appconfig.Config{RuntimeBaseURL: "http://agent-compose.test:7410"},
		store,
		sandbox,
		" claude-sonnet-4-20250514 ",
		"run-claude-attach",
	)
	if err != nil {
		t.Fatalf("ensurePromptAttachClaudeLLMFacadeEnv returned error: %v", err)
	}
	if env["ANTHROPIC_MODEL"] != "claude-sonnet-4-20250514" || env["CLAUDE_MODEL"] != "claude-sonnet-4-20250514" {
		t.Fatalf("Claude model env = %#v", env)
	}
	if len(store.tokens) != 1 || store.tokens[0].Model != "claude-sonnet-4-20250514" {
		t.Fatalf("saved tokens = %#v", store.tokens)
	}
}
