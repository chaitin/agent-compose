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

func TestEnsureSessionStartupFacadeConfigSupportsLegacyProviderAliases(t *testing.T) {
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
		ID: "anthropic-primary", Name: "Anthropic", ProviderType: llms.ProviderFamilyAnthropic,
		DefaultWireAPI: llms.APIProtocolMessages, BaseURL: "https://anthropic.example.test",
		APIKey: "anthropic-upstream-secret", Scope: llms.ProviderScopeSystem, Weight: 1,
	}, llms.Model{ID: "claude-model", Name: "claude-model", Enabled: true, DefaultModel: true, Scope: llms.ProviderScopeSystem}); err != nil {
		t.Fatalf("save Anthropic provider: %v", err)
	}
	if err := store.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID: "openai-primary", Name: "OpenAI", ProviderType: llms.ProviderFamilyOpenAI,
		DefaultWireAPI: llms.APIProtocolResponses, BaseURL: "https://openai.example.test",
		APIKey: "openai-upstream-secret", Scope: llms.ProviderScopeSystem, Weight: 2,
	}, llms.Model{ID: "openai-model", Name: "openai-model", Enabled: true, Scope: llms.ProviderScopeSystem}); err != nil {
		t.Fatalf("save OpenAI provider: %v", err)
	}

	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-startup-facades", Driver: driverpkg.RuntimeDriverDocker}}
	claudeEnv, err := EnsureSessionStartupFacadeConfig(ctx, SessionFacadeConfigRequest{
		Config: config, Store: store, Session: session, Agent: "claude", Source: TokenSourceAgent,
	})
	if err != nil {
		t.Fatalf("EnsureSessionStartupFacadeConfig returned error: %v", err)
	}
	if claudeEnv["ANTHROPIC_API_KEY"] == "" || claudeEnv["ANTHROPIC_AUTH_TOKEN"] != claudeEnv["ANTHROPIC_API_KEY"] || claudeEnv["ANTHROPIC_BASE_URL"] == "" || claudeEnv["ANTHROPIC_MODEL"] != "claude-model" || claudeEnv["CLAUDE_MODEL"] != "claude-model" {
		t.Fatalf("Anthropic startup environment = %#v", claudeEnv)
	}
	// Both families are published, because the image's entrypoint decides which
	// one it reads and the daemon does not configure that image. A Claude agent
	// therefore also gets the OpenAI aliases, each backed by its own token.
	if claudeEnv["OPENAI_API_KEY"] == "" || !strings.HasSuffix(claudeEnv["OPENAI_BASE_URL"], "/llm/openai/v1") || claudeEnv["CODEX_MODEL"] != "openai-model" || claudeEnv["OPENAI_MODEL"] != "openai-model" {
		t.Fatalf("Claude startup environment is missing the OpenAI aliases: %#v", claudeEnv)
	}
	if claudeEnv["OPENAI_API_KEY"] == claudeEnv["ANTHROPIC_API_KEY"] {
		t.Fatalf("both families share one facade token: %#v", claudeEnv)
	}
	claudeOpenAIToken, err := store.GetLLMFacadeToken(ctx, claudeEnv["OPENAI_API_KEY"])
	if err != nil {
		t.Fatalf("load the Claude run's OpenAI startup token: %v", err)
	}
	if claudeOpenAIToken.ProviderID != "openai-primary" {
		t.Fatalf("Claude run's OpenAI startup token = %#v", claudeOpenAIToken)
	}
	if claudeEnv["LLM_API_KEY"] != "" || claudeEnv["AGENT_COMPOSE_SANDBOX_TOKEN"] != "" {
		t.Fatalf("startup environment contains common facade variables = %#v", claudeEnv)
	}
	for _, secret := range []string{"anthropic-upstream-secret", "openai-upstream-secret"} {
		for name, value := range claudeEnv {
			if strings.Contains(value, secret) {
				t.Fatalf("startup env[%s] leaked upstream credential", name)
			}
		}
	}
	anthropicToken, err := store.GetLLMFacadeToken(ctx, claudeEnv["ANTHROPIC_API_KEY"])
	if err != nil {
		t.Fatalf("load Anthropic startup token: %v", err)
	}
	if anthropicToken.ProviderID != "anthropic-primary" || anthropicToken.WireAPI != llms.APIProtocolMessages {
		t.Fatalf("Anthropic startup token = %#v", anthropicToken)
	}

	codexEnv, err := EnsureSessionStartupFacadeConfig(ctx, SessionFacadeConfigRequest{
		Config: config, Store: store, Session: session, Agent: "codex", Model: "openai-model", Source: TokenSourceAgent,
	})
	if err != nil {
		t.Fatalf("EnsureSessionStartupFacadeConfig codex returned error: %v", err)
	}
	if codexEnv["OPENAI_API_KEY"] == "" || codexEnv["OPENAI_BASE_URL"] == "" || codexEnv["CODEX_MODEL"] != "openai-model" || codexEnv["OPENAI_MODEL"] != "openai-model" {
		t.Fatalf("OpenAI startup environment = %#v", codexEnv)
	}
	// And the codex agent gets the Anthropic aliases for the same reason.
	if codexEnv["ANTHROPIC_API_KEY"] == "" || !strings.HasSuffix(codexEnv["ANTHROPIC_BASE_URL"], "/llm/anthropic") || codexEnv["ANTHROPIC_AUTH_TOKEN"] != codexEnv["ANTHROPIC_API_KEY"] {
		t.Fatalf("Codex startup environment is missing the Anthropic aliases: %#v", codexEnv)
	}
	// Those aliases must name an Anthropic model, not the codex run's. req.Model
	// is spelled in the family the selected agent addresses, so publishing it
	// here would hand an image reading ANTHROPIC_MODEL a name this connection
	// need not serve — the startup failure this facade prevents, moved to the
	// first request.
	if codexEnv["ANTHROPIC_MODEL"] != "claude-model" || codexEnv["CLAUDE_MODEL"] != "claude-model" {
		t.Fatalf("codex run published %q/%q as the Anthropic model, want the Anthropic provider's own model",
			codexEnv["ANTHROPIC_MODEL"], codexEnv["CLAUDE_MODEL"])
	}
	openAIToken, err := store.GetLLMFacadeToken(ctx, codexEnv["OPENAI_API_KEY"])
	if err != nil {
		t.Fatalf("load OpenAI startup token: %v", err)
	}
	if openAIToken.ProviderID != "openai-primary" || openAIToken.WireAPI != "" {
		t.Fatalf("OpenAI startup token = %#v", openAIToken)
	}
	// This facade serves images the daemon does not configure, so its tokens
	// record no model and forward whatever the image asks for.
	if openAIToken.Model != "" || openAIToken.GuestModel != "" {
		t.Fatalf("startup token records a model, want an unbound token = %#v", openAIToken)
	}

	// opencode is the case an alias rule keyed on the agent kind cannot cover: it
	// does not address exactly one family, so "is this the run's family?" has no
	// answer for it. Both families must still name their own model rather than the
	// run's, which is why the family's own sources answer first for every agent.
	opencodeEnv, err := EnsureSessionStartupFacadeConfig(ctx, SessionFacadeConfigRequest{
		Config: config, Store: store, Session: session, Agent: "opencode", Model: "openai-model", Source: TokenSourceAgent,
	})
	if err != nil {
		t.Fatalf("EnsureSessionStartupFacadeConfig opencode returned error: %v", err)
	}
	if opencodeEnv["OPENAI_MODEL"] != "openai-model" {
		t.Fatalf("opencode OpenAI alias = %q, want the OpenAI provider's own model", opencodeEnv["OPENAI_MODEL"])
	}
	if opencodeEnv["ANTHROPIC_MODEL"] != "claude-model" || opencodeEnv["CLAUDE_MODEL"] != "claude-model" {
		t.Fatalf("opencode run published %q/%q as the Anthropic model, want the Anthropic provider's own model",
			opencodeEnv["ANTHROPIC_MODEL"], opencodeEnv["CLAUDE_MODEL"])
	}
}

func TestEnsureSessionStartupFacadeConfigProjectsGlobalAnthropicCredential(t *testing.T) {
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
	if _, err := store.ReplaceGlobalEnv(ctx, []domain.SandboxEnvVar{
		{Name: "ANTHROPIC_API_KEY", Value: "global-anthropic-secret", Secret: true},
		{Name: "ANTHROPIC_BASE_URL", Value: "https://anthropic.example.test"},
		{Name: "ANTHROPIC_MODEL", Value: "claude-global"},
	}); err != nil {
		t.Fatalf("ReplaceGlobalEnv returned error: %v", err)
	}
	// This is the shape that broke a codex sandbox running an image whose
	// entrypoint reads ANTHROPIC_*: the only credential is the global Anthropic
	// one and the selected agent is codex. Every agent must get the Anthropic
	// aliases regardless of which family it addresses itself.
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-global-anthropic-" + agent, Driver: driverpkg.RuntimeDriverDocker}}
			env, err := EnsureSessionStartupFacadeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: agent, Source: TokenSourceAgent})
			if err != nil {
				t.Fatalf("EnsureSessionStartupFacadeConfig returned error: %v", err)
			}
			tokenValue := env["ANTHROPIC_API_KEY"]
			if tokenValue == "" || env["ANTHROPIC_AUTH_TOKEN"] != tokenValue || strings.Contains(tokenValue, "global-anthropic-secret") {
				t.Fatalf("global Anthropic startup environment = %#v", env)
			}
			token, err := store.GetLLMFacadeToken(ctx, tokenValue)
			if err != nil {
				t.Fatalf("GetLLMFacadeToken returned error: %v", err)
			}
			if !strings.HasPrefix(token.ProviderID, llms.DeclaredConnectionPrefix+session.Summary.ID+":"+llms.ProviderFamilyAnthropic+":") {
				t.Fatalf("startup token provider = %q, want sandbox-scoped Anthropic connection", token.ProviderID)
			}
			// The resolved model is published as this family's alias but
			// deliberately not recorded on the token: the image decides which
			// model to send, so the upstream has to see it verbatim.
			if token.Model != "" || token.GuestModel != "" {
				t.Fatalf("startup token is model-bound, want an unbound token = %#v", token)
			}
			if env["ANTHROPIC_MODEL"] != "claude-global" || env["CLAUDE_MODEL"] != "claude-global" {
				t.Fatalf("global Anthropic startup environment = %#v", env)
			}
		})
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

	// An opaque model no connection declares is still served: a model id is
	// opaque, so the daemon picks among the connections by protocol affinity
	// instead of failing. OpenCode speaks messages natively, and the responses
	// connection is not something it can speak at all, so the Anthropic
	// connection wins.
	unknown, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "opencode", Model: "unknown-model", Source: "", RunID: "run-unknown"})
	if err != nil {
		t.Fatalf("unknown model returned error: %v", err)
	}
	unknownToken, err := store.GetLLMFacadeToken(ctx, unknown.Env["AGENT_COMPOSE_SANDBOX_TOKEN"])
	if err != nil {
		t.Fatalf("GetLLMFacadeToken(unknown-model) returned error: %v", err)
	}
	if unknownToken.ProviderID != "anthropic" || unknownToken.Model != "unknown-model" {
		t.Fatalf("unknown model token = %#v, want the messages connection over the opaque model", unknownToken)
	}
	if env, err := EnsureSessionLLMFacadeConfig(ctx, SessionFacadeConfigRequest{Config: nil, Store: store, Session: session, Agent: "codex", Model: "", Source: "", RunID: ""}); err != nil || env != nil {
		t.Fatalf("nil config env=%#v err=%v", env, err)
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

// TestEnsureSessionAgentRuntimeConfigChoosesBetweenEquivalentConnections pins
// that a model two connections serve over the same protocol resolves at the
// facade boundary: the connections are interchangeable, so one is chosen and
// the run is not reported as unresolved.
func TestEnsureSessionAgentRuntimeConfigChoosesBetweenEquivalentConnections(t *testing.T) {
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
		t.Fatalf("OpenConfigStore returned error: %v", err)
	}
	// Two connections serve the same model, so model inference alone cannot
	// choose between them.
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{
		Providers: map[string]llms.CatalogProvider{
			"gateway": {
				BaseURL:  catalogStringPointer("https://gateway.example.test/v1"),
				Protocol: catalogStringPointer(llms.APIProtocolResponses),
				APIKey:   catalogStringPointer("gateway-key"),
				Models:   []llms.CatalogModel{{ID: "shared-model"}},
			},
			"backup": {
				BaseURL:  catalogStringPointer("https://backup.example.test/v1"),
				Protocol: catalogStringPointer(llms.APIProtocolResponses),
				APIKey:   catalogStringPointer("backup-key"),
				Models:   []llms.CatalogModel{{ID: "shared-model"}},
			},
		},
	}); err != nil {
		t.Fatalf("apply model catalog: %v", err)
	}
	session := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:            "sandbox-agent-connection",
			Driver:        driverpkg.RuntimeDriverDocker,
			WorkspacePath: filepath.Join(root, "sandboxes", "sandbox-agent-connection", "workspace"),
		},
	}

	// Two connections serve the same model over the same protocol. They are
	// interchangeable, so the run proceeds on one of them: the tie-break picks
	// rather than reporting the model as unresolved.
	shared, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{
		Config: config, Store: store, Session: session, Agent: "pi", Model: "shared-model", Source: TokenSourceAgent, RunID: "run-connection-bare",
	})
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig returned error: %v", err)
	}
	token, err := store.GetLLMFacadeToken(ctx, shared.Env["AGENT_COMPOSE_SANDBOX_TOKEN"])
	if err != nil {
		t.Fatalf("GetLLMFacadeToken returned error: %v", err)
	}
	if token.ProviderID != "gateway" && token.ProviderID != "backup" {
		t.Fatalf("token provider = %q, want one of the equivalent connections", token.ProviderID)
	}
}

// The startup compatibility facade publishes provider aliases before the
// managed environment, so the managed environment decides the value of every
// name the two share.
//
// For the family the selected agent addresses, every startup name must be
// covered: a name the managed environment does not define keeps the startup
// facade's value, and a dialect writer that replaces the family credential
// without its model names would leave the guest reading this run's token beside
// the startup facade's model for the same family.
//
// The *other* family is deliberately left uncovered. The startup facade
// publishes it because an image's entrypoint — not the selected agent — decides
// which family it reads, and the managed environment of an agent that addresses
// one family writes nothing for the other. It has to survive as a whole set, so
// this test also pins that the family's credential always comes with its own
// family's route. dsh is deliberately not in the loop: it publishes no family
// credential, so the startup facade's whole set survives for both families and
// stays consistent with itself.
func TestManagedEnvironmentCoversTheSelectedFamilyAndLeavesTheOtherStartupFacadeIntact(t *testing.T) {
	for _, agent := range []string{"codex", "claude", "opencode", "pi"} {
		t.Run(agent, func(t *testing.T) {
			isolateLLMEnv(t)

			ctx := context.Background()
			root := t.TempDir()
			config, store := commandFacadeTestStore(t, ctx, root)
			seedCommandFacadeProviders(t, ctx, store)
			// A daemon model the catalog does not resolve: the startup facade
			// prefers it while the managed environment resolves the catalog, so
			// a startup alias left in place would carry a different model.
			config.LLMModel = "legacy-model"
			session := &domain.Sandbox{Summary: domain.SandboxSummary{
				ID:            "sandbox-alias-coverage-" + agent,
				Driver:        driverpkg.RuntimeDriverDocker,
				WorkspacePath: filepath.Join(root, "sandboxes", "sandbox-alias-coverage-"+agent, "workspace"),
			}}
			request := SessionFacadeConfigRequest{
				Config: config, Store: store, Session: session, Agent: agent, Source: TokenSourceAgent,
			}

			startupEnv, err := EnsureSessionStartupFacadeConfig(ctx, request)
			if err != nil {
				t.Fatalf("EnsureSessionStartupFacadeConfig returned error: %v", err)
			}
			if len(startupEnv) == 0 {
				t.Fatalf("startup facade published no aliases for %s", agent)
			}
			managedEnv, err := EnsureSessionLLMFacadeConfig(ctx, request)
			if err != nil {
				t.Fatalf("EnsureSessionLLMFacadeConfig returned error: %v", err)
			}
			if len(managedEnv) == 0 {
				t.Fatalf("managed facade published no environment for %s", agent)
			}

			// The family the selected agent addresses, read off the managed
			// environment rather than assumed: opencode and pi resolve their
			// family from the catalog, so it is not a property of the agent kind.
			selected := ""
			switch {
			case managedEnv["OPENAI_API_KEY"] != "":
				selected = llms.ProviderFamilyOpenAI
			case managedEnv["ANTHROPIC_API_KEY"] != "":
				selected = llms.ProviderFamilyAnthropic
			}
			if selected == "" {
				t.Fatalf("managed environment names no provider family: %#v", managedEnv)
			}

			for name, value := range startupEnv {
				if _, covered := managedEnv[name]; covered {
					continue
				}
				if family := envNameProviderFamily(name); family == "" || family == selected {
					t.Errorf("startup alias %s is not covered by the managed environment: startup=%q managed=%#v",
						name, value, managedEnv)
				}
			}

			other := llms.ProviderFamilyAnthropic
			if selected == llms.ProviderFamilyAnthropic {
				other = llms.ProviderFamilyOpenAI
			}
			credentialKey, routeKey, routeSuffix := "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "/llm/anthropic"
			if other == llms.ProviderFamilyOpenAI {
				credentialKey, routeKey, routeSuffix = "OPENAI_API_KEY", "OPENAI_BASE_URL", "/llm/openai/v1"
			}
			if _, ok := startupEnv[credentialKey]; ok && !strings.HasSuffix(startupEnv[routeKey], routeSuffix) {
				t.Errorf("%s startup aliases are not self-consistent: %#v", other, startupEnv)
			}
		})
	}
}

// envNameProviderFamily classifies a provider alias by the family it belongs to,
// returning "" for a name no family owns.
func envNameProviderFamily(name string) string {
	switch {
	case strings.HasPrefix(name, "ANTHROPIC_"), name == "CLAUDE_MODEL":
		return llms.ProviderFamilyAnthropic
	case strings.HasPrefix(name, "OPENAI_"), name == "CODEX_MODEL":
		return llms.ProviderFamilyOpenAI
	default:
		return ""
	}
}

// An agent kind with no dialect still gets both startup families. Which family
// an image reads is a property of the image's entrypoint, not of the agent the
// daemon selected, so no agent kind can justify skipping a family — and a guard
// keyed on the agent is exactly what left ANTHROPIC_API_KEY unpublished for a
// codex sandbox running an image that reads it.
func TestEnsureSessionStartupFacadeConfigPublishesBothFamiliesForAnUnknownAgent(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config, store := commandFacadeTestStore(t, ctx, root)
	seedCommandFacadeProviders(t, ctx, store)
	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-unknown-agent", Driver: driverpkg.RuntimeDriverDocker}}
	env, err := EnsureSessionStartupFacadeConfig(ctx, SessionFacadeConfigRequest{
		Config: config, Store: store, Session: session, Agent: "unknown-agent", Model: "claude-model", Source: TokenSourceAgent,
	})
	if err != nil {
		t.Fatalf("EnsureSessionStartupFacadeConfig returned error: %v", err)
	}
	if env["ANTHROPIC_API_KEY"] == "" || env["OPENAI_API_KEY"] == "" {
		t.Fatalf("unknown agent startup environment = %#v, want both families published", env)
	}
}
