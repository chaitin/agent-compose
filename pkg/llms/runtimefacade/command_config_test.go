package runtimefacade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samber/do/v2"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
)

func TestEnsureSessionCommandFacadeConfigConfiguresSelectedAgent(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config, store := commandFacadeTestStore(t, ctx, root)
	seedCommandFacadeProviders(t, ctx, store)
	session := &domain.Sandbox{Summary: domain.SandboxSummary{
		ID:            "sandbox-command-facades",
		Driver:        driverpkg.RuntimeDriverDocker,
		WorkspacePath: filepath.Join(root, "sandboxes", "sandbox-command-facades", "workspace"),
	}}

	result, err := EnsureSessionCommandFacadeConfig(ctx, CommandFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "codex", Model: "openai-model", Source: TokenSourceSchedulerCommand, RunID: "run-command"})
	if err != nil {
		t.Fatalf("EnsureSessionCommandFacadeConfig returned error: %v", err)
	}
	// PrepareAgentLLM decides one dialect for the named agent, so the retired
	// startup facade for the other family must not appear.
	if result.Env["ANTHROPIC_API_KEY"] != "" || result.Env["ANTHROPIC_BASE_URL"] != "" {
		t.Fatalf("command environment contains a startup Anthropic facade = %#v", result.Env)
	}
	if result.Env["AGENT_COMPOSE_SANDBOX_TOKEN"] == "" || result.Env["OPENAI_API_KEY"] != result.Env["AGENT_COMPOSE_SANDBOX_TOKEN"] {
		t.Fatalf("selected Codex environment = %#v", result.Env)
	}
	if result.Env["LLM_API_PROTOCOL"] != llms.APIProtocolResponses {
		t.Fatalf("LLM_API_PROTOCOL = %q, want responses", result.Env["LLM_API_PROTOCOL"])
	}
	if len(result.TokenHashes) != 1 {
		t.Fatalf("command token hashes = %#v, want exactly the selected Codex token", result.TokenHashes)
	}
	selectedHash, _ := llms.HashFacadeToken(result.Env["AGENT_COMPOSE_SANDBOX_TOKEN"])
	if result.TokenHashes[0] != selectedHash {
		t.Fatalf("command token hash = %#v, want the selected token hash", result.TokenHashes)
	}
	if got := countCommandFacadeTokens(t, ctx, store, "run-command"); got != 1 {
		t.Fatalf("persisted command facade tokens = %d, want 1", got)
	}
}

func TestEnsureSessionCommandFacadeConfigHonoursADeclaredUpstream(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config, store := commandFacadeTestStore(t, ctx, root)
	seedCommandFacadeProviders(t, ctx, store)
	session := &domain.Sandbox{Summary: domain.SandboxSummary{
		ID:            "sandbox-command-declared-upstream",
		Driver:        driverpkg.RuntimeDriverDocker,
		WorkspacePath: filepath.Join(root, "sandboxes", "sandbox-command-declared-upstream", "workspace"),
	}}

	result, err := EnsureSessionCommandFacadeConfig(ctx, CommandFacadeConfigRequest{
		Config: config, Store: store, Session: session, Agent: "codex", Model: "",
		AgentEnv: []domain.SandboxEnvVar{
			{Name: "LLM_API_ENDPOINT", Value: "https://declared.upstream.test/v1"},
			{Name: "LLM_API_KEY", Value: "declared-upstream-key"},
			{Name: "LLM_MODEL", Value: "declared-model"},
		},
		Source: TokenSourceSchedulerCommand, RunID: "run-declared-upstream",
	})
	if err != nil {
		t.Fatalf("EnsureSessionCommandFacadeConfig returned error: %v", err)
	}
	// The declared upstream owns the command: the CLI is pointed straight at it
	// with its own key and no facade token exists to present to a facade.
	if token := result.Env["AGENT_COMPOSE_SANDBOX_TOKEN"]; token != "" {
		t.Fatalf("command facade token for a declared upstream = %q", token)
	}
	if result.Env["OPENAI_BASE_URL"] != "https://declared.upstream.test/v1" || result.Env["OPENAI_API_KEY"] != "declared-upstream-key" {
		t.Fatalf("declared command environment = %#v", result.Env)
	}
	if result.Env["CODEX_MODEL"] != "declared-model" || result.Env["LLM_API_PROTOCOL"] != llms.APIProtocolResponses {
		t.Fatalf("declared command model/protocol = %#v", result.Env)
	}
	if len(result.TokenHashes) != 0 {
		t.Fatalf("command token hashes = %#v, want none", result.TokenHashes)
	}
	if got := countCommandFacadeTokens(t, ctx, store, "run-declared-upstream"); got != 0 {
		t.Fatalf("persisted command facade tokens for a declared upstream = %d, want 0", got)
	}
}

func TestEnsureSessionCommandFacadeConfigCleansTokenAfterPartialFailure(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config, store := commandFacadeTestStore(t, ctx, root)
	seedCommandFacadeProviders(t, ctx, store)
	blockedSandboxDir := filepath.Join(root, "blocked-sandbox")
	if err := os.WriteFile(blockedSandboxDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocked sandbox path: %v", err)
	}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{
		ID:            "sandbox-command-partial-failure",
		Driver:        driverpkg.RuntimeDriverDocker,
		WorkspacePath: filepath.Join(blockedSandboxDir, "workspace"),
	}}

	result, err := EnsureSessionCommandFacadeConfig(ctx, CommandFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "codex", Model: "openai-model", Source: TokenSourceSchedulerCommand, RunID: "run-partial-failure"})
	if err == nil || !strings.Contains(err.Error(), "codex config") {
		t.Fatalf("EnsureSessionCommandFacadeConfig error = %v, want Codex config write failure", err)
	}
	if len(result.Env) != 0 || len(result.TokenHashes) != 0 {
		t.Fatalf("partial result = %#v, want empty", result)
	}
	if got := countCommandFacadeTokens(t, ctx, store, "run-partial-failure"); got != 0 {
		t.Fatalf("persisted command facade tokens after partial failure = %d, want 0", got)
	}
}

func commandFacadeTestStore(t *testing.T, ctx context.Context, root string) (*appconfig.Config, *configstore.ConfigStore) {
	t.Helper()
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
	return config, store
}

func seedCommandFacadeProviders(t *testing.T, ctx context.Context, store *configstore.ConfigStore) {
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

func countCommandFacadeTokens(t *testing.T, ctx context.Context, store *configstore.ConfigStore, runID string) int {
	t.Helper()
	var count int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(1) FROM llm_facade_token WHERE source = ? AND run_id = ?`, TokenSourceSchedulerCommand, runID).Scan(&count); err != nil {
		t.Fatalf("count command facade tokens: %v", err)
	}
	return count
}
