package llms

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
)

func TestSandboxProviderEnvItemsFiltersProviderFamilies(t *testing.T) {
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-family"}}
	SetSandboxProviderEnvItems(sandbox, []domain.SandboxEnvVar{
		{Name: "LLM_API_KEY", Value: "generic-key", Secret: true},
		{Name: "OPENAI_API_KEY", Value: "openai-key", Secret: true},
		{Name: "OPENAI_BASE_URL", Value: "https://openai.example/v1"},
		{Name: "ANTHROPIC_API_KEY", Value: "anthropic-key", Secret: true},
		{Name: "ANTHROPIC_BASE_URL", Value: "https://anthropic.example"},
		{Name: "CLAUDE_MODEL", Value: "claude-session"},
		{Name: "CLAUDE_CODE_PATH", Value: "/custom/claude"},
	})

	openAI, err := SandboxProviderEnvItems(context.Background(), nil, sandbox, ProviderFamilyOpenAI)
	if err != nil {
		t.Fatalf("SandboxProviderEnvItems OpenAI returned error: %v", err)
	}
	if EnvItemValue(openAI, "OPENAI_API_KEY") != "openai-key" || EnvItemValue(openAI, "LLM_API_KEY") != "generic-key" {
		t.Fatalf("OpenAI provider env = %#v", openAI)
	}
	if EnvItemValue(openAI, "ANTHROPIC_API_KEY") != "" || EnvItemValue(openAI, "ANTHROPIC_BASE_URL") != "" || EnvItemValue(openAI, "CLAUDE_MODEL") != "" {
		t.Fatalf("OpenAI provider env contains Anthropic values: %#v", openAI)
	}

	anthropic, err := SandboxProviderEnvItems(context.Background(), nil, sandbox, ProviderFamilyAnthropic)
	if err != nil {
		t.Fatalf("SandboxProviderEnvItems Anthropic returned error: %v", err)
	}
	if EnvItemValue(anthropic, "ANTHROPIC_API_KEY") != "anthropic-key" || EnvItemValue(anthropic, "LLM_API_KEY") != "generic-key" || EnvItemValue(anthropic, "CLAUDE_MODEL") != "claude-session" {
		t.Fatalf("Anthropic provider env = %#v", anthropic)
	}
	if EnvItemValue(anthropic, "OPENAI_API_KEY") != "" || EnvItemValue(anthropic, "OPENAI_BASE_URL") != "" || EnvItemValue(anthropic, "CLAUDE_CODE_PATH") != "" {
		t.Fatalf("Anthropic provider env contains OpenAI values: %#v", anthropic)
	}
}

func TestSandboxProviderEnvProvenanceDoesNotPersistSecrets(t *testing.T) {
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-provenance"}}
	SetSandboxProviderEnvItems(sandbox, []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://sandbox.example/v1"},
		{Name: "OPENAI_API_KEY", Value: "sandbox-secret", Secret: true},
		{Name: "CLAUDE_MODEL", Value: "claude-session"},
		{Name: "CLAUDE_CODE_PATH", Value: "/custom/claude"},
		{Name: "EMPTY", Value: ""},
	})

	data, err := json.Marshal(sandbox)
	if err != nil {
		t.Fatalf("marshal sandbox: %v", err)
	}
	if strings.Contains(string(data), "sandbox-secret") {
		t.Fatalf("sandbox metadata contains provider secret: %s", data)
	}
	if !strings.Contains(string(data), `"provider_env_override_names":["CLAUDE_MODEL","LLM_API_ENDPOINT","OPENAI_API_KEY"]`) {
		t.Fatalf("sandbox metadata has unexpected provenance: %s", data)
	}

	var persisted domain.Sandbox
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("unmarshal sandbox: %v", err)
	}
	if persisted.ProviderEnvOverrideNames == nil || len(persisted.ProviderEnvItems) != 0 {
		t.Fatalf("persisted sandbox = %#v", persisted)
	}

	empty := &domain.Sandbox{}
	SetSandboxProviderEnvItems(empty, nil)
	emptyData, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty provenance: %v", err)
	}
	var emptyRoundTrip domain.Sandbox
	if err := json.Unmarshal(emptyData, &emptyRoundTrip); err != nil {
		t.Fatalf("unmarshal empty provenance: %v", err)
	}
	if emptyRoundTrip.ProviderEnvOverrideNames == nil {
		t.Fatalf("recorded empty provenance became indistinguishable from missing provenance: %s", emptyData)
	}
	var withoutProvenance domain.Sandbox
	if err := json.Unmarshal([]byte(`{"summary":{}}`), &withoutProvenance); err != nil {
		t.Fatalf("unmarshal sandbox without provenance: %v", err)
	}
	if withoutProvenance.ProviderEnvOverrideNames != nil {
		t.Fatalf("missing provenance = %#v, want nil", withoutProvenance.ProviderEnvOverrideNames)
	}
}

func TestSandboxProviderEnvItemsReconstructsRestartedOverrides(t *testing.T) {
	store := newResolverCoverageStore()
	store.providers = []Provider{
		{ID: SessionEnvProviderID("sandbox-restart", ProviderFamilyOpenAI), ProviderType: ProviderFamilyOpenAI, APIKey: "persisted-openai-key", Enabled: true},
		{ID: SessionEnvProviderID("sandbox-restart", ProviderFamilyAnthropic), ProviderType: ProviderFamilyAnthropic, APIKey: "persisted-anthropic-key", Enabled: true},
	}
	sandbox := &domain.Sandbox{
		Summary: domain.SandboxSummary{ID: "sandbox-restart"},
		EnvItems: []domain.SandboxEnvVar{
			{Name: "LLM_API_ENDPOINT", Value: "https://sandbox.example/v1"},
			{Name: "ANTHROPIC_BASE_URL", Value: "https://anthropic.example"},
			{Name: "CLAUDE_MODEL", Value: "claude-session"},
		},
		ProviderEnvOverrideNames: []string{"LLM_API_ENDPOINT", "OPENAI_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_MODEL"},
	}

	openAI, err := SandboxProviderEnvItems(context.Background(), store, sandbox, ProviderFamilyOpenAI)
	if err != nil {
		t.Fatalf("reconstruct OpenAI provider env: %v", err)
	}
	if EnvItemValue(openAI, "OPENAI_API_KEY") != "persisted-openai-key" || EnvItemValue(openAI, "LLM_API_ENDPOINT") == "" {
		t.Fatalf("reconstructed OpenAI env = %#v", openAI)
	}
	if EnvItemValue(openAI, "ANTHROPIC_BASE_URL") != "" || EnvItemValue(openAI, "CLAUDE_MODEL") != "" {
		t.Fatalf("reconstructed OpenAI env contains Anthropic value: %#v", openAI)
	}

	anthropic, err := SandboxProviderEnvItems(context.Background(), store, sandbox, ProviderFamilyAnthropic)
	if err != nil {
		t.Fatalf("reconstruct Anthropic provider env: %v", err)
	}
	if EnvItemValue(anthropic, "ANTHROPIC_AUTH_TOKEN") != "persisted-anthropic-key" || EnvItemValue(anthropic, "ANTHROPIC_BASE_URL") == "" || EnvItemValue(anthropic, "CLAUDE_MODEL") != "claude-session" {
		t.Fatalf("reconstructed Anthropic env = %#v", anthropic)
	}
}

func TestSandboxProviderEnvItemsSeparatesRecordedEmptyFromMissingProvenance(t *testing.T) {
	recorded := &domain.Sandbox{
		EnvItems:                 []domain.SandboxEnvVar{{Name: "LLM_API_KEY", Value: "global-snapshot"}},
		ProviderEnvOverrideNames: []string{},
	}
	items, err := SandboxProviderEnvItems(context.Background(), nil, recorded, ProviderFamilyOpenAI)
	if err != nil {
		t.Fatalf("recorded provider env: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("recorded empty provenance used Global Env snapshot: %#v", items)
	}

	withoutProvenance := &domain.Sandbox{EnvItems: []domain.SandboxEnvVar{
		{Name: "LLM_API_KEY", Value: "snapshot-key"},
		{Name: "ANTHROPIC_API_KEY", Value: "snapshot-anthropic-key"},
	}}
	items, err = SandboxProviderEnvItems(context.Background(), nil, withoutProvenance, ProviderFamilyOpenAI)
	if err != nil {
		t.Fatalf("provider env without provenance: %v", err)
	}
	if EnvItemValue(items, "LLM_API_KEY") != "snapshot-key" || EnvItemValue(items, "ANTHROPIC_API_KEY") != "" {
		t.Fatalf("family-filtered fallback without provenance = %#v", items)
	}
}

func TestRestoreSandboxTransientFieldsPreservesPendingProviderProvenance(t *testing.T) {
	source := &domain.Sandbox{}
	SetSandboxProviderEnvItems(source, nil)
	destination := &domain.Sandbox{}
	domain.RestoreSandboxTransientFields(destination, source)
	if destination.ProviderEnvOverrideNames == nil {
		t.Fatal("recorded empty provenance was lost during sandbox reload")
	}

	SetSandboxProviderEnvItems(source, []domain.SandboxEnvVar{{Name: "OPENAI_API_KEY", Value: "secret", Secret: true}})
	domain.RestoreSandboxTransientFields(destination, source)
	if len(destination.ProviderEnvOverrideNames) != 1 || destination.ProviderEnvOverrideNames[0] != "OPENAI_API_KEY" {
		t.Fatalf("restored provenance = %#v", destination.ProviderEnvOverrideNames)
	}
	if EnvItemValue(destination.ProviderEnvItems, "OPENAI_API_KEY") != "secret" {
		t.Fatalf("restored transient provider env = %#v", destination.ProviderEnvItems)
	}
}

func TestRuntimeTargetUsesLayeredProviderEnvironmentForRequestedFamily(t *testing.T) {
	isolateLLMEnv(t)
	store := newResolverCoverageStore()
	store.global = []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://global.example/v1"},
		{Name: "LLM_API_PROTOCOL", Value: APIProtocolResponses},
		{Name: "LLM_API_KEY", Value: "global-key"},
		{Name: "LLM_MODEL", Value: "global-model"},
		{Name: "ANTHROPIC_BASE_URL", Value: "https://global.anthropic.example"},
		{Name: "ANTHROPIC_API_KEY", Value: "global-anthropic-key"},
		{Name: "ANTHROPIC_MODEL", Value: "global-claude"},
	}

	target, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), &appconfig.Config{}, store, "sandbox-layered", ProviderFamilyOpenAI, "", "", []domain.SandboxEnvVar{
		{Name: "OPENAI_API_KEY", Value: "sandbox-key"},
	})
	if err != nil {
		t.Fatalf("resolve layered OpenAI target: %v", err)
	}
	if target.Provider.ID != SessionEnvProviderID("sandbox-layered", ProviderFamilyOpenAI) || target.Provider.APIKey != "sandbox-key" || target.Provider.BaseURL != "https://global.example/v1" || target.Model.ID != "global-model" {
		t.Fatalf("layered OpenAI target = %#v", target)
	}
	for _, provider := range store.providers {
		if provider.ProviderType == ProviderFamilyAnthropic {
			t.Fatalf("OpenAI resolution bootstrapped Anthropic provider: %#v", store.providers)
		}
	}
}

func TestRuntimeTargetUsesSharedDefaultInsteadOfGlobalSessionSnapshot(t *testing.T) {
	isolateLLMEnv(t)
	store := newResolverCoverageStore()
	store.global = []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://global.example/v1"},
		{Name: "LLM_API_KEY", Value: "global-key"},
		{Name: "LLM_MODEL", Value: "global-model"},
	}
	target, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), &appconfig.Config{}, store, "sandbox-global-only", ProviderFamilyOpenAI, "", "", nil)
	if err != nil {
		t.Fatalf("resolve default OpenAI target: %v", err)
	}
	if target.Provider.ID != ProviderIDDefaultOpenAI || target.Provider.Scope != ProviderScopeEnvDefault {
		t.Fatalf("global-only target = %#v", target)
	}
}

func TestRuntimeTargetKeepsAnthropicCredentialFromWinningLayer(t *testing.T) {
	tests := []struct {
		name             string
		sandboxItems     []domain.SandboxEnvVar
		globalItems      []domain.SandboxEnvVar
		processAPIKey    string
		processAuthToken string
		configKey        string
		wantKey          string
		wantHeader       string
		wantScheme       string
	}{
		{
			name:         "sandbox auth token beats Global Env api key",
			sandboxItems: []domain.SandboxEnvVar{{Name: "ANTHROPIC_AUTH_TOKEN", Value: "sandbox-auth-token"}},
			globalItems:  []domain.SandboxEnvVar{{Name: "ANTHROPIC_API_KEY", Value: "global-api-key"}},
			wantKey:      "sandbox-auth-token",
			wantHeader:   "Authorization",
			wantScheme:   "Bearer",
		},
		{
			name:         "sandbox generic key beats Global Env auth token",
			sandboxItems: []domain.SandboxEnvVar{{Name: "LLM_API_KEY", Value: "sandbox-generic-key"}},
			globalItems:  []domain.SandboxEnvVar{{Name: "ANTHROPIC_AUTH_TOKEN", Value: "global-auth-token"}},
			wantKey:      "sandbox-generic-key",
			wantHeader:   "x-api-key",
		},
		{
			name: "family-specific token beats generic key within sandbox",
			sandboxItems: []domain.SandboxEnvVar{
				{Name: "LLM_API_KEY", Value: "sandbox-generic-key"},
				{Name: "ANTHROPIC_AUTH_TOKEN", Value: "sandbox-auth-token"},
			},
			wantKey:    "sandbox-auth-token",
			wantHeader: "Authorization",
			wantScheme: "Bearer",
		},
		{
			name:             "Global Env generic key beats process auth token",
			globalItems:      []domain.SandboxEnvVar{{Name: "LLM_API_KEY", Value: "global-generic-key"}},
			processAuthToken: "process-auth-token",
			wantKey:          "global-generic-key",
			wantHeader:       "x-api-key",
		},
		{
			name:             "process auth token beats daemon config key",
			processAuthToken: "process-auth-token",
			configKey:        "config-generic-key",
			wantKey:          "process-auth-token",
			wantHeader:       "Authorization",
			wantScheme:       "Bearer",
		},
		{
			name:          "process api key beats daemon config key",
			processAPIKey: "process-api-key",
			configKey:     "config-generic-key",
			wantKey:       "process-api-key",
			wantHeader:    "x-api-key",
		},
		{
			name:       "daemon config generic key is x-api-key",
			configKey:  "config-generic-key",
			wantKey:    "config-generic-key",
			wantHeader: "x-api-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateLLMEnv(t)
			t.Setenv("ANTHROPIC_API_KEY", tt.processAPIKey)
			t.Setenv("ANTHROPIC_AUTH_TOKEN", tt.processAuthToken)
			store := newResolverCoverageStore()
			store.global = append([]domain.SandboxEnvVar{
				{Name: "ANTHROPIC_BASE_URL", Value: "https://global.anthropic.example"},
				{Name: "ANTHROPIC_MODEL", Value: "global-claude"},
			}, tt.globalItems...)
			target, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), &appconfig.Config{LLMAPIKey: tt.configKey}, store, "sandbox-anthropic-auth", ProviderFamilyAnthropic, "", "", tt.sandboxItems)
			if err != nil {
				t.Fatalf("resolve layered Anthropic target: %v", err)
			}
			if target.Provider.APIKey != tt.wantKey || target.Provider.AuthHeader != tt.wantHeader || target.Provider.AuthScheme != tt.wantScheme {
				t.Fatalf("layered Anthropic credential = key %q header %q scheme %q", target.Provider.APIKey, target.Provider.AuthHeader, target.Provider.AuthScheme)
			}
		})
	}
}

func TestRuntimeTargetUsesGenericEnvironmentForExplicitAnthropicFamily(t *testing.T) {
	isolateLLMEnv(t)
	store := newResolverCoverageStore()
	target, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), &appconfig.Config{}, store, "sandbox-generic-anthropic", ProviderFamilyAnthropic, "", "", []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://gateway.example/api/anthropic"},
		{Name: "LLM_API_KEY", Value: "generic-key"},
		{Name: "LLM_MODEL", Value: "generic-claude"},
	})
	if err != nil {
		t.Fatalf("resolve generic Anthropic target: %v", err)
	}
	if target.Provider.ProviderType != ProviderFamilyAnthropic || target.Provider.BaseURL != "https://gateway.example/api/anthropic" || target.Provider.APIKey != "generic-key" || target.Model.ID != "generic-claude" {
		t.Fatalf("generic Anthropic target = %#v", target)
	}
	if target.Endpoint != "https://gateway.example/api/anthropic/messages" {
		t.Fatalf("generic Anthropic endpoint = %q", target.Endpoint)
	}
	for _, provider := range store.providers {
		if provider.ProviderType == ProviderFamilyOpenAI {
			t.Fatalf("explicit Anthropic resolution bootstrapped OpenAI provider: %#v", store.providers)
		}
	}
}

func TestRuntimeTargetUsesMessagesProtocolWhenProviderFamilyIsUnspecified(t *testing.T) {
	isolateLLMEnv(t)
	store := newResolverCoverageStore()
	target, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), &appconfig.Config{}, store, "sandbox-protocol-anthropic", "", "", "", []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://gateway.example/api/anthropic"},
		{Name: "LLM_API_PROTOCOL", Value: APIProtocolMessages},
		{Name: "LLM_API_KEY", Value: "generic-key"},
		{Name: "LLM_MODEL", Value: "generic-claude"},
	})
	if err != nil {
		t.Fatalf("resolve protocol-selected Anthropic target: %v", err)
	}
	if target.Provider.ProviderType != ProviderFamilyAnthropic || target.WireAPI != APIProtocolMessages {
		t.Fatalf("protocol-selected target = %#v", target)
	}
	for _, provider := range store.providers {
		if provider.ProviderType == ProviderFamilyOpenAI {
			t.Fatalf("messages protocol bootstrapped OpenAI provider: %#v", store.providers)
		}
	}
}
