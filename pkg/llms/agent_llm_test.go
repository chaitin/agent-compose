package llms

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// prepareAgentLLMStore is the AgentLLMStore surface PrepareAgentLLM consumes: a
// catalog snapshot plus the facade token it mints. Tokens are recorded so a test
// can assert the resolved connection, model, and inbound protocol.
type prepareAgentLLMStore struct {
	fakeCatalogStore
	savedTokens []FacadeToken
	saveErr     error
}

func (s *prepareAgentLLMStore) SaveLLMFacadeToken(_ context.Context, token FacadeToken) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.savedTokens = append(s.savedTokens, token)
	return nil
}

// bareModelSandbox is a sandbox with no agent-declared environment, so
// PrepareAgentLLM resolves through the catalog.
func bareModelSandbox(root, id string) *domain.Sandbox {
	return &domain.Sandbox{Summary: domain.SandboxSummary{
		ID:            id,
		Driver:        driverpkg.RuntimeDriverDocker,
		WorkspacePath: filepath.Join(root, "sandboxes", id, "workspace"),
	}}
}

func bareModelConfig(root string) *appconfig.Config {
	return &appconfig.Config{
		DataRoot:       root,
		RuntimeBaseURL: "http://agent-compose.test:7410",
		GuestHomePath:  "/root",
	}
}

// agentLLMProvider builds a connection with an explicit upstream protocol.
func agentLLMProvider(id, family, wireAPI, baseURL string) Provider {
	return Provider{
		ID: id, Name: id, ProviderType: family,
		DefaultWireAPI: wireAPI, BaseURL: baseURL, APIKey: "key-" + id,
		AuthHeader: "Authorization", AuthScheme: "Bearer", Enabled: true,
	}
}

const (
	agentLLMBaseURL   = "http://agent-compose.test:7410"
	agentLLMSandboxID = "sandbox-agent-llm"
	// facadeTokenPlaceholder stands for the run-scoped token in an expected env
	// map, which cannot be known before PrepareAgentLLM mints it.
	facadeTokenPlaceholder = "$FACADE_TOKEN"
)

func agentLLMEndpoint(inbound Protocol) string {
	if inbound == ProtocolMessages {
		return agentLLMBaseURL + "/api/runtime/sandboxes/" + agentLLMSandboxID + "/llm/anthropic"
	}
	return agentLLMBaseURL + "/api/runtime/sandboxes/" + agentLLMSandboxID + "/llm/openai/v1"
}

// agentLLMEnv builds an expected env map with the token placeholder filled in.
func agentLLMEnv(prepared *AgentLLM, entries map[string]string) map[string]string {
	env := make(map[string]string, len(entries))
	for key, value := range entries {
		if value == facadeTokenPlaceholder {
			value = prepared.Token
		}
		env[key] = value
	}
	return env
}

func assertStringMapEqual(t *testing.T, got, want map[string]string) {
	t.Helper()
	for key, value := range want {
		if got[key] != value {
			t.Errorf("env[%s] = %q, want %q", key, got[key], value)
		}
	}
	if len(got) != len(want) {
		t.Errorf("env has %d entries, want %d: %v", len(got), len(want), got)
	}
}

func readAgentLLMJSON(t *testing.T, path string, dest any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, dest); err != nil {
		t.Fatalf("decode %s: %v\n%s", path, err, data)
	}
}

// TestPrepareAgentLLMDialectGuestConfig pins the complete guest-facing result of
// one prepare call per agent: the model the guest addresses, the protocol the
// daemon serves, whether conversion is implied, the minted token, the
// environment the agent process inherits, and the guest config file written.
func TestPrepareAgentLLMDialectGuestConfig(t *testing.T) {
	isolateLLMEnv(t)
	responses := catalogOpenAIConnection("gateway-responses", "https://gateway.test")
	anthropic := catalogAnthropicConnection("anthropic-primary", "https://anthropic.test")
	openAIEndpoint := agentLLMEndpoint(ProtocolResponses)
	anthropicEndpoint := agentLLMEndpoint(ProtocolMessages)
	codexPath := filepath.Join("sandboxes", agentLLMSandboxID, "home", ".codex", "config.toml")
	piPath := filepath.Join("sandboxes", agentLLMSandboxID, "home", ".pi", "agent", "models.json")
	openCodePath := filepath.Join("sandboxes", agentLLMSandboxID, "home", ".config", "opencode", "opencode.json")

	cases := []struct {
		name           string
		agent          string
		connection     Provider
		model          string
		wantGuestModel string
		wantInbound    Protocol
		wantConvert    bool
		wantProviderID string
		wantEnv        map[string]string
		checkFile      func(t *testing.T, root string)
	}{
		{
			name: "codex passes the model through and speaks responses", agent: "codex",
			connection: responses, model: "gpt-5.5",
			wantGuestModel: "gpt-5.5", wantInbound: ProtocolResponses, wantConvert: false,
			wantProviderID: "gateway-responses",
			wantEnv: map[string]string{
				"AGENT_COMPOSE_SANDBOX_TOKEN": facadeTokenPlaceholder,
				"LLM_API_KEY":                 facadeTokenPlaceholder,
				"LLM_API_ENDPOINT":            openAIEndpoint,
				"LLM_API_PROTOCOL":            string(ProtocolResponses),
				"LLM_MODEL":                   "gpt-5.5",
				"CODEX_MODEL":                 "gpt-5.5",
				GuestModelEnvName:             "gpt-5.5",
				"OPENAI_API_KEY":              facadeTokenPlaceholder,
				"OPENAI_BASE_URL":             openAIEndpoint,
			},
			checkFile: func(t *testing.T, root string) {
				t.Helper()
				data, err := os.ReadFile(filepath.Join(root, codexPath))
				if err != nil {
					t.Fatalf("read codex config: %v", err)
				}
				for _, want := range []string{`model = "gpt-5.5"`, `wire_api = "responses"`, `base_url = "` + openAIEndpoint + `"`} {
					if !strings.Contains(string(data), want) {
						t.Errorf("codex config %q does not contain %q", data, want)
					}
				}
			},
		},
		{
			name: "claude keeps the bare model and serves messages", agent: "claude",
			connection: anthropic, model: "claude-sonnet-4",
			wantGuestModel: "claude-sonnet-4", wantInbound: ProtocolMessages, wantConvert: false,
			wantProviderID: "anthropic-primary",
			wantEnv: map[string]string{
				"AGENT_COMPOSE_SANDBOX_TOKEN": facadeTokenPlaceholder,
				"LLM_API_KEY":                 facadeTokenPlaceholder,
				"LLM_API_ENDPOINT":            anthropicEndpoint,
				"LLM_API_PROTOCOL":            string(ProtocolMessages),
				"ANTHROPIC_API_KEY":           facadeTokenPlaceholder,
				"ANTHROPIC_AUTH_TOKEN":        facadeTokenPlaceholder,
				"ANTHROPIC_BASE_URL":          anthropicEndpoint,
				"ANTHROPIC_MODEL":             "claude-sonnet-4",
				"CLAUDE_MODEL":                "claude-sonnet-4",
				GuestModelEnvName:             "claude-sonnet-4",
			},
		},
		{
			name: "opencode namespaces the model and converts responses to chat", agent: "opencode",
			connection: responses, model: "gpt-5.5",
			wantGuestModel: "agent-compose/gpt-5.5", wantInbound: ProtocolChatCompletions, wantConvert: true,
			wantProviderID: "gateway-responses",
			wantEnv: map[string]string{
				"AGENT_COMPOSE_SANDBOX_TOKEN": facadeTokenPlaceholder,
				"LLM_API_KEY":                 facadeTokenPlaceholder,
				"LLM_API_ENDPOINT":            openAIEndpoint,
				"LLM_API_PROTOCOL":            string(ProtocolResponses),
				"OPENCODE_CONFIG":             "/root/.config/opencode/opencode.json",
				"LLM_MODEL":                   "agent-compose/gpt-5.5",
				"OPENCODE_MODEL":              "agent-compose/gpt-5.5",
				GuestModelEnvName:             "agent-compose/gpt-5.5",
				"OPENAI_API_KEY":              facadeTokenPlaceholder,
				"OPENAI_BASE_URL":             openAIEndpoint,
			},
			checkFile: func(t *testing.T, root string) {
				t.Helper()
				var config struct {
					Provider map[string]struct {
						NPM     string `json:"npm"`
						Options struct {
							BaseURL string `json:"baseURL"`
						} `json:"options"`
						Models map[string]json.RawMessage `json:"models"`
					} `json:"provider"`
				}
				readAgentLLMJSON(t, filepath.Join(root, openCodePath), &config)
				entry, ok := config.Provider[GuestProviderAgentCompose]
				if !ok {
					t.Fatalf("opencode config has no %q provider", GuestProviderAgentCompose)
				}
				if entry.NPM != "@ai-sdk/openai-compatible" {
					t.Errorf("opencode provider npm = %q, want the chat-completions package", entry.NPM)
				}
				if entry.Options.BaseURL != openAIEndpoint {
					t.Errorf("opencode provider baseURL = %q, want %q", entry.Options.BaseURL, openAIEndpoint)
				}
				if entry.Models["gpt-5.5"] == nil {
					t.Errorf("opencode config does not register model gpt-5.5")
				}
			},
		},
		{
			name: "pi namespaces the model and passes responses through", agent: "pi",
			connection: responses, model: "gpt-5.5",
			wantGuestModel: "agent-compose/gpt-5.5", wantInbound: ProtocolResponses, wantConvert: false,
			wantProviderID: "gateway-responses",
			wantEnv: map[string]string{
				"AGENT_COMPOSE_SANDBOX_TOKEN": facadeTokenPlaceholder,
				"LLM_API_KEY":                 facadeTokenPlaceholder,
				"LLM_API_ENDPOINT":            openAIEndpoint,
				"LLM_API_PROTOCOL":            string(ProtocolResponses),
				"PI_CODING_AGENT_DIR":         "/root/.pi/agent",
				GuestModelEnvName:             "agent-compose/gpt-5.5",
				"OPENAI_API_KEY":              facadeTokenPlaceholder,
			},
			checkFile: func(t *testing.T, root string) {
				t.Helper()
				var config struct {
					Providers map[string]struct {
						BaseURL string `json:"baseUrl"`
						API     string `json:"api"`
						Models  []struct {
							ID string `json:"id"`
						} `json:"models"`
					} `json:"providers"`
				}
				readAgentLLMJSON(t, filepath.Join(root, piPath), &config)
				entry, ok := config.Providers[GuestProviderAgentCompose]
				if !ok {
					t.Fatalf("pi config has no %q provider", GuestProviderAgentCompose)
				}
				if entry.API != "openai-responses" || entry.BaseURL != openAIEndpoint {
					t.Errorf("pi provider = %#v, want openai-responses at %q", entry, openAIEndpoint)
				}
				if len(entry.Models) != 1 || entry.Models[0].ID != "gpt-5.5" {
					t.Errorf("pi models = %#v, want gpt-5.5", entry.Models)
				}
			},
		},
		{
			name: "dsh passes the bare model through and speaks responses", agent: "dsh",
			connection: responses, model: "deepseek-chat",
			wantGuestModel: "deepseek-chat", wantInbound: ProtocolResponses, wantConvert: false,
			wantProviderID: "gateway-responses",
			wantEnv: map[string]string{
				"AGENT_COMPOSE_SANDBOX_TOKEN": facadeTokenPlaceholder,
				"LLM_API_KEY":                 facadeTokenPlaceholder,
				"LLM_API_ENDPOINT":            openAIEndpoint,
				"LLM_API_PROTOCOL":            string(ProtocolResponses),
				"DSH_WIRE_API":                "openai-responses",
				"DSH_MODEL":                   "deepseek-chat",
				"DSH_PERMISSION_MODE":         "danger-full-access",
				GuestModelEnvName:             "deepseek-chat",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			sandbox := bareModelSandbox(root, agentLLMSandboxID)
			store := &prepareAgentLLMStore{fakeCatalogStore: fakeCatalogStore{providers: []Provider{tc.connection}}}

			prepared, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
				Config: bareModelConfig(root), Store: store, Sandbox: sandbox,
				AgentKind: tc.agent, Model: tc.model, Source: "agent", RunID: "run-" + tc.name,
			})
			if err != nil {
				t.Fatalf("PrepareAgentLLM returned error: %v", err)
			}
			if prepared.GuestModel != tc.wantGuestModel {
				t.Errorf("GuestModel = %q, want %q", prepared.GuestModel, tc.wantGuestModel)
			}
			if prepared.Inbound != tc.wantInbound {
				t.Errorf("Inbound = %q, want %q", prepared.Inbound, tc.wantInbound)
			}
			if prepared.Convert != tc.wantConvert {
				t.Errorf("Convert = %v, want %v", prepared.Convert, tc.wantConvert)
			}
			if prepared.Upstream != NormalizeProtocol(tc.connection.DefaultWireAPI) {
				t.Errorf("Upstream = %q, want %q", prepared.Upstream, NormalizeProtocol(tc.connection.DefaultWireAPI))
			}
			if prepared.Model != tc.model || prepared.Target.Provider.ID != tc.wantProviderID {
				t.Errorf("model/provider = %q/%q, want %q/%q", prepared.Model, prepared.Target.Provider.ID, tc.model, tc.wantProviderID)
			}
			if prepared.BaseURL != agentLLMBaseURL {
				t.Errorf("BaseURL = %q, want %q", prepared.BaseURL, agentLLMBaseURL)
			}
			assertStringMapEqual(t, prepared.Env, agentLLMEnv(prepared, tc.wantEnv))
			if len(store.savedTokens) != 1 {
				t.Fatalf("saved tokens = %#v, want exactly one", store.savedTokens)
			}
			token := store.savedTokens[0]
			if token.ProviderID != tc.wantProviderID || token.Model != tc.model ||
				token.WireAPI != string(tc.wantInbound) || token.SandboxID != agentLLMSandboxID ||
				token.Source != "agent" || token.RunID != "run-"+tc.name {
				t.Errorf("saved token = %#v, want provider %q model %q wire api %q", token, tc.wantProviderID, tc.model, tc.wantInbound)
			}
			if prepared.Token == "" || token.TokenFingerprint == "" {
				t.Error("prepare did not mint a usable token")
			}
			if tc.checkFile != nil {
				tc.checkFile(t, root)
			}
		})
	}
}

// TestPrepareAgentLLMKeepsLiteralModelWithSlashes covers a model id that is
// itself namespaced: the daemon composes the guest reference, and a slash in the
// literal must survive intact rather than being shortened to its remainder.
func TestPrepareAgentLLMKeepsLiteralModelWithSlashes(t *testing.T) {
	isolateLLMEnv(t)
	const literal = "meta-llama/Llama-3.1-8B"
	responses := catalogOpenAIConnection("gateway-responses", "https://gateway.test")

	cases := []struct {
		agent          string
		wantGuestModel string
		wantFileModel  string
	}{
		{agent: "pi", wantGuestModel: "agent-compose/" + literal, wantFileModel: literal},
		{agent: "opencode", wantGuestModel: "agent-compose/" + literal, wantFileModel: literal},
		{agent: "dsh", wantGuestModel: literal},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			root := t.TempDir()
			sandbox := bareModelSandbox(root, agentLLMSandboxID)
			store := &prepareAgentLLMStore{fakeCatalogStore: fakeCatalogStore{providers: []Provider{responses}}}

			prepared, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
				Config: bareModelConfig(root), Store: store, Sandbox: sandbox,
				AgentKind: tc.agent, Model: literal,
			})
			if err != nil {
				t.Fatalf("PrepareAgentLLM returned error: %v", err)
			}
			if prepared.Model != literal {
				t.Errorf("Model = %q, want the declared literal %q", prepared.Model, literal)
			}
			if prepared.GuestModel != tc.wantGuestModel {
				t.Errorf("GuestModel = %q, want %q", prepared.GuestModel, tc.wantGuestModel)
			}
			if got := prepared.Env[GuestModelEnvName]; got != tc.wantGuestModel {
				t.Errorf("env[%s] = %q, want %q", GuestModelEnvName, got, tc.wantGuestModel)
			}
			if len(store.savedTokens) != 1 || store.savedTokens[0].Model != literal {
				t.Fatalf("saved tokens = %#v, want model %q", store.savedTokens, literal)
			}
			switch tc.agent {
			case "pi":
				var config struct {
					Providers map[string]struct {
						Models []struct {
							ID string `json:"id"`
						} `json:"models"`
					} `json:"providers"`
				}
				readAgentLLMJSON(t, filepath.Join(root, "sandboxes", agentLLMSandboxID, "home", ".pi", "agent", "models.json"), &config)
				models := config.Providers[GuestProviderAgentCompose].Models
				if len(models) != 1 || models[0].ID != literal {
					t.Errorf("pi models = %#v, want the literal %q", models, literal)
				}
			case "opencode":
				var config struct {
					Provider map[string]struct {
						Models map[string]json.RawMessage `json:"models"`
					} `json:"provider"`
				}
				readAgentLLMJSON(t, filepath.Join(root, "sandboxes", agentLLMSandboxID, "home", ".config", "opencode", "opencode.json"), &config)
				if config.Provider[GuestProviderAgentCompose].Models[literal] == nil {
					t.Errorf("opencode config does not register the literal model %q", literal)
				}
			}
		})
	}
}

// TestPrepareAgentLLMGuestConfigFailurePersistsNoToken pins the ordering between
// writing the guest configuration and storing the facade token. The token is
// run-scoped, and callers only learn its value through the returned env; if the
// store already held it when the config write failed, nothing would ever delete
// it and dead credentials would accumulate in the table.
func TestPrepareAgentLLMGuestConfigFailurePersistsNoToken(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	sandbox := bareModelSandbox(root, agentLLMSandboxID)
	// A regular file where the sandbox home belongs makes every config writer's
	// MkdirAll fail, which is the failure this ordering exists to tolerate.
	home := filepath.Join(root, "sandboxes", agentLLMSandboxID, "home")
	if err := os.MkdirAll(filepath.Dir(home), 0o755); err != nil {
		t.Fatalf("create sandbox dir: %v", err)
	}
	if err := os.WriteFile(home, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write sandbox home file: %v", err)
	}
	store := &prepareAgentLLMStore{fakeCatalogStore: fakeCatalogStore{
		providers: []Provider{catalogOpenAIConnection("gateway", "https://gateway.test")},
	}}

	_, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: sandbox,
		AgentKind: "codex", Model: "gpt-5.5",
	})
	if err == nil {
		t.Fatal("PrepareAgentLLM succeeded although the guest config could not be written")
	}
	if len(store.savedTokens) != 0 {
		t.Fatalf("saved tokens = %#v, want none after the guest config failed", store.savedTokens)
	}
}
