package llms

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// TestPrepareAgentLLMRejectsClaudeChatUpstream pins the one conversion the
// bridge registry does not provide. Claude needs the messages->chat bridge to be
// served a chat-completions upstream, so the call must fail while preparing the
// run instead of failing on every request.
func TestPrepareAgentLLMRejectsClaudeChatUpstream(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := &prepareAgentLLMStore{fakeCatalogStore: fakeCatalogStore{providers: []Provider{
		agentLLMProvider("gateway-chat", ProviderFamilyOpenAI, APIProtocolChatCompletions, "https://gateway.test/v1"),
	}}}

	_, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, agentLLMSandboxID),
		AgentKind: "claude", Model: "claude-sonnet-4",
	})
	if err == nil {
		t.Fatal("PrepareAgentLLM served a chat-completions upstream to claude")
	}
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("error = %v, want a failed-precondition classification", err)
	}
	if !strings.Contains(err.Error(), "chat_completions") || !strings.Contains(err.Error(), "claude") {
		t.Fatalf("error = %v, want it to name the agent and the unsupported upstream", err)
	}
	if len(store.savedTokens) != 0 {
		t.Fatalf("saved tokens = %#v, want none for an unservable upstream", store.savedTokens)
	}
}

// TestPrepareAgentLLMCodexConvertsMessagesUpstream is the codex half of the
// conversion contract: a messages-only upstream is bridged to the Responses API
// codex speaks, and the token still pins responses because that is what the
// guest posts.
func TestPrepareAgentLLMCodexConvertsMessagesUpstream(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	sandbox := bareModelSandbox(root, agentLLMSandboxID)
	store := &prepareAgentLLMStore{fakeCatalogStore: fakeCatalogStore{
		providers: []Provider{catalogAnthropicConnection("anthropic-primary", "https://anthropic.test")},
	}}
	endpoint := agentLLMEndpoint(ProtocolResponses)

	prepared, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: sandbox,
		AgentKind: "codex", Model: "claude-sonnet-4", Source: "agent", RunID: "run-codex-messages",
	})
	if err != nil {
		t.Fatalf("PrepareAgentLLM returned error: %v", err)
	}
	if prepared.Upstream != ProtocolMessages || prepared.Inbound != ProtocolResponses || !prepared.Convert {
		t.Fatalf("upstream/inbound/convert = %q/%q/%v, want messages/responses/true", prepared.Upstream, prepared.Inbound, prepared.Convert)
	}
	if prepared.Env["LLM_API_PROTOCOL"] != string(ProtocolMessages) || prepared.Env["OPENAI_BASE_URL"] != endpoint {
		t.Fatalf("codex env = %#v", prepared.Env)
	}
	if len(store.savedTokens) != 1 || store.savedTokens[0].WireAPI != string(ProtocolResponses) ||
		store.savedTokens[0].ProviderID != "anthropic-primary" {
		t.Fatalf("saved token = %#v, want responses to the anthropic connection", store.savedTokens)
	}
	data, err := os.ReadFile(filepath.Join(root, "sandboxes", agentLLMSandboxID, "home", ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	if !strings.Contains(string(data), `wire_api = "responses"`) {
		t.Fatalf("codex config = %q, want the responses wire api", data)
	}
}

// TestPrepareAgentLLMOpencodeInboundProtocol pins opencode's ingress rule: it
// can speak messages natively, so an anthropic upstream is passed through; it
// cannot speak responses, so that upstream is converted to chat completions and
// the guest is given the ai-sdk package that posts chat completions.
func TestPrepareAgentLLMOpencodeInboundProtocol(t *testing.T) {
	isolateLLMEnv(t)
	responses := catalogOpenAIConnection("gateway-responses", "https://gateway.test")
	anthropic := catalogAnthropicConnection("anthropic-primary", "https://anthropic.test")

	cases := []struct {
		name        string
		connection  Provider
		model       string
		wantInbound Protocol
		wantConvert bool
		wantNPM     string
		wantBaseURL string
	}{
		{
			name: "responses upstream is converted to chat", connection: responses, model: "gpt-5.5",
			wantInbound: ProtocolChatCompletions, wantConvert: true,
			wantNPM: "@ai-sdk/openai-compatible", wantBaseURL: agentLLMEndpoint(ProtocolChatCompletions),
		},
		{
			name: "messages upstream is served natively", connection: anthropic, model: "claude-sonnet-4",
			wantInbound: ProtocolMessages, wantConvert: false,
			wantNPM: "@ai-sdk/anthropic", wantBaseURL: agentLLMEndpoint(ProtocolMessages) + "/v1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store := &prepareAgentLLMStore{fakeCatalogStore: fakeCatalogStore{providers: []Provider{tc.connection}}}
			prepared, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
				Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, agentLLMSandboxID),
				AgentKind: "opencode", Model: tc.model,
			})
			if err != nil {
				t.Fatalf("PrepareAgentLLM returned error: %v", err)
			}
			if prepared.Inbound != tc.wantInbound || prepared.Convert != tc.wantConvert {
				t.Fatalf("inbound/convert = %q/%v, want %q/%v", prepared.Inbound, prepared.Convert, tc.wantInbound, tc.wantConvert)
			}
			if len(store.savedTokens) != 1 || store.savedTokens[0].WireAPI != string(tc.wantInbound) {
				t.Fatalf("saved token = %#v, want wire api %q", store.savedTokens, tc.wantInbound)
			}
			var config struct {
				Provider map[string]struct {
					NPM     string `json:"npm"`
					Options struct {
						BaseURL string `json:"baseURL"`
					} `json:"options"`
					Models map[string]json.RawMessage `json:"models"`
				} `json:"provider"`
			}
			readAgentLLMJSON(t, filepath.Join(root, "sandboxes", agentLLMSandboxID, "home", ".config", "opencode", "opencode.json"), &config)
			entry := config.Provider[GuestProviderAgentCompose]
			if entry.NPM != tc.wantNPM {
				t.Errorf("opencode provider npm = %q, want %q", entry.NPM, tc.wantNPM)
			}
			if entry.Options.BaseURL != tc.wantBaseURL {
				t.Errorf("opencode provider baseURL = %q, want %q", entry.Options.BaseURL, tc.wantBaseURL)
			}
			if entry.Models[tc.model] == nil {
				t.Errorf("opencode config does not register model %q", tc.model)
			}
			if tc.wantInbound == ProtocolMessages {
				if prepared.Env["ANTHROPIC_BASE_URL"] != agentLLMEndpoint(ProtocolMessages) || prepared.Env["OPENAI_BASE_URL"] != "" {
					t.Errorf("opencode anthropic env = %#v", prepared.Env)
				}
			} else if prepared.Env["OPENAI_BASE_URL"] != agentLLMEndpoint(ProtocolChatCompletions) || prepared.Env["ANTHROPIC_BASE_URL"] != "" {
				t.Errorf("opencode openai env = %#v", prepared.Env)
			}
		})
	}
}

// TestPrepareAgentLLMPiProtocolSpelling pins the pi-ai adapter's protocol names
// and the facade route each inbound protocol is served from. Pi can speak all
// three protocols, so nothing is converted and the upstream protocol selects
// both the adapter name and the route.
func TestPrepareAgentLLMPiProtocolSpelling(t *testing.T) {
	isolateLLMEnv(t)
	cases := []struct {
		name        string
		connection  Provider
		model       string
		wantInbound Protocol
		wantAPI     string
	}{
		{
			name: "responses", connection: catalogOpenAIConnection("gateway-responses", "https://gateway.test"),
			model: "gpt-5.5", wantInbound: ProtocolResponses, wantAPI: "openai-responses",
		},
		{
			name: "chat completions", connection: agentLLMProvider("gateway-chat", ProviderFamilyOpenAI, APIProtocolChatCompletions, "https://gateway.test/v1"),
			model: "gpt-5.5", wantInbound: ProtocolChatCompletions, wantAPI: "openai-completions",
		},
		{
			name: "messages", connection: catalogAnthropicConnection("anthropic-primary", "https://anthropic.test"),
			model: "claude-sonnet-4", wantInbound: ProtocolMessages, wantAPI: "anthropic-messages",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store := &prepareAgentLLMStore{fakeCatalogStore: fakeCatalogStore{providers: []Provider{tc.connection}}}
			prepared, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
				Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, agentLLMSandboxID),
				AgentKind: "pi", Model: tc.model,
			})
			if err != nil {
				t.Fatalf("PrepareAgentLLM returned error: %v", err)
			}
			endpoint := agentLLMEndpoint(tc.wantInbound)
			if prepared.Inbound != tc.wantInbound || prepared.Convert {
				t.Fatalf("inbound/convert = %q/%v, want %q/false", prepared.Inbound, prepared.Convert, tc.wantInbound)
			}
			if prepared.Env["LLM_API_ENDPOINT"] != endpoint {
				t.Errorf("LLM_API_ENDPOINT = %q, want %q", prepared.Env["LLM_API_ENDPOINT"], endpoint)
			}
			if tc.wantInbound == ProtocolMessages {
				if prepared.Env["ANTHROPIC_API_KEY"] != prepared.Token || prepared.Env["OPENAI_API_KEY"] != "" {
					t.Errorf("pi anthropic env = %#v", prepared.Env)
				}
			} else if prepared.Env["OPENAI_API_KEY"] != prepared.Token || prepared.Env["ANTHROPIC_API_KEY"] != "" {
				t.Errorf("pi openai env = %#v", prepared.Env)
			}
			var config struct {
				Providers map[string]struct {
					BaseURL string `json:"baseUrl"`
					API     string `json:"api"`
				} `json:"providers"`
			}
			readAgentLLMJSON(t, filepath.Join(root, "sandboxes", agentLLMSandboxID, "home", ".pi", "agent", "models.json"), &config)
			entry := config.Providers[GuestProviderAgentCompose]
			if entry.API != tc.wantAPI || entry.BaseURL != endpoint {
				t.Errorf("pi provider = %#v, want api %q at %q", entry, tc.wantAPI, endpoint)
			}
		})
	}
}
