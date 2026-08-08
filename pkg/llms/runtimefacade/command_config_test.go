package runtimefacade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/internal/testutil"
	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/configstore"
)

func TestEnsureSessionCommandFacadeConfigRebuildsStartupAndSelectedEnvironment(t *testing.T) {
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

	result, err := EnsureSessionCommandFacadeConfig(ctx, config, store, session, "codex", "openai-model", TokenSourceSchedulerCommand, "run-command")
	if err != nil {
		t.Fatalf("EnsureSessionCommandFacadeConfig returned error: %v", err)
	}
	if result.Env["ANTHROPIC_API_KEY"] == "" || result.Env["ANTHROPIC_AUTH_TOKEN"] != result.Env["ANTHROPIC_API_KEY"] || result.Env["ANTHROPIC_BASE_URL"] == "" {
		t.Fatalf("command Anthropic startup environment = %#v", result.Env)
	}
	if result.Env["AGENT_COMPOSE_SANDBOX_TOKEN"] == "" || result.Env["OPENAI_API_KEY"] != result.Env["AGENT_COMPOSE_SANDBOX_TOKEN"] {
		t.Fatalf("selected Codex environment did not override startup OpenAI values = %#v", result.Env)
	}
	if result.Env["ANTHROPIC_API_KEY"] == result.Env["AGENT_COMPOSE_SANDBOX_TOKEN"] {
		t.Fatalf("Anthropic and selected Codex tokens unexpectedly match")
	}
	if len(result.TokenHashes) != 3 {
		t.Fatalf("command token hashes = %#v, want startup Anthropic, startup OpenAI, and selected Codex", result.TokenHashes)
	}
	anthropicHash, _ := llms.HashFacadeToken(result.Env["ANTHROPIC_API_KEY"])
	selectedHash, _ := llms.HashFacadeToken(result.Env["AGENT_COMPOSE_SANDBOX_TOKEN"])
	if result.TokenHashes[0] != anthropicHash || result.TokenHashes[len(result.TokenHashes)-1] != selectedHash {
		t.Fatalf("command token hash ordering = %#v", result.TokenHashes)
	}
	if got := countCommandFacadeTokens(t, ctx, store, "run-command"); got != 3 {
		t.Fatalf("persisted command facade tokens = %d, want 3", got)
	}
}

func TestEnsureSessionCommandFacadeConfigCleansAllTokensAfterPartialFailure(t *testing.T) {
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

	result, err := EnsureSessionCommandFacadeConfig(ctx, config, store, session, "codex", "openai-model", TokenSourceSchedulerCommand, "run-partial-failure")
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
