package runtimefacade_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/llms/runtimefacade"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/configstore"
)

func TestEnsureSessionLLMFacadeConfigCreatesCodexEnvAndToken(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:               root,
		DbAddr:                 filepath.Join(root, "data.db"),
		LLMAPIEndpoint:         "https://llm.example.test/v1",
		LLMAPIKey:              "test-key",
		LLMModel:               "gpt-test",
		LLMAPIProtocol:         "responses",
		RuntimeBaseURL:         "http://agent-compose.test:7410",
		GuestHomePath:          "/root",
		CodexRequestMaxRetries: 2,
		CodexStreamMaxRetries:  3,
		CodexStreamIdleTimeout: 4 * time.Second,
	}
	di := do.New()
	do.ProvideValue(di, config)
	store, err := configstore.NewConfigStore(di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	session := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:            "sandbox-runtimefacade",
			Driver:        driverpkg.RuntimeDriverDocker,
			WorkspacePath: filepath.Join(root, "sandboxes", "sandbox-runtimefacade", "workspace"),
		},
	}

	env, err := runtimefacade.EnsureSessionLLMFacadeConfig(ctx, config, store, session, "codex", "", "test", "run-1")
	if err != nil {
		t.Fatalf("EnsureSessionLLMFacadeConfig returned error: %v", err)
	}
	if env["LLM_API_PROTOCOL"] != llms.APIProtocolResponses {
		t.Fatalf("LLM_API_PROTOCOL = %q, want responses", env["LLM_API_PROTOCOL"])
	}
	if env["OPENAI_BASE_URL"] != "http://agent-compose.test:7410/api/runtime/sandboxes/sandbox-runtimefacade/llm/openai/v1" {
		t.Fatalf("OPENAI_BASE_URL = %q", env["OPENAI_BASE_URL"])
	}
	if env["AGENT_COMPOSE_SANDBOX_TOKEN"] == "" {
		t.Fatalf("AGENT_COMPOSE_SANDBOX_TOKEN is empty")
	}
	if env["AGENT_COMPOSE_SESSION_TOKEN"] != "" {
		t.Fatalf("AGENT_COMPOSE_SESSION_TOKEN should not be emitted")
	}
	token, err := store.GetLLMFacadeToken(ctx, env["AGENT_COMPOSE_SANDBOX_TOKEN"])
	if err != nil {
		t.Fatalf("GetLLMFacadeToken returned error: %v", err)
	}
	if token.SandboxID != session.Summary.ID || token.Model != "gpt-test" || token.Source != "test" || token.RunID != "run-1" {
		t.Fatalf("stored token = %#v", token)
	}
	codexConfig, err := os.ReadFile(filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read Codex runtime config: %v", err)
	}
	for _, want := range []string{"request_max_retries = 2", "stream_max_retries = 3", "stream_idle_timeout_ms = 4000"} {
		if !strings.Contains(string(codexConfig), want) {
			t.Fatalf("Codex runtime config %q does not contain %q", string(codexConfig), want)
		}
	}
}

func TestEnsureSessionCodexConfigBridgesResponsesIngressToChatUpstream(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:       root,
		DbAddr:         filepath.Join(root, "data.db"),
		LLMAPIEndpoint: "https://chat.example.test/v1",
		LLMAPIProtocol: llms.APIProtocolChatCompletions,
		LLMAPIKey:      "chat-key",
		LLMModel:       "chat-model",
		RuntimeBaseURL: "http://agent-compose.test:7410",
		GuestHomePath:  "/root",
	}
	di := do.New()
	do.ProvideValue(di, config)
	store, err := configstore.NewConfigStore(di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	session := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:            "sandbox-codex-chat",
			Driver:        driverpkg.RuntimeDriverDocker,
			WorkspacePath: filepath.Join(root, "sandboxes", "sandbox-codex-chat", "workspace"),
		},
	}

	env, err := runtimefacade.EnsureSessionLLMFacadeConfig(ctx, config, store, session, "codex", "", runtimefacade.TokenSourceAgent, "run-chat")
	if err != nil {
		t.Fatalf("EnsureSessionLLMFacadeConfig returned error: %v", err)
	}
	if env["LLM_API_PROTOCOL"] != llms.APIProtocolResponses {
		t.Fatalf("LLM_API_PROTOCOL = %q, want responses", env["LLM_API_PROTOCOL"])
	}
	token, err := store.GetLLMFacadeToken(ctx, env["AGENT_COMPOSE_SANDBOX_TOKEN"])
	if err != nil {
		t.Fatalf("GetLLMFacadeToken returned error: %v", err)
	}
	if token.WireAPI != llms.APIProtocolResponses || token.ProviderID == "" || token.Model != "chat-model" {
		t.Fatalf("stored token = %#v", token)
	}
	target, err := llms.ResolveRuntimeLLMTarget(ctx, config, store, "chat-model", token.ProviderID)
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTarget returned error: %v", err)
	}
	if target.WireAPI != llms.APIProtocolChatCompletions {
		t.Fatalf("upstream wire API = %q, want chat_completions", target.WireAPI)
	}
	configBytes, err := os.ReadFile(filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read Codex config: %v", err)
	}
	if !strings.Contains(string(configBytes), `wire_api = "responses"`) {
		t.Fatalf("Codex config = %s", configBytes)
	}
}

func TestEnsureSessionAgentRuntimeConfigClaudeAndOpenCodeWorkflows(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:       root,
		DbAddr:         filepath.Join(root, "data.db"),
		LLMAPIKey:      "global-provider-key",
		RuntimeBaseURL: "http://agent-compose.test:7410",
		GuestHomePath:  "/root",
	}
	di := do.New()
	do.ProvideValue(di, config)
	store, err := configstore.NewConfigStore(di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	session := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:            "sandbox-claude",
			Driver:        driverpkg.RuntimeDriverDocker,
			WorkspacePath: filepath.Join(root, "sandboxes", "sandbox-claude", "workspace"),
		},
		ProviderEnvItems: []domain.SandboxEnvVar{
			{Name: "ANTHROPIC_BASE_URL", Value: "https://anthropic.example.test"},
			{Name: "ANTHROPIC_API_KEY", Value: "anthropic-key"},
			{Name: "ANTHROPIC_MODEL", Value: "claude-test"},
			{Name: "LLM_API_ENDPOINT", Value: "https://openai.example.test/v1"},
			{Name: "LLM_API_PROTOCOL", Value: llms.APIProtocolChatCompletions},
			{Name: "LLM_API_KEY", Value: "openai-key"},
			{Name: "LLM_MODEL", Value: "gpt-test"},
		},
	}
	claude, err := runtimefacade.EnsureSessionAgentRuntimeConfig(ctx, config, store, session, "claude", "", "agent", "run-claude")
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig claude returned error: %v", err)
	}
	if claude.Env["LLM_API_PROTOCOL"] != llms.APIProtocolMessages || claude.Env["ANTHROPIC_MODEL"] != "claude-test" {
		t.Fatalf("claude env = %#v", claude.Env)
	}
	if claude.Env["ANTHROPIC_BASE_URL"] == "" || claude.Env["ANTHROPIC_AUTH_TOKEN"] == "" || claude.Env["ANTHROPIC_AUTH_TOKEN"] != claude.Env["ANTHROPIC_API_KEY"] {
		t.Fatalf("claude anthropic facade env = %#v", claude.Env)
	}
	if _, err := store.GetLLMFacadeToken(ctx, claude.Env["AGENT_COMPOSE_SANDBOX_TOKEN"]); err != nil {
		t.Fatalf("claude token not stored: %v", err)
	}
	if claude.Env["AGENT_COMPOSE_SESSION_TOKEN"] != "" {
		t.Fatalf("claude emitted deprecated session token env")
	}

	openAI, err := runtimefacade.EnsureSessionAgentRuntimeConfig(ctx, config, store, session, "opencode", "openai/gpt-test", runtimefacade.TokenSourceAgent, "run-openai")
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig opencode openai returned error: %v", err)
	}
	if openAI.Env["LLM_API_PROTOCOL"] != llms.APIProtocolResponses || openAI.Env["OPENCODE_CONFIG"] == "" {
		t.Fatalf("opencode openai env = %#v", openAI.Env)
	}
	openAIToken, err := store.GetLLMFacadeToken(ctx, openAI.Env["AGENT_COMPOSE_SANDBOX_TOKEN"])
	if err != nil {
		t.Fatalf("GetLLMFacadeToken opencode openai returned error: %v", err)
	}
	openAITarget, err := llms.ResolveRuntimeLLMTarget(ctx, config, store, "gpt-test", openAIToken.ProviderID)
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTarget opencode openai returned error: %v", err)
	}
	if openAIToken.WireAPI != llms.APIProtocolResponses || openAITarget.WireAPI != llms.APIProtocolChatCompletions {
		t.Fatalf("opencode token wire api = %q, upstream wire api = %q", openAIToken.WireAPI, openAITarget.WireAPI)
	}

	anthropic, err := runtimefacade.EnsureSessionAgentRuntimeConfig(ctx, config, store, session, "opencode", "anthropic/claude-test", runtimefacade.TokenSourceAgent, "run-anthropic")
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig opencode anthropic returned error: %v", err)
	}
	if anthropic.Env["LLM_API_PROTOCOL"] != llms.APIProtocolMessages || anthropic.Env["ANTHROPIC_BASE_URL"] == "" || anthropic.Env["ANTHROPIC_AUTH_TOKEN"] == "" || anthropic.Env["OPENCODE_CONFIG"] == "" {
		t.Fatalf("opencode anthropic env = %#v", anthropic.Env)
	}

	custom, err := runtimefacade.EnsureSessionAgentRuntimeConfig(ctx, config, store, session, "opencode", "custom/gpt-custom", runtimefacade.TokenSourceLoaderCommand, "run-custom")
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig opencode custom returned error: %v", err)
	}
	if custom.Env["LLM_API_PROTOCOL"] != llms.APIProtocolChatCompletions || custom.Env["OPENAI_BASE_URL"] == "" {
		t.Fatalf("opencode custom env = %#v", custom.Env)
	}

	noop, err := runtimefacade.EnsureSessionAgentRuntimeConfig(ctx, config, store, session, "opencode", "opencode/local", "", "")
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig opencode local returned error: %v", err)
	}
	if len(noop.Env) != 0 {
		t.Fatalf("opencode local env = %#v", noop.Env)
	}
	if _, err := runtimefacade.EnsureSessionAgentRuntimeConfig(ctx, config, store, session, "opencode", "bad-model", "", ""); err == nil {
		t.Fatalf("expected invalid opencode model error")
	}
	if env, err := runtimefacade.EnsureSessionLLMFacadeConfig(ctx, nil, store, session, "codex", "", "", ""); err != nil || env != nil {
		t.Fatalf("nil config env=%#v err=%v", env, err)
	}
}

func TestEnsureSessionAgentRuntimeConfigClaudeRejectsGenericOpenAIConfig(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:       root,
		DbAddr:         filepath.Join(root, "data.db"),
		LLMAPIKey:      "generic-provider-key",
		RuntimeBaseURL: "http://agent-compose.test:7410",
	}
	di := do.New()
	do.ProvideValue(di, config)
	store, err := configstore.NewConfigStore(di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-claude-compat", Driver: driverpkg.RuntimeDriverDocker}}

	runtimeConfig, err := runtimefacade.EnsureSessionAgentRuntimeConfig(ctx, config, store, session, "claude", "", "test", "run-compat")
	if err == nil {
		t.Fatal("EnsureSessionAgentRuntimeConfig returned nil error for generic OpenAI config")
	}
	if len(runtimeConfig.Env) != 0 {
		t.Fatalf("runtime config = %#v, want no facade environment", runtimeConfig)
	}
	providers, listErr := store.ListEnabledLLMProviders(ctx)
	if listErr != nil {
		t.Fatalf("ListEnabledLLMProviders returned error: %v", listErr)
	}
	if len(providers) != 0 {
		t.Fatalf("generic OpenAI config created Anthropic providers: %#v", providers)
	}
}

func TestEnsureSessionOpenCodeCustomProviderInheritsDaemonConfig(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:       root,
		DbAddr:         filepath.Join(root, "data.db"),
		LLMAPIEndpoint: "https://daemon.example.test/v1",
		LLMAPIProtocol: llms.APIProtocolChatCompletions,
		LLMAPIKey:      "daemon-key",
		RuntimeBaseURL: "http://agent-compose.test:7410",
		GuestHomePath:  "/root",
	}
	di := do.New()
	do.ProvideValue(di, config)
	store, err := configstore.NewConfigStore(di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	session := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:            "sandbox-custom-layered",
			Driver:        driverpkg.RuntimeDriverDocker,
			WorkspacePath: filepath.Join(root, "sandboxes", "sandbox-custom-layered", "workspace"),
		},
		ProviderEnvItems: []domain.SandboxEnvVar{{Name: "LLM_API_ENDPOINT", Value: "https://sandbox.example.test/v1"}},
	}

	runtimeConfig, err := runtimefacade.EnsureSessionAgentRuntimeConfig(ctx, config, store, session, "opencode", "custom/custom-model", runtimefacade.TokenSourceAgent, "run-custom-layered")
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig returned error: %v", err)
	}
	token, err := store.GetLLMFacadeToken(ctx, runtimeConfig.Env["AGENT_COMPOSE_SANDBOX_TOKEN"])
	if err != nil {
		t.Fatalf("GetLLMFacadeToken returned error: %v", err)
	}
	target, err := llms.ResolveRuntimeLLMTarget(ctx, config, store, "custom-model", token.ProviderID)
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTarget returned error: %v", err)
	}
	if target.Provider.BaseURL != "https://sandbox.example.test/v1" || target.Provider.APIKey != "daemon-key" || target.WireAPI != llms.APIProtocolChatCompletions {
		t.Fatalf("custom provider target = %#v", target)
	}
	if token.WireAPI != llms.APIProtocolChatCompletions || runtimeConfig.Env["LLM_API_PROTOCOL"] != llms.APIProtocolChatCompletions {
		t.Fatalf("custom ingress token=%q env=%q", token.WireAPI, runtimeConfig.Env["LLM_API_PROTOCOL"])
	}
}

func isolateLLMEnv(t *testing.T) {
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
