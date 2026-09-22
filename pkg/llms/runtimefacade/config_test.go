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
	// The agent facade resolves against the model catalog, not the legacy
	// daemon-environment provider, so the test declares its connection there.
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{
		Default: "openai/gpt-test",
		Providers: map[string]llms.CatalogProvider{
			"openai": {
				BaseURL:  catalogStringPointer("https://llm.example.test/v1"),
				Protocol: catalogStringPointer(llms.APIProtocolResponses),
				APIKey:   catalogStringPointer("test-key"),
				Models:   []llms.CatalogModel{{ID: "gpt-test"}},
			},
		},
	}); err != nil {
		t.Fatalf("apply model catalog: %v", err)
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
	if env[llms.GuestModelEnvName] != "gpt-test" {
		t.Fatalf("env[%s] = %q, want the catalog default", llms.GuestModelEnvName, env[llms.GuestModelEnvName])
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

// unreachableFacadeDaemonConfig is a daemon that binds only loopback and has no
// sandbox-reachable runtime base URL, so a managed facade could never reach it.
func unreachableFacadeDaemonConfig(root string) *appconfig.Config {
	return &appconfig.Config{
		DataRoot:       root,
		DbAddr:         filepath.Join(root, "data.db"),
		LLMAPIEndpoint: "https://llm.example.test/v1",
		LLMAPIKey:      "test-key",
		LLMModel:       "gpt-test",
		LLMAPIProtocol: llms.APIProtocolResponses,
		HttpListen:     "127.0.0.1:7410",
		GuestHomePath:  "/root",
	}
}

// TestEnsureSessionLLMFacadeConfigAllowsUnmanagedCodexWithoutReachableFacade
// covers an agent the daemon has no model for on a daemon with no
// sandbox-reachable URL: the catalog is empty, so the agent is unmanaged and
// keeps its own login. The missing daemon URL is not the unmanaged agent's
// problem, so the facade must be a no-op rather than an error.
func TestEnsureSessionLLMFacadeConfigAllowsUnmanagedCodexWithoutReachableFacade(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config := unreachableFacadeDaemonConfig(root)
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
	if err != nil || env != nil {
		t.Fatalf("EnsureSessionLLMFacadeConfig env = %#v, error = %v; want an unmanaged no-op", env, err)
	}
}

// TestEnsureSessionLLMFacadeConfigRejectsManagedCodexWithoutReachableFacade is
// the other direction: once the catalog declares a model, the daemon does manage
// codex, and it cannot deliver the credential without a sandbox-reachable URL.
// That is a configuration fault the operator must see, not a no-op.
func TestEnsureSessionLLMFacadeConfigRejectsManagedCodexWithoutReachableFacade(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config := unreachableFacadeDaemonConfig(root)
	di := do.New()
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, config)
	store, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{
		Default: "openai/gpt-test",
		Providers: map[string]llms.CatalogProvider{
			"openai": {
				BaseURL:  catalogStringPointer("https://llm.example.test/v1"),
				Protocol: catalogStringPointer(llms.APIProtocolResponses),
				APIKey:   catalogStringPointer("test-key"),
				Models:   []llms.CatalogModel{{ID: "gpt-test"}},
			},
		},
	}); err != nil {
		t.Fatalf("apply model catalog: %v", err)
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

// TestEnsureSessionLLMFacadeConfigAllowsUnmanagedCodexWithoutFacade covers an
// agent the daemon has no model for: the catalog is empty, so the facade has
// nothing to apply and codex keeps its own login.
func TestEnsureSessionLLMFacadeConfigAllowsUnmanagedCodexWithoutFacade(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:       root,
		DbAddr:         filepath.Join(root, "data.db"),
		RuntimeBaseURL: "http://agent-compose.test:7410",
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
	// The agent facade resolves against the model catalog; a bare model is
	// selected by the catalog default or by the one connection that serves it.
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{
		Default: "openai/gpt-test",
		Providers: map[string]llms.CatalogProvider{
			"openai": {
				BaseURL:  catalogStringPointer("https://openai.example.test/v1"),
				Protocol: catalogStringPointer(llms.APIProtocolResponses),
				APIKey:   catalogStringPointer("openai-key"),
				Models:   []llms.CatalogModel{{ID: "gpt-test"}},
			},
			"anthropic": {
				BaseURL:  catalogStringPointer("https://anthropic.example.test"),
				Protocol: catalogStringPointer(llms.APIProtocolMessages),
				APIKey:   catalogStringPointer("anthropic-key"),
				Models:   []llms.CatalogModel{{ID: "claude-test"}},
			},
		},
	}); err != nil {
		t.Fatalf("apply model catalog: %v", err)
	}
	session := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:            "sandbox-claude",
			Driver:        driverpkg.RuntimeDriverDocker,
			WorkspacePath: filepath.Join(root, "sandboxes", "sandbox-claude", "workspace"),
		},
	}

	// claude declares no model, so the catalog default supplies one. That
	// default is served over responses while claude speaks messages, so the
	// facade converts and LLM_API_PROTOCOL still names the upstream.
	claude, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "claude", Model: "", Source: "agent", RunID: "run-claude"})
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig claude returned error: %v", err)
	}
	if claude.Model != "gpt-test" || claude.Env["LLM_API_PROTOCOL"] != llms.APIProtocolResponses || claude.Env["ANTHROPIC_MODEL"] != "gpt-test" {
		t.Fatalf("claude env = %#v, model = %q", claude.Env, claude.Model)
	}
	if claude.Env["ANTHROPIC_BASE_URL"] == "" || claude.Env["ANTHROPIC_AUTH_TOKEN"] == "" || claude.Env["ANTHROPIC_AUTH_TOKEN"] != claude.Env["ANTHROPIC_API_KEY"] {
		t.Fatalf("claude anthropic facade env = %#v", claude.Env)
	}
	claudeToken, err := store.GetLLMFacadeToken(ctx, claude.Env["AGENT_COMPOSE_SANDBOX_TOKEN"])
	if err != nil {
		t.Fatalf("claude token not stored: %v", err)
	}
	if claudeToken.ProviderID != "openai" || claudeToken.WireAPI != llms.APIProtocolMessages {
		t.Fatalf("claude token = %#v, want the messages ingress to the openai connection", claudeToken)
	}
	if claude.Env["AGENT_COMPOSE_SESSION_TOKEN"] != "" {
		t.Fatalf("claude emitted deprecated session token env")
	}

	// gpt-test is bound to exactly one connection, so opencode resolves it
	// without the agent naming the connection.
	openAI, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "opencode", Model: "gpt-test", Source: TokenSourceAgent, RunID: "run-openai"})
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig opencode openai returned error: %v", err)
	}
	if openAI.Model != "agent-compose/gpt-test" || openAI.Env["LLM_API_PROTOCOL"] != llms.APIProtocolResponses || openAI.Env["OPENCODE_CONFIG"] == "" {
		t.Fatalf("opencode openai env = %#v, model = %q", openAI.Env, openAI.Model)
	}
	openAIToken, err := store.GetLLMFacadeToken(ctx, openAI.Env["AGENT_COMPOSE_SANDBOX_TOKEN"])
	if err != nil {
		t.Fatalf("opencode openai token not stored: %v", err)
	}
	// OpenCode cannot speak responses, so the ingress is chat completions even
	// though the upstream is responses.
	if openAIToken.ProviderID != "openai" || openAIToken.WireAPI != llms.APIProtocolChatCompletions {
		t.Fatalf("opencode openai token = %#v, want chat completions to the openai connection", openAIToken)
	}

	anthropic, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "opencode", Model: "claude-test", Source: TokenSourceAgent, RunID: "run-anthropic"})
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig opencode anthropic returned error: %v", err)
	}
	if anthropic.Env["LLM_API_PROTOCOL"] != llms.APIProtocolMessages || anthropic.Env["ANTHROPIC_BASE_URL"] == "" || anthropic.Env["ANTHROPIC_AUTH_TOKEN"] == "" || anthropic.Env["OPENCODE_CONFIG"] == "" {
		t.Fatalf("opencode anthropic env = %#v", anthropic.Env)
	}

	pi, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "pi", Model: "gpt-test", Source: TokenSourceAgent, RunID: "run-pi"})
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig pi returned error: %v", err)
	}
	if pi.Model != "agent-compose/gpt-test" || pi.Env["LLM_API_PROTOCOL"] != llms.APIProtocolResponses || pi.Env["PI_CODING_AGENT_DIR"] != "/root/.pi/agent" || pi.Env["OPENAI_API_KEY"] == "" {
		t.Fatalf("pi env = %#v, model = %q", pi.Env, pi.Model)
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
	if err != nil || token.RunID != "run-pi" || token.ProviderID != "openai" {
		t.Fatalf("pi token = %#v, err=%v", token, err)
	}

	// A model no configured connection serves is a configuration error rather
	// than a silent no-op.
	if _, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "opencode", Model: "unknown-model", Source: "", RunID: ""}); !errors.Is(err, llms.ErrAmbiguousConnection) {
		t.Fatalf("unknown model error = %v, want an ambiguous-connection error", err)
	}
	if env, err := EnsureSessionLLMFacadeConfig(ctx, SessionFacadeConfigRequest{Config: nil, Store: store, Session: session, Agent: "codex", Model: "", Source: "", RunID: ""}); err != nil || env != nil {
		t.Fatalf("nil config env=%#v err=%v", env, err)
	}
	if got := firstNonEmpty(" \t", "value"); got != "value" {
		t.Fatalf("firstNonEmpty = %q, want value", got)
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

// Every facade reports the model the guest runner must address, not only
// opencode: the runner forwards it to the agent CLI verbatim, and pi/opencode
// address models through the provider key the facade wrote into their config.
func TestEnsureSessionAgentRuntimeConfigReportsResolvedGuestModel(t *testing.T) {
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
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{
		Default: "openai/gpt-test",
		Providers: map[string]llms.CatalogProvider{
			"openai": {
				BaseURL:  catalogStringPointer("https://openai.example.test/v1"),
				Protocol: catalogStringPointer(llms.APIProtocolResponses),
				APIKey:   catalogStringPointer("openai-key"),
				Models:   []llms.CatalogModel{{ID: "gpt-test"}},
			},
		},
	}); err != nil {
		t.Fatalf("apply model catalog: %v", err)
	}
	session := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:            "sandbox-guest-model",
			Driver:        driverpkg.RuntimeDriverDocker,
			WorkspacePath: filepath.Join(root, "sandboxes", "sandbox-guest-model", "workspace"),
		},
	}
	for _, tc := range []struct {
		agent string
		want  string
	}{
		{agent: "codex", want: "gpt-test"},
		{agent: "claude", want: "gpt-test"},
		{agent: "pi", want: "agent-compose/gpt-test"},
		{agent: "dsh", want: "gpt-test"},
		{agent: "opencode", want: "agent-compose/gpt-test"},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			result, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{
				Config: config, Store: store, Session: session, Agent: tc.agent, Model: "gpt-test", Source: TokenSourceAgent, RunID: "run-" + tc.agent,
			})
			if err != nil {
				t.Fatalf("EnsureSessionAgentRuntimeConfig %s returned error: %v", tc.agent, err)
			}
			if result.Model != tc.want {
				t.Fatalf("%s runtime model = %q, want %q", tc.agent, result.Model, tc.want)
			}
			if got := result.Env[llms.GuestModelEnvName]; got != tc.want {
				t.Fatalf("%s env[%s] = %q, want %q", tc.agent, llms.GuestModelEnvName, got, tc.want)
			}
		})
	}
}

// catalogStringPointer adapts a literal to ModelCatalog's optional fields,
// which distinguish "unset" from "explicitly empty".
func catalogStringPointer(value string) *string {
	return &value
}
