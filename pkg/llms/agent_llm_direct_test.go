package llms

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func directEnvItems(pairs ...string) []domain.SandboxEnvVar {
	items := make([]domain.SandboxEnvVar, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		items = append(items, domain.SandboxEnvVar{Name: pairs[i], Value: pairs[i+1], Secret: true})
	}
	return items
}

// directConfig is a daemon with no sandbox-reachable URL and no catalog: direct
// runs must not need either.
func directConfig(root string) *appconfig.Config {
	return &appconfig.Config{DataRoot: root, GuestHomePath: "/root"}
}

// TestPrepareAgentLLMDirectModeUsesTheAgentOwnUpstream pins the direct half of
// the direct/managed rule. An agent that publishes its own LLM connection is
// configured against it, with its own credential, and the daemon neither mints a
// token nor consults the catalog — an empty catalog and a daemon with no
// reachable URL are both irrelevant here.
func TestPrepareAgentLLMDirectModeUsesTheAgentOwnUpstream(t *testing.T) {
	tests := []struct {
		name            string
		agent           string
		env             []domain.SandboxEnvVar
		model           string
		wantEndpoint    string
		wantProtocol    Protocol
		wantModel       string
		wantGuestModel  string
		wantCredential  string
		wantCredentialK string
	}{
		{
			name:            "codex with a declared key and no endpoint",
			agent:           "codex",
			env:             directEnvItems("OPENAI_API_KEY", "sk-openai"),
			model:           "gpt-5.5",
			wantEndpoint:    directOpenAIEndpoint,
			wantProtocol:    ProtocolResponses,
			wantModel:       "gpt-5.5",
			wantGuestModel:  "gpt-5.5",
			wantCredential:  "sk-openai",
			wantCredentialK: "OPENAI_API_KEY",
		},
		{
			name:            "claude with a declared anthropic upstream",
			agent:           "claude",
			env:             directEnvItems("ANTHROPIC_BASE_URL", "https://anthropic.internal", "ANTHROPIC_API_KEY", "sk-ant", "ANTHROPIC_MODEL", "claude-sonnet-4"),
			wantEndpoint:    "https://anthropic.internal",
			wantProtocol:    ProtocolMessages,
			wantModel:       "claude-sonnet-4",
			wantGuestModel:  "claude-sonnet-4",
			wantCredential:  "sk-ant",
			wantCredentialK: "ANTHROPIC_API_KEY",
		},
		{
			name:            "opencode with a generic key falls back to its canonical protocol",
			agent:           "opencode",
			env:             directEnvItems("LLM_API_KEY", "sk-generic", "LLM_API_ENDPOINT", "https://gateway.internal/v1"),
			model:           "baizhi/deepseek-v4-flash",
			wantEndpoint:    "https://gateway.internal/v1",
			wantProtocol:    ProtocolChatCompletions,
			wantModel:       "baizhi/deepseek-v4-flash",
			wantGuestModel:  "agent-compose/baizhi/deepseek-v4-flash",
			wantCredential:  "sk-generic",
			wantCredentialK: "LLM_API_KEY",
		},
		{
			name:            "pi keeps the declared responses protocol",
			agent:           "pi",
			env:             directEnvItems("OPENAI_API_KEY", "sk-pi", "LLM_API_PROTOCOL", "responses"),
			model:           "gpt-5.5",
			wantEndpoint:    directOpenAIEndpoint,
			wantProtocol:    ProtocolResponses,
			wantModel:       "gpt-5.5",
			wantGuestModel:  "agent-compose/gpt-5.5",
			wantCredential:  "sk-pi",
			wantCredentialK: "OPENAI_API_KEY",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateLLMEnv(t)
			root := t.TempDir()
			// An empty catalog and no daemon URL: a managed run could not
			// succeed here at all.
			store := &prepareAgentLLMStore{}

			prepared, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
				Config: directConfig(root), Store: store,
				Sandbox:   bareModelSandbox(root, agentLLMSandboxID),
				AgentKind: tt.agent, Model: tt.model, AgentEnv: tt.env,
			})
			if err != nil {
				t.Fatalf("PrepareAgentLLM returned error: %v", err)
			}
			if !prepared.Direct {
				t.Fatal("prepared.Direct = false, want true")
			}
			if prepared.Endpoint != tt.wantEndpoint {
				t.Errorf("Endpoint = %q, want %q", prepared.Endpoint, tt.wantEndpoint)
			}
			if prepared.Inbound != tt.wantProtocol || prepared.Upstream != tt.wantProtocol {
				t.Errorf("protocols = %s/%s, want %s", prepared.Inbound, prepared.Upstream, tt.wantProtocol)
			}
			if prepared.Model != tt.wantModel || prepared.GuestModel != tt.wantGuestModel {
				t.Errorf("models = %q/%q, want %q/%q", prepared.Model, prepared.GuestModel, tt.wantModel, tt.wantGuestModel)
			}
			if prepared.Credential != tt.wantCredential {
				t.Errorf("Credential = %q, want the declared key", prepared.Credential)
			}
			if len(store.savedTokens) != 0 {
				t.Fatalf("saved tokens = %#v, want none in direct mode", store.savedTokens)
			}
			// The declared key reaches the guest: that is the documented
			// meaning of publishing a connection in the agent's environment.
			if got := prepared.Env[tt.wantCredentialK]; got != tt.wantCredential {
				t.Errorf("env[%s] = %q, want the declared key", tt.wantCredentialK, got)
			}
			if _, ok := prepared.Env["AGENT_COMPOSE_SANDBOX_TOKEN"]; ok {
				t.Error("direct run carries a facade token, but no facade exists")
			}
		})
	}
}

// TestPrepareAgentLLMDirectModelComesFromTheAgentEnvironment pins that a
// declaration may also name the model, so an agent does not have to repeat it.
func TestPrepareAgentLLMDirectModelComesFromTheAgentEnvironment(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := &prepareAgentLLMStore{}

	prepared, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: directConfig(root), Store: store,
		Sandbox:   bareModelSandbox(root, agentLLMSandboxID),
		AgentKind: "claude", AgentEnv: directEnvItems("ANTHROPIC_API_KEY", "sk-ant", "ANTHROPIC_MODEL", "claude-opus-4"),
	})
	if err != nil {
		t.Fatalf("PrepareAgentLLM returned error: %v", err)
	}
	if prepared.Model != "claude-opus-4" {
		t.Fatalf("Model = %q, want the model named by the agent environment", prepared.Model)
	}
}

// TestPrepareAgentLLMDirectRejectsProtocolTheAgentCannotSpeak pins that nothing
// converts on the direct path, so a declaration the agent cannot speak is a
// configuration error rather than a silent rewrite.
func TestPrepareAgentLLMDirectRejectsProtocolTheAgentCannotSpeak(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := &prepareAgentLLMStore{}

	_, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: directConfig(root), Store: store,
		Sandbox:   bareModelSandbox(root, agentLLMSandboxID),
		AgentKind: "codex", Model: "gpt-5.5",
		AgentEnv: directEnvItems("LLM_API_KEY", "sk", "LLM_API_PROTOCOL", "chat_completions"),
	})
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("PrepareAgentLLM error = %v, want failed precondition", err)
	}
	if len(store.savedTokens) != 0 {
		t.Fatalf("saved tokens = %#v, want none", store.savedTokens)
	}
}

// TestPrepareAgentLLMDirectNeedsAModel pins that a declaration without a model
// is still ErrNoModel: the daemon has nothing to point the agent at.
func TestPrepareAgentLLMDirectNeedsAModel(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := &prepareAgentLLMStore{}

	_, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: directConfig(root), Store: store,
		Sandbox:   bareModelSandbox(root, agentLLMSandboxID),
		AgentKind: "codex",
		AgentEnv:  directEnvItems("OPENAI_API_KEY", "sk-openai"),
	})
	if !errors.Is(err, ErrNoModel) {
		t.Fatalf("PrepareAgentLLM error = %v, want ErrNoModel", err)
	}
}

// TestPrepareAgentLLMWithoutAgentEnvironmentUsesTheCatalog is the other half of
// the rule, stated against the same fixture: no declaration means the catalog
// owns the upstream.
func TestPrepareAgentLLMWithoutAgentEnvironmentUsesTheCatalog(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := &prepareAgentLLMStore{fakeCatalogStore: fakeCatalogStore{
		providers: []Provider{catalogOpenAIConnection("gateway", "https://gateway.test")},
	}}

	prepared, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: bareModelConfig(root), Store: store,
		Sandbox:   bareModelSandbox(root, agentLLMSandboxID),
		AgentKind: "codex", Model: "gpt-5.5",
	})
	if err != nil {
		t.Fatalf("PrepareAgentLLM returned error: %v", err)
	}
	if prepared.Direct {
		t.Fatal("prepared.Direct = true, want a managed run")
	}
	if prepared.Endpoint != agentLLMEndpoint(ProtocolResponses) {
		t.Errorf("Endpoint = %q, want the facade route", prepared.Endpoint)
	}
	if len(store.savedTokens) != 1 {
		t.Fatalf("saved tokens = %#v, want one facade token", store.savedTokens)
	}
}

// TestPrepareAgentLLMDirectGuestConfigReadsTheDeclaredKey pins that a direct
// run's generated CLI configuration names a credential variable the run actually
// exports. The config writers were written for the managed path, where the
// facade token is always present as AGENT_COMPOSE_SANDBOX_TOKEN; a direct run
// exports the agent's own key under the vendor's name instead, so a config that
// still referenced the facade token would read an unset variable and fail to
// authenticate against the declared upstream.
func TestPrepareAgentLLMDirectGuestConfigReadsTheDeclaredKey(t *testing.T) {
	tests := []struct {
		name       string
		agent      string
		env        []domain.SandboxEnvVar
		model      string
		configPath string
		wantRef    string
		wantEnvKey string
	}{
		{
			name: "codex reads OPENAI_API_KEY", agent: "codex",
			env: directEnvItems("OPENAI_API_KEY", "sk-openai"), model: "gpt-5.5",
			configPath: filepath.Join(".codex", "config.toml"),
			wantRef:    `env_key = "OPENAI_API_KEY"`, wantEnvKey: "OPENAI_API_KEY",
		},
		{
			name: "pi reads OPENAI_API_KEY off the openai protocol", agent: "pi",
			env: directEnvItems("OPENAI_API_KEY", "sk-pi"), model: "gpt-5.5",
			configPath: filepath.Join(".pi", "agent", "models.json"),
			wantRef:    `"$OPENAI_API_KEY"`, wantEnvKey: "OPENAI_API_KEY",
		},
		{
			name: "pi reads ANTHROPIC_API_KEY off the messages protocol", agent: "pi",
			env: directEnvItems("ANTHROPIC_API_KEY", "sk-ant"), model: "claude-sonnet-4",
			configPath: filepath.Join(".pi", "agent", "models.json"),
			wantRef:    `"$ANTHROPIC_API_KEY"`, wantEnvKey: "ANTHROPIC_API_KEY",
		},
		{
			name: "opencode reads OPENAI_API_KEY off the chat protocol", agent: "opencode",
			env: directEnvItems("OPENAI_API_KEY", "sk-openai"), model: "gpt-5.5",
			configPath: filepath.Join(".config", "opencode", "opencode.json"),
			wantRef:    `"{env:OPENAI_API_KEY}"`, wantEnvKey: "OPENAI_API_KEY",
		},
		{
			name: "opencode reads ANTHROPIC_API_KEY off the messages protocol", agent: "opencode",
			env: directEnvItems("ANTHROPIC_API_KEY", "sk-ant"), model: "claude-sonnet-4",
			configPath: filepath.Join(".config", "opencode", "opencode.json"),
			wantRef:    `"{env:ANTHROPIC_API_KEY}"`, wantEnvKey: "ANTHROPIC_API_KEY",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateLLMEnv(t)
			root := t.TempDir()
			store := &prepareAgentLLMStore{}

			prepared, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
				Config: directConfig(root), Store: store,
				Sandbox:   bareModelSandbox(root, agentLLMSandboxID),
				AgentKind: tt.agent, Model: tt.model, AgentEnv: tt.env,
			})
			if err != nil {
				t.Fatalf("PrepareAgentLLM returned error: %v", err)
			}
			if got := prepared.Env[tt.wantEnvKey]; got != prepared.Credential {
				t.Fatalf("env[%s] = %q, want the declared credential %q", tt.wantEnvKey, got, prepared.Credential)
			}
			data, err := os.ReadFile(filepath.Join(root, "sandboxes", agentLLMSandboxID, "home", tt.configPath))
			if err != nil {
				t.Fatalf("read guest config: %v", err)
			}
			if !strings.Contains(string(data), tt.wantRef) {
				t.Errorf("guest config %s = %q, want it to reference %s", tt.configPath, data, tt.wantRef)
			}
			if strings.Contains(string(data), guestFacadeTokenEnvName) {
				t.Errorf("guest config %s references %s, but a direct run has no facade token", tt.configPath, guestFacadeTokenEnvName)
			}
		})
	}
}
