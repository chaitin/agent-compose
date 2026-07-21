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
	if environment.endpoint != "https://global.example/v1" || environment.protocol != APIProtocolChatCompletions {
		t.Fatalf("resolved endpoint/protocol = %q/%q", environment.endpoint, environment.protocol)
	}
	if environment.apiKey != "sandbox-alias-key" || environment.model != "global-model" {
		t.Fatalf("resolved key/model = %q/%q", environment.apiKey, environment.model)
	}
	if EnvItemValue(sandboxItems, "LLM_API_ENDPOINT") != "" {
		t.Fatalf("empty sandbox endpoint unexpectedly became a value: %#v", sandboxItems)
	}

	canonical := providerEnvSources{envItemsSource([]domain.SandboxEnvVar{
		{Name: "LLM_API_KEY", Value: "canonical"},
		{Name: "OPENAI_API_KEY", Value: "alias"},
	})}.openAIEnvironment()
	if canonical.apiKey != "canonical" {
		t.Fatalf("same-layer canonical key lost to alias")
	}
}

func TestProviderFamilyDoesNotDependOnEndpointPath(t *testing.T) {
	isolateLLMEnv(t)
	sources := providerEnvSources{envItemsSource([]domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://gateway.example/v1/messages"},
		{Name: "LLM_API_KEY", Value: "openai-key"},
		{Name: "LLM_MODEL", Value: "openai-model"},
	})}
	if got := sources.openAIEnvironment(); !got.configured || got.endpoint == "" || got.apiKey == "" {
		t.Fatalf("generic variables did not configure OpenAI family: %#v", got)
	}
	if got := sources.anthropicEnvironment(); got.configured || got.endpoint != "" || got.apiKey != "" {
		t.Fatalf("generic endpoint leaked into Anthropic family: %#v", got)
	}

	store := newResolverCoverageStore()
	target, err := ResolveFacadeTargetWithEnv(context.Background(), &appconfig.Config{}, store, ProviderFamilyOpenAI, "openai-model", "", []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://gateway.example/v1/messages"},
		{Name: "LLM_API_KEY", Value: "openai-key"},
	})
	if err != nil {
		t.Fatalf("ResolveFacadeTargetWithEnv returned error: %v", err)
	}
	if target.Target.Provider.ProviderType != ProviderFamilyOpenAI {
		t.Fatalf("provider family = %q", target.Target.Provider.ProviderType)
	}
}

func TestSandboxProviderEnvironmentHasSeparatePersistentAndExecutionLayers(t *testing.T) {
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-layered"}}
	SetSandboxProviderEnvItems(sandbox, []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://sandbox.example/v1"},
		{Name: "LLM_API_KEY", Value: "sandbox-key", Secret: true},
		{Name: "ANTHROPIC_MODEL", Value: "claude-sandbox"},
		{Name: "ORDINARY", Value: "not-provider-env"},
	})
	sandbox.ExecutionProviderEnvItems = []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://execution.example/v1"},
		{Name: "OPENAI_API_KEY", Value: "execution-key", Secret: true},
	}

	openAI := SandboxProviderEnvItems(sandbox, ProviderFamilyOpenAI)
	if EnvItemValue(openAI, "LLM_API_ENDPOINT") != "https://execution.example/v1" || EnvItemValue(openAI, "OPENAI_API_KEY") != "execution-key" {
		t.Fatalf("effective execution layer = %#v", openAI)
	}
	if EnvItemValue(openAI, "ANTHROPIC_MODEL") != "" || EnvItemValue(sandbox.ProviderEnvItems, "ORDINARY") != "" {
		t.Fatalf("provider family filtering failed: sandbox=%#v effective=%#v", sandbox.ProviderEnvItems, openAI)
	}

	data, err := json.Marshal(sandbox)
	if err != nil {
		t.Fatalf("marshal sandbox: %v", err)
	}
	var persisted domain.Sandbox
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("unmarshal sandbox: %v", err)
	}
	if EnvItemValue(persisted.ProviderEnvItems, "LLM_API_KEY") != "sandbox-key" {
		t.Fatalf("persisted sandbox provider layer = %#v", persisted.ProviderEnvItems)
	}
	if len(persisted.ExecutionProviderEnvItems) != 0 {
		t.Fatalf("execution layer was persisted: %#v", persisted.ExecutionProviderEnvItems)
	}

	envOnly := &domain.Sandbox{EnvItems: []domain.SandboxEnvVar{{Name: "LLM_API_ENDPOINT", Value: "https://ordinary.example/v1"}}}
	if got := SandboxProviderEnvItems(envOnly, ProviderFamilyOpenAI); len(got) != 0 {
		t.Fatalf("ordinary EnvItems were inferred as Provider Env: %#v", got)
	}
}

func TestFacadeEnvironmentTargetInheritsFieldsIndependently(t *testing.T) {
	tests := []struct {
		name         string
		overrides    []domain.SandboxEnvVar
		wantEndpoint string
		wantProtocol string
		wantKey      string
		wantModel    string
	}{
		{
			name:         "endpoint only",
			overrides:    []domain.SandboxEnvVar{{Name: "LLM_API_ENDPOINT", Value: "https://sandbox.example/v1"}},
			wantEndpoint: "https://sandbox.example/v1",
			wantProtocol: APIProtocolResponses,
			wantKey:      "global-key",
			wantModel:    "global-model",
		},
		{
			name:         "protocol only",
			overrides:    []domain.SandboxEnvVar{{Name: "LLM_API_PROTOCOL", Value: APIProtocolChatCompletions}},
			wantEndpoint: "https://global.example/v1",
			wantProtocol: APIProtocolChatCompletions,
			wantKey:      "global-key",
			wantModel:    "global-model",
		},
		{
			name:         "key alias only",
			overrides:    []domain.SandboxEnvVar{{Name: "OPENAI_API_KEY", Value: "sandbox-key"}},
			wantEndpoint: "https://global.example/v1",
			wantProtocol: APIProtocolResponses,
			wantKey:      "sandbox-key",
			wantModel:    "global-model",
		},
		{
			name:         "model only",
			overrides:    []domain.SandboxEnvVar{{Name: "LLM_MODEL", Value: "sandbox-model"}},
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
			selection, err := ResolveFacadeTargetWithEnv(context.Background(), &appconfig.Config{}, store, ProviderFamilyOpenAI, "", "", tc.overrides)
			if err != nil {
				t.Fatalf("ResolveFacadeTargetWithEnv returned error: %v", err)
			}
			target := selection.Target
			if target.Provider.BaseURL != tc.wantEndpoint || target.Provider.APIKey != tc.wantKey || target.WireAPI != tc.wantProtocol || target.Model.ID != tc.wantModel {
				t.Fatalf("target = %#v", target)
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

func TestFacadeEnvironmentTargetUsesAnthropicAliases(t *testing.T) {
	isolateLLMEnv(t)
	store := newResolverCoverageStore()
	store.global = []domain.SandboxEnvVar{
		{Name: "ANTHROPIC_BASE_URL", Value: "https://global.anthropic.example"},
		{Name: "ANTHROPIC_API_KEY", Value: "global-anthropic-key"},
		{Name: "ANTHROPIC_MODEL", Value: "global-claude"},
	}
	selection, err := ResolveFacadeTargetWithEnv(context.Background(), &appconfig.Config{}, store, ProviderFamilyAnthropic, "", "", []domain.SandboxEnvVar{
		{Name: "ANTHROPIC_API_ENDPOINT", Value: "https://sandbox.anthropic.example"},
		{Name: "ANTHROPIC_AUTH_TOKEN", Value: "sandbox-auth-token"},
		{Name: "CLAUDE_MODEL", Value: "sandbox-claude"},
	})
	if err != nil {
		t.Fatalf("ResolveFacadeTargetWithEnv returned error: %v", err)
	}
	target := selection.Target
	if target.Provider.BaseURL != "https://sandbox.anthropic.example/v1" || target.Provider.AuthHeader != "Authorization" || target.Provider.AuthScheme != "Bearer" || target.Model.ID != "sandbox-claude" {
		t.Fatalf("target = %#v", target)
	}
	if !strings.HasSuffix(target.Endpoint, "/messages") {
		t.Fatalf("upstream endpoint = %q", target.Endpoint)
	}
}

func TestFacadeProviderSelectionPrecedesEnvironmentResolution(t *testing.T) {
	isolateLLMEnv(t)
	store := configuredFacadeProviderStore()
	store.globalErr = errors.New("global environment must not be read")

	selection, err := ResolveFacadeTargetWithEnv(context.Background(), &appconfig.Config{}, store, ProviderFamilyOpenAI, "system-model", "", []domain.SandboxEnvVar{
		{Name: "LLM_API_KEY", Value: "unused-sandbox-key"},
	})
	if err != nil {
		t.Fatalf("system provider resolution returned error: %v", err)
	}
	if selection.Environment != nil || selection.Target.Provider.ID != "system-provider" {
		t.Fatalf("system provider selection = %#v", selection)
	}

	store.providers = append(store.providers, Provider{
		ID:             "explicit-provider",
		ProviderType:   ProviderFamilyOpenAI,
		DefaultWireAPI: APIProtocolChatCompletions,
		BaseURL:        "https://explicit.example/v1",
		Enabled:        true,
		Scope:          ProviderScopeSystem,
	})
	explicit, err := ResolveFacadeTargetWithEnv(context.Background(), &appconfig.Config{}, store, ProviderFamilyOpenAI, "any-model", "explicit-provider", nil)
	if err != nil {
		t.Fatalf("explicit provider resolution returned error: %v", err)
	}
	if explicit.Target.Provider.ID != "explicit-provider" || explicit.Target.WireAPI != APIProtocolChatCompletions {
		t.Fatalf("explicit provider selection = %#v", explicit)
	}
}

func TestFacadeEnvironmentResolutionFailsClosedWhenGlobalEnvCannotBeRead(t *testing.T) {
	isolateLLMEnv(t)
	wantErr := errors.New("global environment unavailable")
	store := newResolverCoverageStore()
	store.globalErr = wantErr
	_, err := ResolveFacadeTargetWithEnv(context.Background(), &appconfig.Config{LLMModel: "daemon-model"}, store, ProviderFamilyOpenAI, "", "", nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("ResolveFacadeTargetWithEnv error = %v, want %v", err, wantErr)
	}
}

func TestFacadeTokenEnvironmentsAreIsolatedAndRefreshInheritedFields(t *testing.T) {
	isolateLLMEnv(t)
	ctx := context.Background()
	store := newResolverCoverageStore()
	store.facadeEnvironments = map[string]FacadeEnvironment{}
	store.global = []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://global-a.example/v1"},
		{Name: "LLM_API_PROTOCOL", Value: APIProtocolResponses},
		{Name: "LLM_API_KEY", Value: "global-a-key"},
		{Name: "LLM_MODEL", Value: "global-model"},
	}

	firstTarget, err := ResolveFacadeTargetWithEnv(ctx, &appconfig.Config{}, store, ProviderFamilyOpenAI, "model-a", "", []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://execution-a.example/v1"},
	})
	if err != nil {
		t.Fatalf("resolve first target: %v", err)
	}
	_, firstGrant, err := NewFacadeGrant("sandbox-1", firstTarget, APIProtocolResponses, "agent", "run-a")
	if err != nil {
		t.Fatalf("create first grant: %v", err)
	}
	store.facadeEnvironments[firstGrant.Environment.ProviderID] = *firstGrant.Environment

	secondTarget, err := ResolveFacadeTargetWithEnv(ctx, &appconfig.Config{}, store, ProviderFamilyOpenAI, "model-b", "", []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://execution-b.example/v1"},
		{Name: "LLM_API_KEY", Value: "execution-b-key"},
	})
	if err != nil {
		t.Fatalf("resolve second target: %v", err)
	}
	_, secondGrant, err := NewFacadeGrant("sandbox-1", secondTarget, APIProtocolResponses, "agent", "run-b")
	if err != nil {
		t.Fatalf("create second grant: %v", err)
	}
	store.facadeEnvironments[secondGrant.Environment.ProviderID] = *secondGrant.Environment
	if firstGrant.Token.ProviderID == secondGrant.Token.ProviderID {
		t.Fatal("independent executions shared a facade provider id")
	}

	store.global[1].Value = APIProtocolChatCompletions
	store.global[2].Value = "global-b-key"
	first, err := ResolveFacadeRuntimeTarget(ctx, &appconfig.Config{}, store, "runtime-model", firstGrant.Token.ProviderID)
	if err != nil {
		t.Fatalf("resolve first runtime target: %v", err)
	}
	second, err := ResolveFacadeRuntimeTarget(ctx, &appconfig.Config{}, store, "runtime-model", secondGrant.Token.ProviderID)
	if err != nil {
		t.Fatalf("resolve second runtime target: %v", err)
	}
	if first.Provider.BaseURL != "https://execution-a.example/v1" || first.Provider.APIKey != "global-b-key" || first.WireAPI != APIProtocolChatCompletions {
		t.Fatalf("first runtime target = %#v", first)
	}
	if second.Provider.BaseURL != "https://execution-b.example/v1" || second.Provider.APIKey != "execution-b-key" || second.WireAPI != APIProtocolChatCompletions {
		t.Fatalf("second runtime target = %#v", second)
	}

	if _, err := ResolveFacadeRuntimeTarget(ctx, &appconfig.Config{}, store, "runtime-model", FacadeEnvironmentProviderID("missing", ProviderFamilyOpenAI)); err == nil {
		t.Fatal("missing token environment did not fail closed")
	}
}

func configuredFacadeProviderStore() *resolverCoverageStore {
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
	return store
}

func mustLayeredProviderEnvSources(t *testing.T, config *appconfig.Config, store GlobalEnvStore, items []domain.SandboxEnvVar) (providerEnvSources, []domain.SandboxEnvVar) {
	t.Helper()
	sources, normalized, err := layeredProviderEnvSources(context.Background(), config, store, items)
	if err != nil {
		t.Fatalf("layeredProviderEnvSources returned error: %v", err)
	}
	return sources, normalized
}
