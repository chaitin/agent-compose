package runtimefacade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samber/do/v2"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
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
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, config)
	store, err := testutil.OpenConfigStore(t, di)
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

	env, err := EnsureSessionLLMFacadeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "codex", Model: "", Source: "test", RunID: "run-1"})
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

func TestEnsureSessionStartupFacadeConfigIncludesAllAvailableFamilies(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:       root,
		DbAddr:         filepath.Join(root, "data.db"),
		RuntimeBaseURL: "http://agent-compose.test:7410",
		GuestHomePath:  "/root",
	}
	di := do.New()
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, config)
	store, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
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

	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-startup-facades", Driver: driverpkg.RuntimeDriverDocker}}
	env, err := EnsureSessionStartupFacadeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Source: TokenSourceAgent, RunID: ""})
	if err != nil {
		t.Fatalf("EnsureSessionStartupFacadeConfig returned error: %v", err)
	}
	if env["ANTHROPIC_API_KEY"] == "" || env["ANTHROPIC_AUTH_TOKEN"] != env["ANTHROPIC_API_KEY"] || env["ANTHROPIC_BASE_URL"] == "" {
		t.Fatalf("Anthropic startup environment = %#v", env)
	}
	if env["OPENAI_API_KEY"] == "" || env["OPENAI_BASE_URL"] == "" {
		t.Fatalf("OpenAI startup environment = %#v", env)
	}
	if env["AGENT_COMPOSE_SANDBOX_TOKEN"] != "" || env["LLM_API_KEY"] != "" {
		t.Fatalf("startup environment unexpectedly contains common facade variables = %#v", env)
	}
	if env["ANTHROPIC_API_KEY"] == "anthropic-upstream-secret" || env["OPENAI_API_KEY"] == "openai-upstream-secret" {
		t.Fatalf("startup environment leaked an upstream credential = %#v", env)
	}

	anthropicToken, err := store.GetLLMFacadeToken(ctx, env["ANTHROPIC_API_KEY"])
	if err != nil {
		t.Fatalf("load Anthropic startup token: %v", err)
	}
	if anthropicToken.ProviderID != "anthropic-primary" || anthropicToken.WireAPI != llms.APIProtocolMessages {
		t.Fatalf("Anthropic startup token = %#v", anthropicToken)
	}
	openAIToken, err := store.GetLLMFacadeToken(ctx, env["OPENAI_API_KEY"])
	if err != nil {
		t.Fatalf("load OpenAI startup token: %v", err)
	}
	if openAIToken.ProviderID != "openai-primary" || openAIToken.WireAPI != "" {
		t.Fatalf("OpenAI startup token = %#v", openAIToken)
	}
}

func TestEnsureSessionStartupFacadeConfigSkipsUnavailableFamily(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{DataRoot: root, DbAddr: filepath.Join(root, "data.db"), RuntimeBaseURL: "http://agent-compose.test:7410"}
	di := do.New()
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, config)
	store, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	if err := store.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID:           "anthropic-only",
		Name:         "Anthropic",
		ProviderType: llms.ProviderFamilyAnthropic,
		BaseURL:      "https://anthropic.upstream.test",
		APIKey:       "anthropic-upstream-secret",
		Scope:        llms.ProviderScopeSystem,
	}, llms.Model{ID: "claude-only", Name: "claude-only", Enabled: true, DefaultModel: true, Scope: llms.ProviderScopeSystem}); err != nil {
		t.Fatalf("save Anthropic provider: %v", err)
	}

	env, err := EnsureSessionStartupFacadeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-anthropic-only"}}, Source: TokenSourceAgent, RunID: ""})
	if err != nil {
		t.Fatalf("EnsureSessionStartupFacadeConfig returned error: %v", err)
	}
	if env["ANTHROPIC_API_KEY"] == "" || env["OPENAI_API_KEY"] != "" || env["OPENAI_BASE_URL"] != "" {
		t.Fatalf("single-family startup environment = %#v", env)
	}
}

func TestEnsureSessionLLMFacadeConfigRejectsManagedCodexWithoutReachableFacade(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:       root,
		DbAddr:         filepath.Join(root, "data.db"),
		LLMAPIEndpoint: "https://llm.example.test/v1",
		LLMAPIKey:      "test-key",
		LLMModel:       "gpt-test",
		LLMAPIProtocol: llms.APIProtocolResponses,
		HttpListen:     "127.0.0.1:7410",
		GuestHomePath:  "/root",
	}
	di := do.New()
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, config)
	store, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{
		ID:            "sandbox-runtimefacade-unreachable",
		Driver:        driverpkg.RuntimeDriverDocker,
		WorkspacePath: filepath.Join(root, "sandboxes", "sandbox-runtimefacade-unreachable", "workspace"),
	}}

	env, err := EnsureSessionLLMFacadeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "codex", Model: "", Source: "test", RunID: "run-1"})
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("EnsureSessionLLMFacadeConfig error = %v, want failed precondition", err)
	}
	if len(env) != 0 {
		t.Fatalf("EnsureSessionLLMFacadeConfig env = %#v, want empty", env)
	}
	if !strings.Contains(err.Error(), llms.RuntimeBaseURLEnvName) {
		t.Fatalf("EnsureSessionLLMFacadeConfig error = %q, want actionable runtime base URL configuration", err)
	}
}

func TestEnsureSessionLLMFacadeConfigAllowsUnmanagedCodexWithoutFacade(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:      root,
		DbAddr:        filepath.Join(root, "data.db"),
		HttpListen:    "127.0.0.1:7410",
		GuestHomePath: "/root",
	}
	di := do.New()
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, config)
	store, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-runtimefacade-unmanaged", Driver: driverpkg.RuntimeDriverDocker}}

	env, err := EnsureSessionLLMFacadeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "codex", Model: "", Source: "test", RunID: "run-1"})
	if err != nil || len(env) != 0 {
		t.Fatalf("EnsureSessionLLMFacadeConfig env = %#v, error = %v; want unmanaged no-op", env, err)
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
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, config)
	store, err := testutil.OpenConfigStore(t, di)
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
			{Name: "LLM_API_KEY", Value: "openai-key"},
			{Name: "LLM_MODEL", Value: "gpt-test"},
		},
	}
	claude, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "claude", Model: "", Source: "agent", RunID: "run-claude"})
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

	openAI, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "opencode", Model: "openai/gpt-test", Source: TokenSourceAgent, RunID: "run-openai"})
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig opencode openai returned error: %v", err)
	}
	if openAI.Env["LLM_API_PROTOCOL"] != llms.APIProtocolChatCompletions || openAI.Env["OPENCODE_CONFIG"] == "" {
		t.Fatalf("opencode openai env = %#v", openAI.Env)
	}

	anthropic, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "opencode", Model: "anthropic/claude-test", Source: TokenSourceAgent, RunID: "run-anthropic"})
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig opencode anthropic returned error: %v", err)
	}
	if anthropic.Env["LLM_API_PROTOCOL"] != llms.APIProtocolMessages || anthropic.Env["ANTHROPIC_BASE_URL"] == "" || anthropic.Env["ANTHROPIC_AUTH_TOKEN"] == "" || anthropic.Env["OPENCODE_CONFIG"] == "" {
		t.Fatalf("opencode anthropic env = %#v", anthropic.Env)
	}

	pi, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "pi", Model: "openai/gpt-test", Source: TokenSourceAgent, RunID: "run-pi"})
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig pi returned error: %v", err)
	}
	if pi.Env["LLM_API_PROTOCOL"] != llms.APIProtocolResponses || pi.Env["PI_CODING_AGENT_DIR"] != "/root/.pi/agent" || pi.Env["OPENAI_API_KEY"] == "" {
		t.Fatalf("pi env = %#v", pi.Env)
	}
	piConfigPath := filepath.Join(execution.HostSandboxHome(session), ".pi", "agent", "models.json")
	piConfig, err := os.ReadFile(piConfigPath)
	if err != nil {
		t.Fatalf("read pi models.json: %v", err)
	}
	for _, want := range []string{"agent-compose", "gpt-test", "openai-responses", "$AGENT_COMPOSE_SANDBOX_TOKEN"} {
		if !strings.Contains(string(piConfig), want) {
			t.Fatalf("pi config %q does not contain %q", piConfig, want)
		}
	}
	if strings.Contains(string(piConfig), pi.Env["AGENT_COMPOSE_SANDBOX_TOKEN"]) {
		t.Fatal("pi config persisted the run-scoped token")
	}
	token, err := store.GetLLMFacadeToken(ctx, pi.Env["AGENT_COMPOSE_SANDBOX_TOKEN"])
	if err != nil || token.RunID != "run-pi" || token.ProviderID == "" {
		t.Fatalf("pi token = %#v, err=%v", token, err)
	}

	custom, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "opencode", Model: "custom/gpt-custom", Source: TokenSourceSchedulerCommand, RunID: "run-custom"})
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig opencode custom returned error: %v", err)
	}
	if custom.Env["LLM_API_PROTOCOL"] != llms.APIProtocolChatCompletions || custom.Env["OPENAI_BASE_URL"] == "" {
		t.Fatalf("opencode custom env = %#v", custom.Env)
	}

	noop, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "opencode", Model: "opencode/local", Source: "", RunID: ""})
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig opencode local returned error: %v", err)
	}
	if len(noop.Env) != 0 {
		t.Fatalf("opencode local env = %#v", noop.Env)
	}
	if _, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "opencode", Model: "bad-model", Source: "", RunID: ""}); err == nil {
		t.Fatalf("expected invalid opencode model error")
	}
	if env, err := EnsureSessionLLMFacadeConfig(ctx, SessionFacadeConfigRequest{Config: nil, Store: store, Session: session, Agent: "codex", Model: "", Source: "", RunID: ""}); err != nil || env != nil {
		t.Fatalf("nil config env=%#v err=%v", env, err)
	}
	if !HasAnthropicProviderKey(ctx, config, store) {
		t.Fatalf("expected anthropic provider key")
	}
	if got := firstNonEmpty(" \t", "value"); got != "value" {
		t.Fatalf("firstNonEmpty = %q, want value", got)
	}
}

func TestEnsureSessionAgentRuntimeConfigClaudePreservesProviderlessCompatibilityToken(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:       root,
		DbAddr:         filepath.Join(root, "data.db"),
		LLMAPIEndpoint: "https://openai.example.test/base",
		LLMAPIProtocol: llms.APIProtocolResponses,
		LLMAPIKey:      "generic-provider-key",
		LLMModel:       "generic-model",
		RuntimeBaseURL: "http://agent-compose.test:7410",
	}
	di := do.New()
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, config)
	store, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-claude-compat", Driver: driverpkg.RuntimeDriverDocker}}

	runtimeConfig, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "claude", Model: "", Source: "test", RunID: "run-compat"})
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig returned error: %v", err)
	}
	rawToken := runtimeConfig.Env["AGENT_COMPOSE_SANDBOX_TOKEN"]
	if rawToken == "" {
		t.Fatal("AGENT_COMPOSE_SANDBOX_TOKEN is empty")
	}
	token, err := store.GetLLMFacadeToken(ctx, rawToken)
	if err != nil {
		t.Fatalf("GetLLMFacadeToken returned error: %v", err)
	}
	if token.ProviderID != "" || token.Model != "" {
		t.Fatalf("compatibility token = %#v", token)
	}
	target, err := llms.ResolveRuntimeLLMTarget(ctx, config, store, config.LLMModel, token.ProviderID)
	if err != nil {
		t.Fatalf("resolve providerless compatibility target: %v", err)
	}
	if target.Provider.ProviderType != llms.ProviderFamilyOpenAI || target.WireAPI != llms.APIProtocolResponses || target.Provider.APIKey == "" {
		t.Fatalf("providerless compatibility target = family %q, wire API %q", target.Provider.ProviderType, target.WireAPI)
	}
}

func isolateLLMEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"LLM_API_ENDPOINT",
		"LLM_API_PROTOCOL",
		"LLM_API_KEY",
		"LLM_API_HEADERS",
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
