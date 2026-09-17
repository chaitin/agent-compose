package llms

import (
	"context"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

func configuredConnection(id string, scope string) Provider {
	return Provider{
		ID:             id,
		ProviderType:   ProviderFamilyOpenAI,
		DefaultWireAPI: APIProtocolChatCompletions,
		BaseURL:        "https://" + id + ".test/v1",
		APIKey:         id + "-key",
		Enabled:        true,
		Scope:          scope,
	}
}

// A connection created through the provider API (scope=api) has no model or
// binding row. A bare model name must still resolve against it: agent-compose
// otherwise rejects configuration that is complete from the operator's view.
func TestResolveRuntimeLLMTargetUsesSingleConfiguredConnectionForBareModel(t *testing.T) {
	isolateLLMEnv(t)
	store := newResolverCoverageStore()
	store.providers = []Provider{configuredConnection("gateway", ProviderScopeAPI)}

	target, err := ResolveRuntimeLLMTarget(context.Background(), &appconfig.Config{}, store, "qwen3-8b", "")
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTarget returned error: %v", err)
	}
	if target.Provider.ID != "gateway" || target.Model.ID != "qwen3-8b" {
		t.Fatalf("target = %#v, want gateway/qwen3-8b", target)
	}
	if target.Endpoint != "https://gateway.test/v1/chat/completions" {
		t.Fatalf("endpoint = %q", target.Endpoint)
	}
}

func TestResolveRuntimeLLMTargetPrefersReservedDefaultConnection(t *testing.T) {
	isolateLLMEnv(t)
	store := newResolverCoverageStore()
	store.providers = []Provider{
		configuredConnection("gateway", ProviderScopeAPI),
		configuredConnection(ProviderIDDefaultOpenAI, ProviderScopeEnvDefault),
	}

	target, err := ResolveRuntimeLLMTarget(context.Background(), &appconfig.Config{}, store, "qwen3-8b", "")
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTarget returned error: %v", err)
	}
	if target.Provider.ID != ProviderIDDefaultOpenAI {
		t.Fatalf("provider = %q, want reserved default connection", target.Provider.ID)
	}
}

func TestResolveRuntimeLLMTargetRejectsAmbiguousDefaultConnection(t *testing.T) {
	isolateLLMEnv(t)
	store := newResolverCoverageStore()
	store.providers = []Provider{
		configuredConnection("gateway-a", ProviderScopeAPI),
		configuredConnection("gateway-b", ProviderScopeAPI),
	}

	_, err := ResolveRuntimeLLMTarget(context.Background(), &appconfig.Config{}, store, "qwen3-8b", "")
	if err == nil {
		t.Fatal("ambiguous bare model resolved without an error")
	}
	if !strings.Contains(err.Error(), "gateway-a") || !strings.Contains(err.Error(), "gateway-b") {
		t.Fatalf("ambiguous error = %v, want both connection ids", err)
	}
}

func TestResolveRuntimeLLMTargetIgnoresDisabledConnectionForBareModel(t *testing.T) {
	isolateLLMEnv(t)
	store := newResolverCoverageStore()
	disabled := configuredConnection("gateway", ProviderScopeAPI)
	disabled.Enabled = false
	store.providers = []Provider{disabled}

	if _, err := ResolveRuntimeLLMTarget(context.Background(), &appconfig.Config{}, store, "qwen3-8b", ""); err == nil {
		t.Fatal("disabled connection served a bare model")
	}
}

// Bindings remain authoritative metadata: an explicitly registered model still
// selects its bound connection instead of the daemon default.
func TestResolveRuntimeLLMTargetPrefersModelBindingOverDefaultConnection(t *testing.T) {
	isolateLLMEnv(t)
	store := newResolverCoverageStore()
	store.providers = []Provider{
		configuredConnection(ProviderIDDefaultOpenAI, ProviderScopeEnvDefault),
		configuredConnection("catalog", ProviderScopeCatalog),
	}
	store.models = []Model{{ID: "gpt-5.6-sol", Name: "gpt-5.6-sol", Enabled: true, Scope: ProviderScopeCatalog}}
	store.wire["catalog\x00gpt-5.6-sol"] = APIProtocolResponses

	target, err := ResolveRuntimeLLMTarget(context.Background(), &appconfig.Config{}, store, "gpt-5.6-sol", "")
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTarget returned error: %v", err)
	}
	if target.Provider.ID != "catalog" || target.WireAPI != APIProtocolResponses {
		t.Fatalf("target = %#v, want the bound catalog connection", target)
	}
}

// The connection prefix is optional: only an unqualified value is a literal
// model name, and a qualified one keeps its established dispatch meaning.
func TestSplitModelReferenceLeavesUnqualifiedModelBare(t *testing.T) {
	for _, tc := range []struct {
		value    string
		provider string
		model    string
	}{
		{value: "", provider: "", model: ""},
		{value: "qwen3-8b", provider: "", model: "qwen3-8b"},
		{value: "  qwen3-8b  ", provider: "", model: "qwen3-8b"},
		{value: "gateway/org/deepseek-v4", provider: "gateway", model: "org/deepseek-v4"},
		{value: "openai/gpt-test", provider: "openai", model: "gpt-test"},
	} {
		provider, model, err := SplitModelReference(tc.value)
		if err != nil {
			t.Fatalf("SplitModelReference(%q) returned error: %v", tc.value, err)
		}
		if provider != tc.provider || model != tc.model {
			t.Fatalf("SplitModelReference(%q) = (%q, %q), want (%q, %q)", tc.value, provider, model, tc.provider, tc.model)
		}
	}
}

// A value that carries a slash but leaves a side empty is a typo, not a model
// name: resolving it would publish a facade token for a model that cannot exist
// upstream. An absent value stays valid and means "use the daemon default".
func TestSplitModelReferenceRejectsEmptyReferenceSide(t *testing.T) {
	for _, value := range []string{"/model", "connection/", "/", "  /model  "} {
		if _, _, err := SplitModelReference(value); err == nil {
			t.Fatalf("SplitModelReference(%q) returned no error, want a malformed-reference error", value)
		}
	}
}

// bootstrapConfig is the historical .env channel: base, key and (optionally)
// a model.
func bootstrapConfig(model string) *appconfig.Config {
	return &appconfig.Config{
		LLMAPIEndpoint: "https://api.openai.test",
		LLMAPIProtocol: APIProtocolResponses,
		LLMAPIKey:      "openai-key",
		LLMModel:       model,
	}
}

// The bootstrap environment stays the default channel end to end. This pins the
// complete request path rather than the default-connection tier: a complete
// bootstrap environment short-circuits in ResolveRuntimeLLMTargetWithEnv before
// defaultConfiguredConnection runs, so adding a connection through the RPC must
// not move an agent that names only a model. The tier itself is covered by
// TestResolveRuntimeLLMTargetPrefersPersistedBootstrapConnection.
func TestResolveRuntimeLLMTargetKeepsBootstrapChannelForBareModel(t *testing.T) {
	isolateLLMEnv(t)
	store := newResolverCoverageStore()
	store.providers = []Provider{configuredConnection("gateway", ProviderScopeAPI)}

	target, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), store, RuntimeLLMTargetQuery{
		Config: bootstrapConfig("gpt-env-default"), SessionID: "session-1", RequestedModel: "qwen3-8b",
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
	}
	if target.Provider.ID != ProviderIDDefaultOpenAI || target.Model.ID != "qwen3-8b" {
		t.Fatalf("target = %#v, want the bootstrap channel with the literal model", target)
	}
}

// An agent that declares no model uses the bootstrap model, which is why the
// model field is optional in a project definition.
func TestResolveRuntimeLLMTargetUsesBootstrapModelWhenAgentDeclaresNone(t *testing.T) {
	isolateLLMEnv(t)
	store := newResolverCoverageStore()
	store.providers = []Provider{configuredConnection("gateway", ProviderScopeAPI)}

	target, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), store, RuntimeLLMTargetQuery{
		Config: bootstrapConfig("gpt-env-default"), SessionID: "session-1",
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
	}
	if target.Provider.ID != ProviderIDDefaultOpenAI || target.Model.ID != "gpt-env-default" {
		t.Fatalf("target = %#v, want the bootstrap model", target)
	}
}

// The reserved bootstrap connection is the daemon default even when it survives
// only as a persisted env_default row: it wins over a connection added through
// the RPC. This reaches defaultConfiguredConnection itself because a live
// environment is absent, so it exercises the default-connection tier directly
// rather than the bootstrap short-circuit in ResolveRuntimeLLMTargetWithEnv.
func TestResolveRuntimeLLMTargetPrefersPersistedBootstrapConnection(t *testing.T) {
	isolateLLMEnv(t)
	store := newResolverCoverageStore()
	store.providers = []Provider{
		configuredConnection(ProviderIDDefaultOpenAI, ProviderScopeEnvDefault),
		configuredConnection("gateway", ProviderScopeAPI),
	}

	target, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), store, RuntimeLLMTargetQuery{
		Config: &appconfig.Config{}, SessionID: "session-1", RequestedModel: "qwen3-8b",
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
	}
	if target.Provider.ID != ProviderIDDefaultOpenAI || target.Model.ID != "qwen3-8b" {
		t.Fatalf("target = %#v, want the reserved bootstrap connection", target)
	}
}

// A reserved connection is the daemon default even when its family differs from
// the other configured connection, because a bare model does not imply a family
// and agent-compose does not interpret model names. The operator selects a
// different connection by qualifying the model as <connection>/<model>.
func TestResolveRuntimeLLMTargetUsesReservedBootstrapFamilyForBareModel(t *testing.T) {
	isolateLLMEnv(t)
	anthropic := configuredConnection(ProviderIDDefaultAnthropic, ProviderScopeEnvDefault)
	anthropic.ProviderType = ProviderFamilyAnthropic
	anthropic.DefaultWireAPI = APIProtocolMessages
	store := newResolverCoverageStore()
	store.providers = []Provider{anthropic, configuredConnection("gateway", ProviderScopeAPI)}

	target, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), store, RuntimeLLMTargetQuery{
		Config: &appconfig.Config{}, SessionID: "session-1", RequestedModel: "qwen3-8b",
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
	}
	if target.Provider.ID != ProviderIDDefaultAnthropic || target.Model.ID != "qwen3-8b" {
		t.Fatalf("target = %#v, want the reserved bootstrap connection", target)
	}

	qualified, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), store, RuntimeLLMTargetQuery{
		Config: &appconfig.Config{}, SessionID: "session-1", RequestedModel: "gateway/qwen3-8b",
	})
	if err != nil {
		t.Fatalf("qualified ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
	}
	if qualified.Provider.ID != "gateway" || qualified.Model.ID != "qwen3-8b" {
		t.Fatalf("qualified target = %#v, want the explicit gateway connection", qualified)
	}
}
