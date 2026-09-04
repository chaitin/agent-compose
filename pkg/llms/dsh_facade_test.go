package llms

import (
	"context"
	"errors"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestSplitDshModel(t *testing.T) {
	provider, model, err := SplitDshModel(" custom/model/variant ")
	if err != nil || provider != "custom" || model != "model/variant" {
		t.Fatalf("SplitDshModel provider=%q model=%q err=%v", provider, model, err)
	}
	for _, invalid := range []string{"", "model", "/model", "provider/", " / "} {
		if _, _, err := SplitDshModel(invalid); err == nil {
			t.Fatalf("SplitDshModel(%q) succeeded", invalid)
		}
	}
}

func TestEnsureDshFacadeConfigBindsConfiguredProviderToken(t *testing.T) {
	isolateLLMEnv(t)
	store := newDshFacadeTestStore()
	store.providers = []Provider{{
		ID: "deepseek-catalog", ProviderType: ProviderFamilyOpenAI,
		DefaultWireAPI: APIProtocolResponses, BaseURL: "https://deepseek.test", APIKey: "secret", Enabled: true,
	}}
	store.models = []Model{{ID: "model-id", Name: "org/deepseek-v4", Enabled: true}}
	store.wire["deepseek-catalog\x00model-id"] = APIProtocolResponses

	env, err := EnsureDshFacadeConfig(context.Background(), DshFacadeConfigRequest{
		Config:  &appconfig.Config{RuntimeBaseURL: "http://runtime.test/base/"},
		Store:   store,
		Sandbox: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1"}},
		Model:   "deepseek-catalog/org/deepseek-v4", Source: "agent", RunID: "run-1",
	})
	if err != nil {
		t.Fatalf("EnsureDshFacadeConfig returned error: %v", err)
	}
	// The wire protocol follows the resolved provider (Responses here) rather
	// than being fixed, so the guest speaks what the provider serves and the
	// proxy never has to convert. DSH_WIRE_API carries the same choice in the
	// spelling the profile's llm-pi-ai route expects.
	if env["DSH_MODEL"] != "org/deepseek-v4" || env["LLM_API_PROTOCOL"] != APIProtocolResponses {
		t.Fatalf("DSH environment = %#v", env)
	}
	if env["DSH_WIRE_API"] != "openai-responses" {
		t.Fatalf("DSH_WIRE_API = %q", env["DSH_WIRE_API"])
	}
	if env["LLM_API_ENDPOINT"] != "http://runtime.test/base/api/runtime/sandboxes/sandbox-1/llm/openai/v1" {
		t.Fatalf("LLM_API_ENDPOINT = %q", env["LLM_API_ENDPOINT"])
	}
	if env["AGENT_COMPOSE_SANDBOX_TOKEN"] == "" || env["LLM_API_KEY"] != env["AGENT_COMPOSE_SANDBOX_TOKEN"] {
		t.Fatalf("facade token environment = %#v", env)
	}
	if len(store.savedTokens) != 1 {
		t.Fatalf("saved token count = %d", len(store.savedTokens))
	}
	token := store.savedTokens[0]
	if token.SandboxID != "sandbox-1" || token.Model != "org/deepseek-v4" || token.ProviderID != "deepseek-catalog" ||
		token.WireAPI != APIProtocolResponses || token.Source != "agent" || token.RunID != "run-1" {
		t.Fatalf("saved token = %#v", token)
	}
}

func TestEnsureDshFacadeConfigUsesOpenAIFamilyAndPropagatesSaveFailure(t *testing.T) {
	isolateLLMEnv(t)
	store := newDshFacadeTestStore()
	store.providers = []Provider{{
		ID: ProviderIDDefaultOpenAI, ProviderType: ProviderFamilyOpenAI,
		DefaultWireAPI: APIProtocolChatCompletions, BaseURL: "https://openai.test", APIKey: "secret", Enabled: true,
	}}
	store.models = []Model{{ID: "deepseek-v4", Name: "deepseek-v4", Enabled: true}}
	store.wire[ProviderIDDefaultOpenAI+"\x00deepseek-v4"] = APIProtocolChatCompletions
	store.saveErr = errors.New("save token failed")

	_, err := EnsureDshFacadeConfig(context.Background(), DshFacadeConfigRequest{
		Config:  &appconfig.Config{RuntimeBaseURL: "http://runtime.test"},
		Store:   store,
		Sandbox: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1"}},
		Model:   "openai/deepseek-v4", Source: "scheduler", RunID: "run-2",
	})
	if !errors.Is(err, store.saveErr) {
		t.Fatalf("EnsureDshFacadeConfig error = %v", err)
	}
}

func TestEnsureDshFacadeConfigUsesSessionEnvProvider(t *testing.T) {
	isolateLLMEnv(t)
	store := newDshFacadeTestStore()
	sandbox := &domain.Sandbox{
		Summary: domain.SandboxSummary{ID: "sandbox-env"},
		ProviderEnvItems: []domain.SandboxEnvVar{
			{Name: "LLM_API_ENDPOINT", Value: "https://session.test/v1"},
			{Name: "LLM_API_KEY", Value: "session-key", Secret: true},
			{Name: "LLM_API_PROTOCOL", Value: APIProtocolResponses},
		},
	}

	env, err := EnsureDshFacadeConfig(context.Background(), DshFacadeConfigRequest{
		Config:  &appconfig.Config{RuntimeBaseURL: "http://runtime.test"},
		Store:   store,
		Sandbox: sandbox,
		Model:   "ignored-provider/org/deepseek-v4", Source: "agent", RunID: "run-env",
	})
	if err != nil {
		t.Fatalf("EnsureDshFacadeConfig returned error: %v", err)
	}
	wantProviderID := SessionEnvProviderID("sandbox-env", ProviderFamilyOpenAI)
	if len(store.savedTokens) != 1 || store.savedTokens[0].ProviderID != wantProviderID {
		t.Fatalf("saved tokens = %#v, want session provider %q", store.savedTokens, wantProviderID)
	}
	if store.savedTokens[0].Model != "org/deepseek-v4" || store.savedTokens[0].WireAPI != APIProtocolResponses {
		t.Fatalf("saved token = %#v", store.savedTokens[0])
	}
	if env["DSH_MODEL"] != "org/deepseek-v4" {
		t.Fatalf("DSH_MODEL = %q", env["DSH_MODEL"])
	}
}

func TestEnsureDshFacadeConfigRejectsUnknownCustomProvider(t *testing.T) {
	isolateLLMEnv(t)
	_, err := EnsureDshFacadeConfig(context.Background(), DshFacadeConfigRequest{
		Config:  &appconfig.Config{RuntimeBaseURL: "http://runtime.test"},
		Store:   newDshFacadeTestStore(),
		Sandbox: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1"}},
		Model:   "unconfigured/deepseek-v4", Source: "agent", RunID: "run-1",
	})
	if err == nil || !strings.Contains(err.Error(), "is not configured") {
		t.Fatalf("EnsureDshFacadeConfig error = %v", err)
	}
}

type dshFacadeTestStore struct {
	*resolverCoverageStore
	savedTokens []FacadeToken
	saveErr     error
}

func newDshFacadeTestStore() *dshFacadeTestStore {
	return &dshFacadeTestStore{resolverCoverageStore: newResolverCoverageStore()}
}

func (s *dshFacadeTestStore) SaveLLMFacadeToken(_ context.Context, token FacadeToken) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.savedTokens = append(s.savedTokens, token)
	return nil
}

// TestEnsureDshFacadeConfigFollowsChatCompletionsProvider is the other half of
// the contract the two tests above cover for Responses: dsh no longer pins a
// wire protocol, so a provider serving chat completions gets a guest speaking
// chat completions. Matching the provider is what keeps the request off the
// proxy's conversion path, where an unmodelled upstream event would otherwise
// reach the guest as assistant text.
func TestEnsureDshFacadeConfigFollowsChatCompletionsProvider(t *testing.T) {
	store := newDshFacadeTestStore()
	store.providers = []Provider{{
		ID: "compat-gateway", ProviderType: ProviderFamilyOpenAI,
		DefaultWireAPI: APIProtocolChatCompletions, BaseURL: "https://compat.test", APIKey: "secret", Enabled: true,
	}}
	store.models = []Model{{ID: "model-id", Name: "org/compat-model", Enabled: true}}
	store.wire["compat-gateway\x00model-id"] = APIProtocolChatCompletions

	env, err := EnsureDshFacadeConfig(context.Background(), DshFacadeConfigRequest{
		Config:  &appconfig.Config{RuntimeBaseURL: "http://runtime.test/base/"},
		Store:   store,
		Sandbox: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-chat"}},
		Model:   "compat-gateway/org/compat-model", Source: "agent", RunID: "run-chat",
	})
	if err != nil {
		t.Fatalf("EnsureDshFacadeConfig returned error: %v", err)
	}
	if env["LLM_API_PROTOCOL"] != APIProtocolChatCompletions {
		t.Fatalf("LLM_API_PROTOCOL = %q", env["LLM_API_PROTOCOL"])
	}
	if env["DSH_WIRE_API"] != "openai-completions" {
		t.Fatalf("DSH_WIRE_API = %q", env["DSH_WIRE_API"])
	}
	if len(store.savedTokens) != 1 || store.savedTokens[0].WireAPI != APIProtocolChatCompletions {
		t.Fatalf("saved token = %#v", store.savedTokens)
	}
}
