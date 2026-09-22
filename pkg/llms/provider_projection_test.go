package llms

import (
	"context"
	"errors"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

type defaultLLMUpsert struct {
	Provider Provider
	Model    Model
}

type projectionStore struct {
	upserts []defaultLLMUpsert
	err     error
}

func (s *projectionStore) UpsertDefaultLLMConfig(_ context.Context, provider Provider, model Model) error {
	if s.err != nil {
		return s.err
	}
	s.upserts = append(s.upserts, defaultLLMUpsert{Provider: provider, Model: model})
	return nil
}

// TestProjectDaemonLLMConfigRegistersDaemonEnvironment pins that a daemon
// configured only through its environment still reaches the catalog. Without
// this projection the catalog is empty, every agent resolves to no model, and a
// daemon that used to serve its own gateway silently stops doing so.
func TestProjectDaemonLLMConfigRegistersDaemonEnvironment(t *testing.T) {
	tests := []struct {
		name           string
		env            map[string]string
		config         *appconfig.Config
		wantProviderID string
		wantFamily     string
		wantWireAPI    string
		wantBaseURL    string
		wantAuthHeader string
		wantAuthScheme string
		wantModel      string
	}{
		{
			name:           "openai endpoint and key",
			env:            map[string]string{"LLM_API_ENDPOINT": "https://gateway.test/v1", "LLM_API_KEY": "sk-gateway", "LLM_MODEL": "gpt-5.5"},
			wantProviderID: ProviderIDDefaultOpenAI,
			wantFamily:     ProviderFamilyOpenAI,
			wantWireAPI:    APIProtocolResponses,
			wantBaseURL:    "https://gateway.test/v1",
			wantAuthHeader: "Authorization",
			wantAuthScheme: "Bearer",
			wantModel:      "gpt-5.5",
		},
		{
			name:           "openai chat completions protocol",
			env:            map[string]string{"LLM_API_ENDPOINT": "https://gateway.test/v1", "LLM_API_KEY": "sk-gateway", "LLM_MODEL": "gpt-5.5", "LLM_API_PROTOCOL": "chat_completions"},
			wantProviderID: ProviderIDDefaultOpenAI,
			wantFamily:     ProviderFamilyOpenAI,
			wantWireAPI:    APIProtocolChatCompletions,
			wantBaseURL:    "https://gateway.test/v1",
			wantAuthHeader: "Authorization",
			wantAuthScheme: "Bearer",
			wantModel:      "gpt-5.5",
		},
		{
			name:           "anthropic api key",
			env:            map[string]string{"ANTHROPIC_BASE_URL": "https://anthropic.test", "ANTHROPIC_API_KEY": "sk-ant", "ANTHROPIC_MODEL": "claude-sonnet-4"},
			wantProviderID: ProviderIDDefaultAnthropic,
			wantFamily:     ProviderFamilyAnthropic,
			wantWireAPI:    APIProtocolMessages,
			wantBaseURL:    "https://anthropic.test",
			wantAuthHeader: "x-api-key",
			wantModel:      "claude-sonnet-4",
		},
		{
			name:           "anthropic auth token is a bearer credential",
			env:            map[string]string{"ANTHROPIC_BASE_URL": "https://anthropic.test", "ANTHROPIC_AUTH_TOKEN": "oat-token", "ANTHROPIC_MODEL": "claude-sonnet-4"},
			wantProviderID: ProviderIDDefaultAnthropic,
			wantFamily:     ProviderFamilyAnthropic,
			wantWireAPI:    APIProtocolMessages,
			wantBaseURL:    "https://anthropic.test",
			wantAuthHeader: "Authorization",
			wantAuthScheme: "Bearer",
			wantModel:      "claude-sonnet-4",
		},
		{
			name:           "generic env declaring the messages protocol",
			env:            map[string]string{"LLM_API_ENDPOINT": "https://gateway.test", "LLM_API_KEY": "sk-generic", "LLM_MODEL": "claude-sonnet-4", "LLM_API_PROTOCOL": "anthropic_messages"},
			wantProviderID: ProviderIDDefaultAnthropic,
			wantFamily:     ProviderFamilyAnthropic,
			wantWireAPI:    APIProtocolMessages,
			wantBaseURL:    "https://gateway.test",
			wantAuthHeader: "x-api-key",
			wantModel:      "claude-sonnet-4",
		},
		{
			name: "daemon configuration rather than process environment",
			config: &appconfig.Config{
				LLMAPIEndpoint: "https://config.test/v1",
				LLMAPIKey:      "sk-config",
				LLMModel:       "gpt-5.5",
			},
			wantProviderID: ProviderIDDefaultOpenAI,
			wantFamily:     ProviderFamilyOpenAI,
			wantWireAPI:    APIProtocolResponses,
			wantBaseURL:    "https://config.test/v1",
			wantAuthHeader: "Authorization",
			wantAuthScheme: "Bearer",
			wantModel:      "gpt-5.5",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateLLMEnv(t)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			store := &projectionStore{}
			if err := ProjectDaemonLLMConfig(context.Background(), tt.config, store); err != nil {
				t.Fatalf("ProjectDaemonLLMConfig: %v", err)
			}
			if len(store.upserts) != 1 {
				t.Fatalf("recorded %d connections, want 1: %+v", len(store.upserts), store.upserts)
			}
			got := store.upserts[0]
			if got.Provider.ID != tt.wantProviderID {
				t.Errorf("provider id = %q, want %q", got.Provider.ID, tt.wantProviderID)
			}
			if got.Provider.ProviderType != tt.wantFamily {
				t.Errorf("provider type = %q, want %q", got.Provider.ProviderType, tt.wantFamily)
			}
			if got.Provider.DefaultWireAPI != tt.wantWireAPI {
				t.Errorf("wire api = %q, want %q", got.Provider.DefaultWireAPI, tt.wantWireAPI)
			}
			if got.Provider.BaseURL != tt.wantBaseURL {
				t.Errorf("base url = %q, want %q", got.Provider.BaseURL, tt.wantBaseURL)
			}
			if got.Provider.AuthHeader != tt.wantAuthHeader {
				t.Errorf("auth header = %q, want %q", got.Provider.AuthHeader, tt.wantAuthHeader)
			}
			if got.Provider.AuthScheme != tt.wantAuthScheme {
				t.Errorf("auth scheme = %q, want %q", got.Provider.AuthScheme, tt.wantAuthScheme)
			}
			if got.Provider.Scope != ProviderScopeEnvDefault {
				t.Errorf("scope = %q, want %q", got.Provider.Scope, ProviderScopeEnvDefault)
			}
			if !got.Provider.Enabled {
				t.Error("projected connection is disabled")
			}
			if got.Model.ID != tt.wantModel {
				t.Errorf("model id = %q, want %q", got.Model.ID, tt.wantModel)
			}
			if !got.Model.DefaultModel {
				t.Error("projected model is not the catalog default")
			}
		})
	}
}

// TestProjectDaemonLLMConfigIsNoOpWithoutDeclaration pins that an operator who
// configured no daemon-wide connection does not get an empty one: the agent must
// keep its own authentication instead of being pointed at api.openai.com.
func TestProjectDaemonLLMConfigIsNoOpWithoutDeclaration(t *testing.T) {
	isolateLLMEnv(t)
	store := &projectionStore{}
	if err := ProjectDaemonLLMConfig(context.Background(), nil, store); err != nil {
		t.Fatalf("ProjectDaemonLLMConfig: %v", err)
	}
	if len(store.upserts) != 0 {
		t.Fatalf("recorded %d connections, want 0: %+v", len(store.upserts), store.upserts)
	}
}

// TestProjectDaemonLLMConfigRequiresCredential pins that an endpoint and a model
// without a key do not become a connection. Otherwise the daemon would route an
// agent to a gateway it cannot authenticate to, instead of letting the agent use
// the credential it carries itself.
func TestProjectDaemonLLMConfigRequiresCredential(t *testing.T) {
	isolateLLMEnv(t)
	t.Setenv("LLM_API_ENDPOINT", "https://gateway.test/v1")
	t.Setenv("LLM_MODEL", "gpt-5.5")
	store := &projectionStore{}
	if err := ProjectDaemonLLMConfig(context.Background(), nil, store); err != nil {
		t.Fatalf("ProjectDaemonLLMConfig: %v", err)
	}
	if len(store.upserts) != 0 {
		t.Fatalf("recorded %d connections, want 0 without a credential: %+v", len(store.upserts), store.upserts)
	}
}

func TestProjectDaemonLLMConfigPropagatesStoreFailure(t *testing.T) {
	isolateLLMEnv(t)
	t.Setenv("LLM_API_ENDPOINT", "https://gateway.test/v1")
	t.Setenv("LLM_API_KEY", "sk-gateway")
	t.Setenv("LLM_MODEL", "gpt-5.5")
	wantErr := errors.New("store unavailable")
	store := &projectionStore{err: wantErr}
	if err := ProjectDaemonLLMConfig(context.Background(), nil, store); !errors.Is(err, wantErr) {
		t.Fatalf("ProjectDaemonLLMConfig error = %v, want %v", err, wantErr)
	}
}

func TestProjectDaemonLLMConfigIgnoresMissingStore(t *testing.T) {
	isolateLLMEnv(t)
	if err := ProjectDaemonLLMConfig(context.Background(), nil, nil); err != nil {
		t.Fatalf("ProjectDaemonLLMConfig: %v", err)
	}
}
