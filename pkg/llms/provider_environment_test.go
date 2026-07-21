package llms

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
)

func TestProviderEnvironmentUsesSourceMajorPrecedence(t *testing.T) {
	isolateLLMEnv(t)
	t.Setenv("LLM_API_ENDPOINT", "https://daemon.example/v1")
	t.Setenv("LLM_API_PROTOCOL", APIProtocolResponses)
	t.Setenv("LLM_API_KEY", "daemon-canonical-key")
	t.Setenv("OPENAI_API_KEY", "daemon-alias-key")
	t.Setenv("LLM_MODEL", "daemon-model")

	store := newResolverCoverageStore()
	store.global = []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://global.example/v1"},
		{Name: "LLM_API_KEY", Value: "global-canonical-key"},
		{Name: "OPENAI_API_KEY", Value: "global-alias-key"},
		{Name: "LLM_MODEL", Value: "global-model"},
	}
	sources, sandboxItems := mustLayeredProviderEnvSources(t, &appconfig.Config{}, store, []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: ""},
		{Name: "LLM_API_PROTOCOL", Value: APIProtocolChatCompletions},
		{Name: "OPENAI_API_KEY", Value: "sandbox-alias-key"},
	})
	environment := sources.openAIEnvironment()
	if environment.endpoint != "https://global.example/v1" {
		t.Fatalf("endpoint = %q, want Global Env value", environment.endpoint)
	}
	if environment.protocol != APIProtocolChatCompletions {
		t.Fatalf("protocol = %q, want sandbox value", environment.protocol)
	}
	if environment.apiKey != "sandbox-alias-key" {
		t.Fatalf("api key = %q, want higher-layer alias", environment.apiKey)
	}
	if environment.model != "global-model" {
		t.Fatalf("model = %q, want Global Env value", environment.model)
	}
	if EnvItemValue(sandboxItems, "LLM_API_ENDPOINT") != "" {
		t.Fatalf("empty sandbox endpoint did not remain an empty fallback: %#v", sandboxItems)
	}

	sameCanonicalSources, _ := mustLayeredProviderEnvSources(t, &appconfig.Config{}, store, []domain.SandboxEnvVar{
		{Name: "LLM_API_KEY", Value: "global-canonical-key"},
		{Name: "OPENAI_API_KEY", Value: "sandbox-alias-key"},
	})
	if got := sameCanonicalSources.openAIEnvironment().apiKey; got != "global-canonical-key" {
		t.Fatalf("same-value sandbox canonical key = %q, want canonical to beat same-layer alias", got)
	}
	defaultEnvironment := mustDefaultProviderEnvSources(t, &appconfig.Config{}, store).openAIEnvironment()
	if defaultEnvironment.apiKey != "global-canonical-key" {
		t.Fatalf("default api key = %q, want canonical name within Global Env", defaultEnvironment.apiKey)
	}

	store.global = []domain.SandboxEnvVar{{Name: "OPENAI_API_KEY", Value: "global-alias-key"}}
	defaultEnvironment = mustDefaultProviderEnvSources(t, &appconfig.Config{}, store).openAIEnvironment()
	if defaultEnvironment.apiKey != "global-alias-key" {
		t.Fatalf("default api key = %q, want Global Env alias above daemon canonical", defaultEnvironment.apiKey)
	}

	store.global = []domain.SandboxEnvVar{{Name: "LLM_API_KEY", Value: ""}}
	defaultEnvironment = mustDefaultProviderEnvSources(t, &appconfig.Config{}, store).openAIEnvironment()
	if defaultEnvironment.apiKey != "daemon-canonical-key" {
		t.Fatalf("default api key = %q, want empty Global Env to fall through", defaultEnvironment.apiKey)
	}
}

func TestProviderFamilyDoesNotDependOnEndpointPath(t *testing.T) {
	isolateLLMEnv(t)
	store := newResolverCoverageStore()
	store.global = []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://gateway.example/v1/messages"},
		{Name: "LLM_API_KEY", Value: "openai-key"},
		{Name: "LLM_MODEL", Value: "openai-model"},
	}
	sources := mustDefaultProviderEnvSources(t, &appconfig.Config{}, store)
	environment := sources.openAIEnvironment()
	if environment.endpoint != "https://gateway.example/v1/messages" {
		t.Fatalf("OpenAI environment endpoint = %q", environment.endpoint)
	}
	providerID, err := ensureOpenAIProviderEnvironment(context.Background(), store, environment, ProviderIDDefaultOpenAI, "default", ProviderScopeEnvDefault, "", true)
	if err != nil {
		t.Fatalf("ensureOpenAIProviderEnvironment returned error: %v", err)
	}
	if providerID != ProviderIDDefaultOpenAI || len(store.providers) != 1 {
		t.Fatalf("OpenAI bootstrap provider %q: %#v", providerID, store.providers)
	}
	provider := store.providers[0]
	if provider.ProviderType != ProviderFamilyOpenAI || provider.BaseURL != "https://gateway.example/v1/messages" || provider.APIKey != "openai-key" {
		t.Fatalf("OpenAI provider = %#v", provider)
	}

	anthropicEnvironment := sources.anthropicEnvironment()
	if anthropicEnvironment.configured || anthropicEnvironment.endpoint != "" || anthropicEnvironment.apiKey != "" || anthropicEnvironment.model != "" {
		t.Fatalf("generic OpenAI names configured Anthropic environment: %#v", anthropicEnvironment)
	}
}

func TestSessionProviderSelectionDoesNotCrossPreferredFamily(t *testing.T) {
	if got := ChooseSessionEnvProviderID("", "anthropic-session", ProviderFamilyAnthropic, ProviderFamilyOpenAI); got != "" {
		t.Fatalf("non-preferred session provider = %q", got)
	}
	if got := ChooseSessionEnvProviderID("", "openai-session", ProviderFamilyOpenAI, ProviderFamilyOpenAI); got != "openai-session" {
		t.Fatalf("preferred session provider = %q", got)
	}
}

func TestRuntimeTargetBootstrapsOnlyRequestedProviderFamily(t *testing.T) {
	t.Run("generic names are OpenAI even when endpoint ends in messages", func(t *testing.T) {
		isolateLLMEnv(t)
		store := newResolverCoverageStore()
		target, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), &appconfig.Config{}, store, "sandbox-openai-generic", ProviderFamilyOpenAI, "openai-model", "", []domain.SandboxEnvVar{
			{Name: "LLM_API_ENDPOINT", Value: "https://gateway.example/v1/messages"},
			{Name: "LLM_API_KEY", Value: "openai-key"},
			{Name: "LLM_MODEL", Value: "openai-model"},
		})
		if err != nil {
			t.Fatalf("ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
		}
		if target.Provider.ProviderType != ProviderFamilyOpenAI || target.Provider.BaseURL != "https://gateway.example/v1/messages" || target.Provider.APIKey != "openai-key" {
			t.Fatalf("OpenAI target = %#v", target)
		}
	})

	t.Run("generic OpenAI names do not bootstrap Anthropic", func(t *testing.T) {
		isolateLLMEnv(t)
		store := newResolverCoverageStore()
		_, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), &appconfig.Config{}, store, "sandbox-anthropic-generic", ProviderFamilyAnthropic, "claude-model", "", []domain.SandboxEnvVar{
			{Name: "LLM_API_ENDPOINT", Value: "https://gateway.example/v1/messages"},
			{Name: "LLM_API_KEY", Value: "openai-key"},
			{Name: "LLM_MODEL", Value: "claude-model"},
		})
		if err == nil {
			t.Fatal("generic OpenAI names resolved as Anthropic")
		}
		if len(store.providers) != 0 {
			t.Fatalf("generic OpenAI names created providers: %#v", store.providers)
		}
	})

	t.Run("explicit Anthropic names bootstrap Anthropic without path inference", func(t *testing.T) {
		isolateLLMEnv(t)
		store := newResolverCoverageStore()
		target, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), &appconfig.Config{}, store, "sandbox-anthropic-explicit", ProviderFamilyAnthropic, "claude-model", "", []domain.SandboxEnvVar{
			{Name: "ANTHROPIC_API_ENDPOINT", Value: "https://gateway.example/anthropic"},
			{Name: "ANTHROPIC_AUTH_TOKEN", Value: "anthropic-key"},
			{Name: "CLAUDE_MODEL", Value: "claude-model"},
		})
		if err != nil {
			t.Fatalf("ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
		}
		if target.Provider.ProviderType != ProviderFamilyAnthropic || target.Provider.BaseURL != "https://gateway.example/anthropic" || target.Provider.APIKey != "anthropic-key" {
			t.Fatalf("Anthropic target = %#v", target)
		}
	})
}

func TestSandboxProviderEnvProvenanceDoesNotPersistSecrets(t *testing.T) {
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-provenance"}}
	SetSandboxProviderEnvItems(sandbox, []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://sandbox.example/v1"},
		{Name: "OPENAI_API_KEY", Value: "sandbox-secret", Secret: true},
		{Name: "EMPTY_VALUE", Value: ""},
	})
	data, err := json.Marshal(sandbox)
	if err != nil {
		t.Fatalf("marshal sandbox: %v", err)
	}
	if strings.Contains(string(data), "sandbox-secret") {
		t.Fatalf("sandbox metadata contains provider credential: %s", data)
	}
	for _, name := range []string{"LLM_API_ENDPOINT", "OPENAI_API_KEY"} {
		if !strings.Contains(string(data), name) {
			t.Fatalf("sandbox metadata does not contain override provenance %q: %s", name, data)
		}
	}
	if strings.Contains(string(data), "EMPTY_VALUE") {
		t.Fatalf("empty fallthrough value was recorded as an override: %s", data)
	}
}

func TestSandboxProviderEnvRestoresExplicitKeyFromSessionProvider(t *testing.T) {
	sandbox := &domain.Sandbox{
		Summary:                  domain.SandboxSummary{ID: "sandbox-explicit-key"},
		ProviderEnvOverrideNames: []string{"OPENAI_API_KEY"},
	}
	providers := []Provider{{
		ID:           SessionEnvProviderID(sandbox.Summary.ID, ProviderFamilyOpenAI),
		ProviderType: ProviderFamilyOpenAI,
		APIKey:       "persisted-explicit-key",
	}}
	overrides := SandboxProviderEnvItems(sandbox, ProviderFamilyOpenAI, providers)
	if got := EnvItemValue(overrides, "OPENAI_API_KEY"); got != "persisted-explicit-key" {
		t.Fatalf("restored explicit key = %q", got)
	}
}

func TestPersistedSandboxDoesNotPromoteInheritedGlobalEnvToOverride(t *testing.T) {
	isolateLLMEnv(t)
	ctx := context.Background()
	store := newResolverCoverageStore()
	store.global = []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://global-a.example/v1"},
		{Name: "LLM_API_KEY", Value: "global-a-key"},
		{Name: "LLM_MODEL", Value: "shared-model"},
	}
	if _, err := ResolveRuntimeLLMTargetWithEnv(ctx, &appconfig.Config{}, store, "sandbox-global", ProviderFamilyOpenAI, "shared-model", "", nil); err != nil {
		t.Fatalf("initial ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
	}
	sandbox := &domain.Sandbox{
		Summary: domain.SandboxSummary{ID: "sandbox-global"},
		EnvItems: []domain.SandboxEnvVar{
			{Name: "LLM_API_ENDPOINT", Value: "https://global-a.example/v1"},
			{Name: "LLM_MODEL", Value: "shared-model"},
		},
	}
	SetSandboxProviderEnvItems(sandbox, nil)

	store.global = []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://global-b.example/v1"},
		{Name: "LLM_API_KEY", Value: "global-b-key"},
		{Name: "LLM_MODEL", Value: "shared-model"},
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMProviders returned error: %v", err)
	}
	overrides := SandboxProviderEnvItems(sandbox, ProviderFamilyOpenAI, providers)
	if len(overrides) != 0 {
		t.Fatalf("inherited sandbox snapshot became overrides: %#v", overrides)
	}
	target, err := ResolveRuntimeLLMTargetWithEnv(ctx, &appconfig.Config{}, store, sandbox.Summary.ID, ProviderFamilyOpenAI, "shared-model", "", overrides)
	if err != nil {
		t.Fatalf("refreshed ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
	}
	if target.Provider.ID != ProviderIDDefaultOpenAI || target.Provider.BaseURL != "https://global-b.example/v1" || target.Provider.APIKey != "global-b-key" {
		t.Fatalf("refreshed target = %#v", target)
	}
}

func TestPersistedProtocolOverrideFollowsRotatedGlobalEndpointAndKey(t *testing.T) {
	isolateLLMEnv(t)
	ctx := context.Background()
	store := newResolverCoverageStore()
	store.global = []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://global-a.example/v1"},
		{Name: "LLM_API_KEY", Value: "global-a-key"},
		{Name: "LLM_MODEL", Value: "shared-model"},
	}
	sandbox := &domain.Sandbox{
		Summary: domain.SandboxSummary{ID: "sandbox-protocol"},
		EnvItems: []domain.SandboxEnvVar{
			{Name: "LLM_API_ENDPOINT", Value: "https://global-a.example/v1"},
			{Name: "LLM_API_PROTOCOL", Value: APIProtocolChatCompletions},
			{Name: "LLM_MODEL", Value: "shared-model"},
		},
	}
	SetSandboxProviderEnvItems(sandbox, []domain.SandboxEnvVar{{Name: "LLM_API_PROTOCOL", Value: APIProtocolChatCompletions}})
	if _, err := ResolveRuntimeLLMTargetWithEnv(ctx, &appconfig.Config{}, store, sandbox.Summary.ID, ProviderFamilyOpenAI, "shared-model", "", sandbox.ProviderEnvItems); err != nil {
		t.Fatalf("initial ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
	}

	sandbox.ProviderEnvItems = nil
	store.global[0].Value = "https://global-b.example/v1"
	store.global[1].Value = "global-b-key"
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMProviders returned error: %v", err)
	}
	overrides := SandboxProviderEnvItems(sandbox, ProviderFamilyOpenAI, providers)
	if EnvItemValue(overrides, "LLM_API_ENDPOINT") != "" || EnvItemValue(overrides, "LLM_API_PROTOCOL") != APIProtocolChatCompletions {
		t.Fatalf("restored overrides = %#v", overrides)
	}
	target, err := ResolveRuntimeLLMTargetWithEnv(ctx, &appconfig.Config{}, store, sandbox.Summary.ID, ProviderFamilyOpenAI, "shared-model", "", overrides)
	if err != nil {
		t.Fatalf("refreshed ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
	}
	if target.Provider.Scope != ProviderScopeSessionEnv || target.Provider.BaseURL != "https://global-b.example/v1" || target.Provider.APIKey != "global-b-key" || target.WireAPI != APIProtocolChatCompletions {
		t.Fatalf("refreshed target = %#v", target)
	}
}

func TestSessionProviderRefreshesInheritedKey(t *testing.T) {
	isolateLLMEnv(t)
	ctx := context.Background()
	store := newResolverCoverageStore()
	store.global = []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://global.example/v1"},
		{Name: "LLM_API_PROTOCOL", Value: APIProtocolResponses},
		{Name: "LLM_API_KEY", Value: "old-global-key"},
		{Name: "LLM_MODEL", Value: "shared-model"},
	}
	sandbox := &domain.Sandbox{
		Summary: domain.SandboxSummary{ID: "sandbox-key-rotation"},
		EnvItems: []domain.SandboxEnvVar{
			{Name: "LLM_API_ENDPOINT", Value: "https://sandbox.example/v1"},
			{Name: "LLM_API_PROTOCOL", Value: APIProtocolResponses},
			{Name: "LLM_MODEL", Value: "shared-model"},
		},
	}
	SetSandboxProviderEnvItems(sandbox, []domain.SandboxEnvVar{{Name: "LLM_API_ENDPOINT", Value: "https://sandbox.example/v1"}})
	initial, err := ResolveRuntimeLLMTargetWithEnv(ctx, &appconfig.Config{}, store, sandbox.Summary.ID, ProviderFamilyOpenAI, "shared-model", "", sandbox.ProviderEnvItems)
	if err != nil {
		t.Fatalf("initial ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
	}
	if initial.Provider.APIKey != "old-global-key" {
		t.Fatalf("initial provider key = %q", initial.Provider.APIKey)
	}

	// Simulate loading the sandbox after ProviderEnvItems has been discarded.
	sandbox.ProviderEnvItems = nil
	store.global[2].Value = "rotated-global-key"
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMProviders returned error: %v", err)
	}
	overrides := SandboxProviderEnvItems(sandbox, ProviderFamilyOpenAI, providers)
	refreshed, err := ResolveRuntimeLLMTargetWithEnv(ctx, &appconfig.Config{}, store, sandbox.Summary.ID, ProviderFamilyOpenAI, "shared-model", "", overrides)
	if err != nil {
		t.Fatalf("refreshed ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
	}
	if refreshed.Provider.BaseURL != "https://sandbox.example/v1" || refreshed.Provider.APIKey != "rotated-global-key" {
		t.Fatalf("refreshed provider = %#v", refreshed.Provider)
	}
}

func TestTransientSessionProviderDoesNotOverrideLaterGlobalEnvironment(t *testing.T) {
	isolateLLMEnv(t)
	ctx := context.Background()
	store := newResolverCoverageStore()
	store.global = []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://global.example/v1"},
		{Name: "LLM_API_PROTOCOL", Value: APIProtocolResponses},
		{Name: "LLM_API_KEY", Value: "global-key"},
		{Name: "LLM_MODEL", Value: "shared-model"},
	}

	transient, err := ResolveRuntimeLLMTargetWithEnv(ctx, &appconfig.Config{}, store, "sandbox-transient", ProviderFamilyOpenAI, "shared-model", "", []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://transient.example/v1"},
		{Name: "LLM_API_KEY", Value: "transient-key"},
	})
	if err != nil {
		t.Fatalf("transient ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
	}
	if transient.Provider.Scope != ProviderScopeSessionEnv || transient.Provider.BaseURL != "https://transient.example/v1" || transient.Provider.APIKey != "transient-key" {
		t.Fatalf("transient target = %#v", transient)
	}

	inherited, err := ResolveRuntimeLLMTargetWithEnv(ctx, &appconfig.Config{}, store, "sandbox-transient", ProviderFamilyOpenAI, "shared-model", "", nil)
	if err != nil {
		t.Fatalf("inherited ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
	}
	if inherited.Provider.ID != ProviderIDDefaultOpenAI || inherited.Provider.BaseURL != "https://global.example/v1" || inherited.Provider.APIKey != "global-key" {
		t.Fatalf("inherited target = %#v", inherited)
	}
}

func TestRuntimeTargetFailsClosedWhenGlobalEnvironmentCannotBeRead(t *testing.T) {
	isolateLLMEnv(t)
	globalErr := errors.New("global environment unavailable")
	store := newResolverCoverageStore()
	store.globalErr = globalErr

	_, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), &appconfig.Config{
		LLMAPIKey: "daemon-key",
		LLMModel:  "daemon-model",
	}, store, "sandbox-global-error", ProviderFamilyOpenAI, "", "", []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://sandbox.example/v1"},
	})
	if !errors.Is(err, globalErr) {
		t.Fatalf("ResolveRuntimeLLMTargetWithEnv error = %v, want %v", err, globalErr)
	}
	if len(store.providers) != 0 || len(store.models) != 0 {
		t.Fatalf("failed resolution persisted providers=%#v models=%#v", store.providers, store.models)
	}
}

func TestRuntimeTargetSessionProviderInheritsUnsetOpenAIFields(t *testing.T) {
	tests := []struct {
		name         string
		sandboxEnv   []domain.SandboxEnvVar
		wantEndpoint string
		wantProtocol string
		wantKey      string
		wantModel    string
	}{
		{
			name:         "endpoint only",
			sandboxEnv:   []domain.SandboxEnvVar{{Name: "LLM_API_ENDPOINT", Value: "https://sandbox.example/v1"}},
			wantEndpoint: "https://sandbox.example/v1",
			wantProtocol: APIProtocolResponses,
			wantKey:      "global-key",
			wantModel:    "global-model",
		},
		{
			name:         "protocol only",
			sandboxEnv:   []domain.SandboxEnvVar{{Name: "LLM_API_PROTOCOL", Value: APIProtocolChatCompletions}},
			wantEndpoint: "https://global.example/v1",
			wantProtocol: APIProtocolChatCompletions,
			wantKey:      "global-key",
			wantModel:    "global-model",
		},
		{
			name:         "key alias only",
			sandboxEnv:   []domain.SandboxEnvVar{{Name: "OPENAI_API_KEY", Value: "sandbox-key"}},
			wantEndpoint: "https://global.example/v1",
			wantProtocol: APIProtocolResponses,
			wantKey:      "sandbox-key",
			wantModel:    "global-model",
		},
		{
			name:         "model only",
			sandboxEnv:   []domain.SandboxEnvVar{{Name: "LLM_MODEL", Value: "sandbox-model"}},
			wantEndpoint: "https://global.example/v1",
			wantProtocol: APIProtocolResponses,
			wantKey:      "global-key",
			wantModel:    "sandbox-model",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateLLMEnv(t)
			store := newResolverCoverageStore()
			store.global = []domain.SandboxEnvVar{
				{Name: "LLM_API_ENDPOINT", Value: "https://global.example/v1"},
				{Name: "LLM_API_PROTOCOL", Value: APIProtocolResponses},
				{Name: "LLM_API_KEY", Value: "global-key"},
				{Name: "LLM_MODEL", Value: "global-model"},
			}
			target, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), &appconfig.Config{}, store, "sandbox-layered", ProviderFamilyOpenAI, "", "", tc.sandboxEnv)
			if err != nil {
				t.Fatalf("ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
			}
			if target.Provider.Scope != ProviderScopeSessionEnv {
				t.Fatalf("provider scope = %q, want session env", target.Provider.Scope)
			}
			if target.Provider.BaseURL != tc.wantEndpoint || target.Provider.APIKey != tc.wantKey || target.WireAPI != tc.wantProtocol || target.Model.ID != tc.wantModel {
				t.Fatalf("target = %#v, want endpoint=%q protocol=%q key=%q model=%q", target, tc.wantEndpoint, tc.wantProtocol, tc.wantKey, tc.wantModel)
			}
			wantPath := "/responses"
			if tc.wantProtocol == APIProtocolChatCompletions {
				wantPath = "/chat/completions"
			}
			if !strings.HasSuffix(target.Endpoint, wantPath) {
				t.Fatalf("upstream endpoint = %q, want suffix %q", target.Endpoint, wantPath)
			}
		})
	}
}

func TestRuntimeTargetSessionAnthropicFieldsUseLayeredAliases(t *testing.T) {
	isolateLLMEnv(t)
	store := newResolverCoverageStore()
	store.global = []domain.SandboxEnvVar{
		{Name: "ANTHROPIC_BASE_URL", Value: "https://global.anthropic.example"},
		{Name: "ANTHROPIC_API_KEY", Value: "global-anthropic-key"},
		{Name: "ANTHROPIC_MODEL", Value: "global-claude"},
	}
	target, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), &appconfig.Config{}, store, "sandbox-anthropic", ProviderFamilyAnthropic, "", "", []domain.SandboxEnvVar{
		{Name: "ANTHROPIC_API_ENDPOINT", Value: "https://sandbox.anthropic.example"},
		{Name: "ANTHROPIC_AUTH_TOKEN", Value: "sandbox-auth-token"},
		{Name: "CLAUDE_MODEL", Value: "sandbox-claude"},
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
	}
	if target.Provider.BaseURL != "https://sandbox.anthropic.example" || target.Provider.APIKey != "sandbox-auth-token" || target.Provider.AuthHeader != "Authorization" || target.Provider.AuthScheme != "Bearer" || target.Model.ID != "sandbox-claude" {
		t.Fatalf("target = %#v", target)
	}
}

func TestRuntimeTargetSessionProviderInheritsUnsetAnthropicFields(t *testing.T) {
	tests := []struct {
		name           string
		sandboxEnv     []domain.SandboxEnvVar
		wantEndpoint   string
		wantKey        string
		wantModel      string
		wantAuthHeader string
		wantAuthScheme string
	}{
		{
			name:           "endpoint alias only",
			sandboxEnv:     []domain.SandboxEnvVar{{Name: "ANTHROPIC_API_ENDPOINT", Value: "https://sandbox.anthropic.example"}},
			wantEndpoint:   "https://sandbox.anthropic.example",
			wantKey:        "global-anthropic-key",
			wantModel:      "global-claude",
			wantAuthHeader: "x-api-key",
		},
		{
			name:           "auth token alias only",
			sandboxEnv:     []domain.SandboxEnvVar{{Name: "ANTHROPIC_AUTH_TOKEN", Value: "sandbox-auth-token"}},
			wantEndpoint:   "https://global.anthropic.example",
			wantKey:        "sandbox-auth-token",
			wantModel:      "global-claude",
			wantAuthHeader: "Authorization",
			wantAuthScheme: "Bearer",
		},
		{
			name:           "model alias only",
			sandboxEnv:     []domain.SandboxEnvVar{{Name: "CLAUDE_MODEL", Value: "sandbox-claude"}},
			wantEndpoint:   "https://global.anthropic.example",
			wantKey:        "global-anthropic-key",
			wantModel:      "sandbox-claude",
			wantAuthHeader: "x-api-key",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateLLMEnv(t)
			store := newResolverCoverageStore()
			store.global = []domain.SandboxEnvVar{
				{Name: "ANTHROPIC_BASE_URL", Value: "https://global.anthropic.example"},
				{Name: "ANTHROPIC_API_KEY", Value: "global-anthropic-key"},
				{Name: "ANTHROPIC_MODEL", Value: "global-claude"},
			}
			target, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), &appconfig.Config{}, store, "sandbox-anthropic-layered", ProviderFamilyAnthropic, "", "", tc.sandboxEnv)
			if err != nil {
				t.Fatalf("ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
			}
			if target.Provider.Scope != ProviderScopeSessionEnv {
				t.Fatalf("provider scope = %q, want session env", target.Provider.Scope)
			}
			if target.Provider.BaseURL != tc.wantEndpoint || target.Provider.APIKey != tc.wantKey || target.Model.ID != tc.wantModel || target.Provider.AuthHeader != tc.wantAuthHeader || target.Provider.AuthScheme != tc.wantAuthScheme {
				t.Fatalf("target = %#v, want endpoint=%q key=%q model=%q auth header=%q scheme=%q", target, tc.wantEndpoint, tc.wantKey, tc.wantModel, tc.wantAuthHeader, tc.wantAuthScheme)
			}
			if !strings.HasSuffix(target.Endpoint, "/messages") {
				t.Fatalf("upstream endpoint = %q, want messages path", target.Endpoint)
			}
		})
	}
}

func TestRuntimeTargetConfiguredProviderPrecedesEnvironmentBootstrap(t *testing.T) {
	isolateLLMEnv(t)
	store := newResolverCoverageStore()
	store.providers = []Provider{{
		ID:             "system-provider",
		ProviderType:   ProviderFamilyOpenAI,
		DefaultWireAPI: APIProtocolResponses,
		BaseURL:        "https://system.example/v1",
		APIKey:         "system-key",
		Enabled:        true,
		Scope:          ProviderScopeSystem,
	}}
	store.models = []Model{{ID: "system-model", Name: "system-model", Enabled: true, Scope: ProviderScopeSystem}}
	store.wire["system-provider\x00system-model"] = APIProtocolResponses

	target, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), &appconfig.Config{
		LLMAPIEndpoint: "https://daemon.example/v1",
		LLMAPIKey:      "daemon-key",
	}, store, "sandbox-system", ProviderFamilyOpenAI, "system-model", "", []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://sandbox.example/v1"},
		{Name: "LLM_API_PROTOCOL", Value: APIProtocolChatCompletions},
		{Name: "LLM_API_KEY", Value: "sandbox-key"},
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
	}
	if target.Provider.ID != "system-provider" || target.Provider.BaseURL != "https://system.example/v1" || target.WireAPI != APIProtocolResponses {
		t.Fatalf("target = %#v, want configured system provider", target)
	}
	if len(store.providers) != 1 {
		t.Fatalf("providers = %#v, environment bootstrap should not add a provider", store.providers)
	}

	store.providers = append(store.providers, Provider{
		ID:             "explicit-provider",
		ProviderType:   ProviderFamilyOpenAI,
		DefaultWireAPI: APIProtocolChatCompletions,
		BaseURL:        "https://explicit.example/v1",
		APIKey:         "explicit-key",
		Enabled:        true,
		Scope:          ProviderScopeEnvDefault,
	})
	store.wire["explicit-provider\x00system-model"] = APIProtocolChatCompletions
	explicitTarget, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), &appconfig.Config{}, store, "sandbox-system", ProviderFamilyOpenAI, "system-model", "explicit-provider", nil)
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTargetWithEnv explicit provider returned error: %v", err)
	}
	if explicitTarget.Provider.ID != "explicit-provider" || explicitTarget.WireAPI != APIProtocolChatCompletions {
		t.Fatalf("explicit target = %#v", explicitTarget)
	}
}

func mustDefaultProviderEnvSources(t *testing.T, config *appconfig.Config, store GlobalEnvStore) providerEnvSources {
	t.Helper()
	sources, err := defaultProviderEnvSources(context.Background(), config, store)
	if err != nil {
		t.Fatalf("defaultProviderEnvSources returned error: %v", err)
	}
	return sources
}

func mustLayeredProviderEnvSources(t *testing.T, config *appconfig.Config, store GlobalEnvStore, items []domain.SandboxEnvVar) (providerEnvSources, []domain.SandboxEnvVar) {
	t.Helper()
	sources, normalized, err := layeredProviderEnvSources(context.Background(), config, store, items)
	if err != nil {
		t.Fatalf("layeredProviderEnvSources returned error: %v", err)
	}
	return sources, normalized
}
