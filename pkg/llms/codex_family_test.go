package llms

import (
	"context"
	"errors"
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// anthropicOnlyConnection is the state that used to break Codex: the daemon has
// exactly one configured connection and it is not OpenAI-family.
func anthropicOnlyConnection() Provider {
	return Provider{
		ID: "anthropic-only", ProviderType: ProviderFamilyAnthropic,
		DefaultWireAPI: APIProtocolMessages,
		BaseURL:        "https://anthropic.test", APIKey: "anthropic-key",
		Enabled: true, Scope: ProviderScopeAPI,
	}
}

// Codex speaks only the OpenAI family. With no OpenAI connection, a bare model
// must stay a no-op so Codex can use its own login; resolving it against the
// cross-family default would fail the run at startup.
func TestEnsureCodexFacadeConfigKeepsOwnLoginWithoutOpenAIConnection(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := newBareModelFacadeStore()
	store.providers = []Provider{anthropicOnlyConnection()}

	env, err := EnsureCodexFacadeConfig(context.Background(), CodexFacadeConfigRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, "sandbox-codex-bare"),
		Model: "gpt-5", Source: "agent", RunID: "run-codex-bare",
	})
	if err != nil {
		t.Fatalf("bare Codex model with only a non-OpenAI connection returned error: %v", err)
	}
	if env != nil {
		t.Fatalf("env = %v, want no managed environment so Codex keeps its own login", env)
	}
	if len(store.savedTokens) != 0 {
		t.Fatalf("saved tokens = %#v, want none for a no-op", store.savedTokens)
	}
}

// An operator who names a non-OpenAI connection for Codex still gets the
// actionable error instead of a silent fallback.
func TestEnsureCodexFacadeConfigRejectsExplicitNonOpenAIConnection(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := newBareModelFacadeStore()
	store.providers = []Provider{anthropicOnlyConnection()}

	_, err := EnsureCodexFacadeConfig(context.Background(), CodexFacadeConfigRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, "sandbox-codex-explicit"),
		Model: "anthropic-only/claude-sonnet", Source: "agent", RunID: "run-codex-explicit",
	})
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("explicit non-OpenAI Codex model err = %v, want failed precondition", err)
	}
	if len(store.savedTokens) != 0 {
		t.Fatalf("saved tokens = %#v, want none for a rejected connection", store.savedTokens)
	}
}

// A `<connection>/<model>` declaration names a daemon connection, so the prefix
// is never part of the upstream model id. With a resolvable default connection
// but no connection named by the prefix, Codex used to degrade the whole string
// to a literal model on that default connection, which sent
// "matrix-chat/deepseek-flash" to the upstream as the model name. The missing
// connection is a configuration error and must be reported, not resolved.
func TestEnsureCodexFacadeConfigRejectsUnknownConnectionPrefix(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := newBareModelFacadeStore()
	store.providers = []Provider{gatewayConnection()}

	_, err := EnsureCodexFacadeConfig(context.Background(), CodexFacadeConfigRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, "sandbox-codex-unknown-connection"),
		Model: "matrix-chat/deepseek-flash", Source: "agent", RunID: "run-codex-unknown-connection",
	})
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("unknown connection prefix err = %v, want failed precondition", err)
	}
	if !strings.Contains(err.Error(), `llm provider "matrix-chat" is not configured`) {
		t.Fatalf("err = %v, want the unknown connection named in the error", err)
	}
	if len(store.savedTokens) != 0 {
		t.Fatalf("saved tokens = %#v, want none for a rejected connection", store.savedTokens)
	}
}

// A gateway issues a `<provider>/<model>` logical name through the session
// environment. Its prefix names no daemon connection, but resolution takes the
// model from the session environment instead of forwarding the qualified
// string, so the unknown-connection guard must leave it alone. This is the only
// branch that keeps such a gateway-issued name from regressing to a startup
// failure, and the rejection test above does not cover it.
func TestEnsureCodexFacadeConfigAllowsSessionEnvProviderReference(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := newBareModelFacadeStore()
	sandbox := bareModelSandbox(root, "sandbox-codex-env-reference")
	SetSandboxProviderEnvItems(sandbox, []domain.SandboxEnvVar{
		{Name: "LLM_API_KEY", Value: "session-key", Secret: true},
		{Name: "LLM_API_ENDPOINT", Value: "https://session.test/v1"},
		{Name: "LLM_MODEL", Value: "baizhi/deepseek-flash"},
	})

	env, err := EnsureCodexFacadeConfig(context.Background(), CodexFacadeConfigRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: sandbox,
		Model: "baizhi/deepseek-flash", Source: "agent", RunID: "run-codex-env-reference",
	})
	if err != nil {
		t.Fatalf("session-env provider reference returned error: %v", err)
	}
	if env == nil || env["LLM_API_KEY"] == "" {
		t.Fatalf("env = %#v, want a managed facade environment", env)
	}
	if len(store.savedTokens) != 1 || store.savedTokens[0].ProviderID != "session-env:sandbox-codex-env-reference:openai" {
		t.Fatalf("saved tokens = %#v, want the session-env provider", store.savedTokens)
	}
	if model := store.savedTokens[0].Model; model != "baizhi/deepseek-flash" {
		t.Fatalf("token model = %q, want the gateway logical model without a forwarded connection id", model)
	}
}
