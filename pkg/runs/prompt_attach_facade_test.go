package runs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
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

func (s *promptAttachFacadeStore) LLMProviderModelWireAPI(_ context.Context, providerID, _ string) (string, bool, error) {
	if providerID == "openai-test" {
		return llms.APIProtocolResponses, true, nil
	}
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
	if env["LLM_API_PROTOCOL"] != llms.APIProtocolMessages {
		t.Fatalf("Claude facade env = %#v", env)
	}
	if env["ANTHROPIC_BASE_URL"] != "http://agent-compose.test:7410/api/runtime/sandboxes/sandbox-claude-attach/llm/anthropic" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", env["ANTHROPIC_BASE_URL"])
	}
	if env["AGENT_COMPOSE_SANDBOX_TOKEN"] == "" || len(store.tokens) != 1 {
		t.Fatalf("Claude facade token env = %q, saved tokens = %#v", env["AGENT_COMPOSE_SANDBOX_TOKEN"], store.tokens)
	}
	token := store.tokens[0]
	if token.SandboxID != sandbox.Summary.ID || token.Source != "agent" || token.RunID != "run-claude-attach" {
		t.Fatalf("stored token = %#v", token)
	}
}

func TestEnsurePromptAttachLLMFacadeEnvRejectsManagedCodexWithoutReachableFacade(t *testing.T) {
	isolatePromptAttachLLMEnv(t)
	store := &promptAttachFacadeStore{
		providers: []llms.Provider{{
			ID:             "openai-test",
			ProviderType:   llms.ProviderFamilyOpenAI,
			DefaultWireAPI: llms.APIProtocolResponses,
			BaseURL:        "https://openai.example.test/v1",
			APIKey:         "openai-key",
			Enabled:        true,
		}},
		models: []llms.Model{{ID: "gpt-test", Name: "gpt-test", DefaultModel: true, Enabled: true}},
	}
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-codex-no-facade", Driver: driver.RuntimeDriverDocker}}
	controller := &Controller{
		config:   &appconfig.Config{HttpListen: "127.0.0.1:7410", GuestHomePath: "/root"},
		configDB: store,
	}

	env, err := controller.ensurePromptAttachLLMFacadeEnv(
		context.Background(),
		sandbox,
		execution.AgentConfig{Provider: "codex", Model: "gpt-test"},
		"run-codex-no-facade",
	)
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("ensurePromptAttachLLMFacadeEnv error = %v, want failed precondition", err)
	}
	if len(env) != 0 || len(store.tokens) != 0 {
		t.Fatalf("facade env = %#v, tokens = %#v; want no partial configuration", env, store.tokens)
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
		promptAttachFacadeTarget{
			Config:  &appconfig.Config{RuntimeBaseURL: "http://agent-compose.test:7410"},
			Store:   store,
			Sandbox: sandbox,
		},
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

func TestEnsurePromptAttachLLMFacadeEnvOpenCodeUsesSharedRuntimeConfig(t *testing.T) {
	isolatePromptAttachLLMEnv(t)
	root := t.TempDir()
	config := &appconfig.Config{
		RuntimeBaseURL: "http://agent-compose.test:7410",
		GuestHomePath:  "/root",
	}
	store := &promptAttachFacadeStore{
		providers: []llms.Provider{{
			ID:             "openai-test",
			ProviderType:   llms.ProviderFamilyOpenAI,
			DefaultWireAPI: llms.APIProtocolResponses,
			BaseURL:        "https://openai.example.test/v1",
			APIKey:         "openai-key",
			Enabled:        true,
		}},
		models: []llms.Model{{ID: "gpt-test", Name: "gpt-test", DefaultModel: true, Enabled: true}},
	}
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{
		ID:            "sandbox-opencode-attach",
		Driver:        driver.RuntimeDriverDocker,
		WorkspacePath: filepath.Join(root, "sandbox", "workspace"),
	}}
	controller := &Controller{config: config, configDB: store}

	env, err := controller.ensurePromptAttachLLMFacadeEnv(
		context.Background(),
		sandbox,
		execution.AgentConfig{Provider: "opencode", Model: "openai/gpt-test"},
		"run-opencode-attach",
	)
	if err != nil {
		t.Fatalf("ensurePromptAttachLLMFacadeEnv returned error: %v", err)
	}
	if env["LLM_API_PROTOCOL"] != llms.APIProtocolChatCompletions ||
		env["OPENCODE_CONFIG"] != "/root/.config/opencode/opencode.json" ||
		env["LLM_MODEL"] != "agent-compose/gpt-test" ||
		env["OPENCODE_MODEL"] != "agent-compose/gpt-test" {
		t.Fatalf("OpenCode facade env = %#v", env)
	}
	if env["AGENT_COMPOSE_SANDBOX_TOKEN"] == "" || len(store.tokens) != 1 {
		t.Fatalf("OpenCode token env = %q, saved tokens = %#v", env["AGENT_COMPOSE_SANDBOX_TOKEN"], store.tokens)
	}
	token := store.tokens[0]
	if token.Model != "gpt-test" || token.ProviderID != "openai-test" || token.Source != "agent" || token.RunID != "run-opencode-attach" {
		t.Fatalf("stored token = %#v", token)
	}
	configPath := filepath.Join(root, "sandbox", "home", ".config", "opencode", "opencode.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read OpenCode runtime config: %v", err)
	}
	if !strings.Contains(string(data), `"gpt-test"`) ||
		!strings.Contains(string(data), `"agent-compose"`) ||
		strings.Contains(string(data), `"agent-compose agent-compose"`) {
		t.Fatalf("OpenCode runtime config = %s", data)
	}
}

func isolatePromptAttachLLMEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"LLM_API_ENDPOINT",
		"LLM_API_PROTOCOL",
		"LLM_API_KEY",
		"OPENAI_API_KEY",
		"LLM_MODEL",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_API_ENDPOINT",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_MODEL",
		"CLAUDE_MODEL",
	} {
		t.Setenv(key, "")
	}
}

func TestEnsurePromptAttachLLMFacadeEnvPiUsesSharedRuntimeConfig(t *testing.T) {
	root := t.TempDir()
	config := &appconfig.Config{
		RuntimeBaseURL: "http://agent-compose.test:7410",
		GuestHomePath:  "/root",
	}
	store := &promptAttachFacadeStore{
		providers: []llms.Provider{{
			ID:             "openai-test",
			ProviderType:   llms.ProviderFamilyOpenAI,
			DefaultWireAPI: llms.APIProtocolResponses,
			BaseURL:        "https://openai.example.test/v1",
			APIKey:         "openai-key",
			Enabled:        true,
		}},
		models: []llms.Model{{ID: "gpt-test", Name: "gpt-test", DefaultModel: true, Enabled: true}},
	}
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{
		ID:            "sandbox-pi-attach",
		Driver:        driver.RuntimeDriverDocker,
		WorkspacePath: filepath.Join(root, "sandbox", "workspace"),
	}}
	controller := &Controller{config: config, configDB: store}

	env, err := controller.ensurePromptAttachLLMFacadeEnv(
		context.Background(),
		sandbox,
		execution.AgentConfig{Provider: "pi", Model: "openai-test/gpt-test"},
		"run-pi-attach",
	)
	if err != nil {
		t.Fatalf("ensurePromptAttachLLMFacadeEnv returned error: %v", err)
	}
	if env["LLM_API_PROTOCOL"] != llms.APIProtocolResponses || env["PI_CODING_AGENT_DIR"] != "/root/.pi/agent" {
		t.Fatalf("Pi facade env = %#v", env)
	}
	if env["AGENT_COMPOSE_SANDBOX_TOKEN"] == "" || len(store.tokens) != 1 {
		t.Fatalf("Pi token env = %q, saved tokens = %#v", env["AGENT_COMPOSE_SANDBOX_TOKEN"], store.tokens)
	}
	token := store.tokens[0]
	if token.Model != "gpt-test" || token.ProviderID != "openai-test" || token.Source != "agent" || token.RunID != "run-pi-attach" {
		t.Fatalf("stored token = %#v", token)
	}
	configPath := filepath.Join(root, "sandbox", "home", ".pi", "agent", "models.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read Pi runtime config: %v", err)
	}
	if !strings.Contains(string(data), `"gpt-test"`) || !strings.Contains(string(data), `"openai-responses"`) {
		t.Fatalf("Pi runtime config = %s", data)
	}
}

// TestEnsurePromptAttachLLMFacadeEnvDshMintsRunScopedFacade guards the pairing
// between promptAttachProviders and this switch: dsh is an accepted attach
// provider, so it must also get a facade environment here. The env
// EnsureDshFacadeConfig builds is per-exec and never persisted onto the
// sandbox, so a missing case leaves the guest with no endpoint, no token and
// no model — the profile falls back to its hardcoded default and the turn
// fails.
func TestEnsurePromptAttachLLMFacadeEnvDshMintsRunScopedFacade(t *testing.T) {
	isolatePromptAttachLLMEnv(t)
	config := &appconfig.Config{
		RuntimeBaseURL: "http://agent-compose.test:7410",
		GuestHomePath:  "/root",
	}
	store := &promptAttachFacadeStore{
		providers: []llms.Provider{{
			ID:             "openai-test",
			ProviderType:   llms.ProviderFamilyOpenAI,
			DefaultWireAPI: llms.APIProtocolResponses,
			BaseURL:        "https://openai.example.test/v1",
			APIKey:         "openai-key",
			Enabled:        true,
		}},
		models: []llms.Model{{ID: "gpt-test", Name: "gpt-test", DefaultModel: true, Enabled: true}},
	}
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-dsh-attach", Driver: driver.RuntimeDriverDocker}}
	controller := &Controller{config: config, configDB: store}

	env, err := controller.ensurePromptAttachLLMFacadeEnv(
		context.Background(),
		sandbox,
		execution.AgentConfig{Provider: "dsh", Model: "openai-test/gpt-test"},
		"run-dsh-attach",
	)
	if err != nil {
		t.Fatalf("ensurePromptAttachLLMFacadeEnv returned error: %v", err)
	}
	if env["DSH_MODEL"] != "gpt-test" || env["DSH_WIRE_API"] != "openai-responses" ||
		env["LLM_API_PROTOCOL"] != llms.APIProtocolResponses {
		t.Fatalf("DSH facade env = %#v", env)
	}
	if env["LLM_API_ENDPOINT"] != "http://agent-compose.test:7410/api/runtime/sandboxes/sandbox-dsh-attach/llm/openai/v1" {
		t.Fatalf("LLM_API_ENDPOINT = %q", env["LLM_API_ENDPOINT"])
	}
	if env["AGENT_COMPOSE_SANDBOX_TOKEN"] == "" || env["LLM_API_KEY"] != env["AGENT_COMPOSE_SANDBOX_TOKEN"] || len(store.tokens) != 1 {
		t.Fatalf("DSH token env = %#v, saved tokens = %#v", env, store.tokens)
	}
	if token := store.tokens[0]; token.Model != "gpt-test" || token.ProviderID != "openai-test" ||
		token.Source != "agent" || token.RunID != "run-dsh-attach" {
		t.Fatalf("stored token = %#v", token)
	}
}

// TestPromptAttachProvidersAllHaveFacadeCases is the general form of the bug
// above: every provider prompt attach accepts must resolve a facade
// environment, or its guest starts with no LLM credentials at all.
func TestPromptAttachProvidersAllHaveFacadeCases(t *testing.T) {
	isolatePromptAttachLLMEnv(t)
	store := &promptAttachFacadeStore{
		providers: []llms.Provider{
			{ID: "openai-test", ProviderType: llms.ProviderFamilyOpenAI, DefaultWireAPI: llms.APIProtocolResponses, BaseURL: "https://openai.example.test/v1", APIKey: "openai-key", Enabled: true},
			{ID: "anthropic-test", ProviderType: llms.ProviderFamilyAnthropic, DefaultWireAPI: llms.APIProtocolMessages, BaseURL: "https://anthropic.example.test", APIKey: "anthropic-key", Enabled: true},
		},
		models: []llms.Model{{ID: "gpt-test", Name: "gpt-test", DefaultModel: true, Enabled: true}},
	}
	models := map[string]string{"codex": "gpt-test", "claude": "", "opencode": "openai/gpt-test", "pi": "openai-test/gpt-test", "dsh": "openai-test/gpt-test"}
	for provider := range promptAttachProviders {
		root := t.TempDir()
		sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{
			ID:            "sandbox-" + provider,
			Driver:        driver.RuntimeDriverDocker,
			WorkspacePath: filepath.Join(root, "sandbox", "workspace"),
		}}
		controller := &Controller{
			config:   &appconfig.Config{RuntimeBaseURL: "http://agent-compose.test:7410", GuestHomePath: "/root"},
			configDB: store,
		}
		env, err := controller.ensurePromptAttachLLMFacadeEnv(
			context.Background(),
			sandbox,
			execution.AgentConfig{Provider: provider, Model: models[provider]},
			"run-"+provider,
		)
		if err != nil {
			t.Fatalf("%s: ensurePromptAttachLLMFacadeEnv returned error: %v", provider, err)
		}
		if env["AGENT_COMPOSE_SANDBOX_TOKEN"] == "" {
			t.Fatalf("%s: prompt attach accepts the provider but mints no facade token: %#v", provider, env)
		}
	}
}

// The start frame's model becomes opencode's --model, which overrides the
// OPENCODE_MODEL env the facade just exported. It therefore has to carry the
// facade's namespace-corrected model, not the agent-compose provider/model pair
// the agent configured — opencode has no entry for the latter and exits without
// reporting why.
func TestPromptAttachRuntimeModelUsesFacadeOpenCodeModel(t *testing.T) {
	managedEnv := map[string]string{"OPENCODE_MODEL": "agent-compose/gpt-test"}
	agent := execution.AgentConfig{Provider: "opencode", Model: "openai/gpt-test"}
	if model := promptAttachRuntimeModel(agent, managedEnv); model != "agent-compose/gpt-test" {
		t.Fatalf("opencode runtime model = %q", model)
	}
}

// Every other provider addresses models in agent-compose's own namespace, so
// their configured model must reach the runner untouched.
func TestPromptAttachRuntimeModelLeavesOtherProvidersUntouched(t *testing.T) {
	managedEnv := map[string]string{"OPENCODE_MODEL": "agent-compose/gpt-test"}
	for _, provider := range []string{"codex", "claude", "pi", "dsh"} {
		agent := execution.AgentConfig{Provider: provider, Model: "openai/gpt-test"}
		if model := promptAttachRuntimeModel(agent, managedEnv); model != "openai/gpt-test" {
			t.Fatalf("%s runtime model = %q", provider, model)
		}
	}
}

// A facade that resolved no opencode model must not blank out the configured
// one; the run should still get as far as opencode's own error reporting.
func TestPromptAttachRuntimeModelKeepsConfiguredModelWithoutFacadeModel(t *testing.T) {
	agent := execution.AgentConfig{Provider: "opencode", Model: "openai/gpt-test"}
	if model := promptAttachRuntimeModel(agent, map[string]string{}); model != "openai/gpt-test" {
		t.Fatalf("opencode runtime model without facade model = %q", model)
	}
}
