package api

import (
	"testing"

	"github.com/chaitin/agent-compose/pkg/llms"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// The wrapper message is what makes "leave the stored set alone" and "clear it"
// distinguishable on the wire, so the mapping must not collapse them.
func TestModelSpecsFromV2DistinguishesAbsentFromEmpty(t *testing.T) {
	if got := modelSpecsFromV2(nil); got != nil {
		t.Fatalf("absent models mapped to %#v, want nil", got)
	}
	empty := modelSpecsFromV2(&agentcomposev2.LLMProviderModels{})
	if empty == nil || len(*empty) != 0 {
		t.Fatalf("present empty models mapped to %#v, want a non-nil empty set", empty)
	}
}

func TestProviderToV2ReportsStoredModelFields(t *testing.T) {
	provider := llms.Provider{ID: "gateway", DefaultWireAPI: llms.APIProtocolResponses}
	bindings := []llms.ProviderModelBinding{
		{ProviderID: "gateway", ModelID: "alpha", Config: llms.ProviderModelConfig{DisplayName: "Alpha", HeadersJSON: `{"x-tier":"batch"}`}},
		{ProviderID: "other", ModelID: "ignored"},
		{ProviderID: "gateway", ModelID: "beta", Config: llms.ProviderModelConfig{WireAPI: llms.APIProtocolChatCompletions, BaseURL: "https://alt.example/v1", MaxOutputTokens: 100}},
	}
	converted, err := providerToV2(provider, bindings, nil)
	if err != nil {
		t.Fatalf("providerToV2: %v", err)
	}
	models := converted.GetModels()
	if len(models) != 2 || models[0].GetId() != "alpha" || models[1].GetId() != "beta" {
		t.Fatalf("models = %#v, want only this connection's models in binding order", models)
	}
	if models[0].GetHeaders()["x-tier"] != "batch" || models[0].GetMaxOutputTokens() != 0 {
		t.Fatalf("inheriting model = %#v", models[0])
	}
	if models[1].GetProtocol() != "chat_completions" || models[1].GetBaseUrl() != "https://alt.example/v1" || models[1].GetMaxOutputTokens() != 100 {
		t.Fatalf("overriding model = %#v", models[1])
	}
	if converted.GetCapabilities() != nil {
		t.Fatalf("capabilities = %#v, want absent without a verdict", converted.GetCapabilities())
	}
}

// Stored headers the daemon cannot decode must be reported, not dropped: a
// silent empty map would look like a model that declares no headers.
func TestProviderToV2RejectsMalformedStoredHeaders(t *testing.T) {
	bindings := []llms.ProviderModelBinding{
		{ProviderID: "gateway", ModelID: "alpha", Config: llms.ProviderModelConfig{HeadersJSON: "{not-json"}},
	}
	if _, err := providerToV2(llms.Provider{ID: "gateway"}, bindings, nil); err == nil {
		t.Fatal("malformed stored headers were accepted")
	}
}
