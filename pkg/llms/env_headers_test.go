package llms

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestEnvProviderHeadersJSON(t *testing.T) {
	t.Run("missing and empty become empty object", func(t *testing.T) {
		for _, raw := range []string{"", "   ", "{}"} {
			got, err := envProviderHeadersJSON(mapLookup(map[string]string{llmAPIHeadersEnv: raw}), nil)
			if err != nil {
				t.Fatalf("raw %q error = %v", raw, err)
			}
			if got != "{}" {
				t.Fatalf("raw %q = %q, want {}", raw, got)
			}
		}
	})

	t.Run("parses static headers", func(t *testing.T) {
		got, err := envProviderHeadersJSON(mapLookup(map[string]string{
			llmAPIHeadersEnv: `{"Bizscenario":"mobile-learning","Branchid":"010801","Servid":"1001"}`,
		}), nil)
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		var headers map[string]string
		if err := json.Unmarshal([]byte(got), &headers); err != nil {
			t.Fatalf("decode %q: %v", got, err)
		}
		if headers["Bizscenario"] != "mobile-learning" || headers["Branchid"] != "010801" || headers["Servid"] != "1001" {
			t.Fatalf("headers = %#v", headers)
		}
	})

	t.Run("merges extra headers under custom values", func(t *testing.T) {
		got, err := envProviderHeadersJSON(mapLookup(map[string]string{
			llmAPIHeadersEnv: `{"Bizscenario":"mobile-learning"}`,
		}), map[string]string{"anthropic-version": "2023-06-01"})
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		var headers map[string]string
		if err := json.Unmarshal([]byte(got), &headers); err != nil {
			t.Fatalf("decode %q: %v", got, err)
		}
		if headers["anthropic-version"] != "2023-06-01" || headers["Bizscenario"] != "mobile-learning" {
			t.Fatalf("headers = %#v", headers)
		}
	})

	t.Run("rejects non-object and invalid values", func(t *testing.T) {
		tests := []string{`[]`, `"x"`, `{"A":1}`, `{"A":"1","B":true}`, "not-json"}
		for _, raw := range tests {
			if _, err := envProviderHeadersJSON(mapLookup(map[string]string{llmAPIHeadersEnv: raw}), nil); err == nil {
				t.Fatalf("raw %q returned nil error", raw)
			}
		}
	})

	t.Run("rejects empty names and CR LF", func(t *testing.T) {
		if _, err := envProviderHeadersJSON(mapLookup(map[string]string{llmAPIHeadersEnv: `{"":"x"}`}), nil); err == nil {
			t.Fatal("empty name returned nil error")
		}
		if _, err := envProviderHeadersJSON(mapLookup(map[string]string{llmAPIHeadersEnv: "{\"A\\n\":\"x\"}"}), nil); err == nil {
			t.Fatal("CR/LF name returned nil error")
		}
		if _, err := envProviderHeadersJSON(mapLookup(map[string]string{llmAPIHeadersEnv: "{\"A\":\"x\\r\\n\"}"}), nil); err == nil {
			t.Fatal("CR/LF value returned nil error")
		}
	})
}

func TestOpenAIEnvProviderPersistsLLMAPIHeaders(t *testing.T) {
	reg := EnvProviderRegistration{ProviderID: "openai-env", Name: "openai-env", Scope: ProviderScopeEnvDefault}
	values := map[string]string{
		"LLM_API_ENDPOINT": "https://gateway.example/v2/chat/completions",
		"LLM_API_PROTOCOL": APIProtocolChatCompletions,
		"LLM_API_KEY":      "generic-key",
		"LLM_MODEL":        "test-model",
		llmAPIHeadersEnv:   `{"Bizscenario":"mobile-learning","Authorization":"should-not-win"}`,
	}
	store := newResolverCoverageStore()
	id, err := EnsureOpenAIEnvProvider(context.Background(), store, mapLookup(values), reg)
	if err != nil {
		t.Fatalf("EnsureOpenAIEnvProvider() error = %v", err)
	}
	if id == "" || len(store.providers) != 1 {
		t.Fatalf("provider id = %q providers = %#v", id, store.providers)
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(store.providers[0].HeadersJSON), &headers); err != nil {
		t.Fatalf("decode HeadersJSON %q: %v", store.providers[0].HeadersJSON, err)
	}
	if headers["Bizscenario"] != "mobile-learning" {
		t.Fatalf("HeadersJSON = %s", store.providers[0].HeadersJSON)
	}

	values[llmAPIHeadersEnv] = `{"Branchid":"010801"}`
	if _, err := EnsureOpenAIEnvProvider(context.Background(), store, mapLookup(values), reg); err != nil {
		t.Fatalf("update error = %v", err)
	}
	headers = map[string]string{}
	if err := json.Unmarshal([]byte(store.providers[0].HeadersJSON), &headers); err != nil {
		t.Fatalf("decode updated HeadersJSON: %v", err)
	}
	if headers["Branchid"] != "010801" || headers["Bizscenario"] != "" {
		t.Fatalf("updated headers = %#v", headers)
	}

	values[llmAPIHeadersEnv] = "{}"
	if _, err := EnsureOpenAIEnvProvider(context.Background(), store, mapLookup(values), reg); err != nil {
		t.Fatalf("clear error = %v", err)
	}
	if store.providers[0].HeadersJSON != "{}" {
		t.Fatalf("cleared HeadersJSON = %q", store.providers[0].HeadersJSON)
	}
}

func TestOpenAIEnvProviderRejectsInvalidLLMAPIHeaders(t *testing.T) {
	store := newResolverCoverageStore()
	_, err := EnsureOpenAIEnvProvider(context.Background(), store, mapLookup(map[string]string{
		"LLM_API_ENDPOINT": "https://gateway.example/openai",
		"LLM_API_KEY":      "generic-key",
		"LLM_MODEL":        "test-model",
		llmAPIHeadersEnv:   "{broken",
	}), EnvProviderRegistration{ProviderID: "openai-env", Name: "openai-env", Scope: ProviderScopeEnvDefault})
	if err == nil {
		t.Fatal("EnsureOpenAIEnvProvider() returned nil error")
	}
	if len(store.providers) != 0 {
		t.Fatalf("invalid headers still persisted %#v", store.providers)
	}
}

func TestAnthropicEnvProviderMergesLLMAPIHeaders(t *testing.T) {
	store := newResolverCoverageStore()
	id, err := EnsureAnthropicEnvProvider(context.Background(), store, mapLookup(map[string]string{
		"LLM_API_ENDPOINT": "https://gateway.example/api/anthropic",
		"LLM_API_KEY":      "generic-key",
		"LLM_MODEL":        "test-model",
		llmAPIHeadersEnv:   `{"Bizscenario":"mobile-learning"}`,
	}), AnthropicEnvProviderRequest{
		AuthHeader: "x-api-key",
		EnvProviderRegistration: EnvProviderRegistration{
			ProviderID: "anthropic-env", Name: "anthropic-env", Scope: ProviderScopeSessionEnv,
		},
	})
	if err != nil {
		t.Fatalf("EnsureAnthropicEnvProvider() error = %v", err)
	}
	if id == "" || len(store.providers) != 1 {
		t.Fatalf("provider id = %q providers = %#v", id, store.providers)
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(store.providers[0].HeadersJSON), &headers); err != nil {
		t.Fatalf("decode HeadersJSON %q: %v", store.providers[0].HeadersJSON, err)
	}
	if headers["anthropic-version"] != "2023-06-01" || headers["Bizscenario"] != "mobile-learning" {
		t.Fatalf("headers = %#v", headers)
	}
}

func TestDefaultEnvGenerateForwardsLLMAPIHeaders(t *testing.T) {
	isolateLLMEnv(t)

	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"id":"chat-1","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	store := newResolverCoverageStore()
	store.global = []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: server.URL},
		{Name: "LLM_API_PROTOCOL", Value: APIProtocolChatCompletions},
		{Name: "LLM_API_KEY", Value: "gateway-key"},
		{Name: "LLM_MODEL", Value: "gateway-model"},
		{Name: llmAPIHeadersEnv, Value: `{"Bizscenario":"mobile-learning","Branchid":"010801","Servid":"1001","Authorization":"should-not-win"}`},
	}
	target, err := ResolveLLMTarget(context.Background(), &appconfig.Config{}, store, "")
	if err != nil {
		t.Fatalf("ResolveLLMTarget() error = %v", err)
	}
	if target.Headers.Get("Bizscenario") != "mobile-learning" || target.Headers.Get("Authorization") != "Bearer gateway-key" {
		t.Fatalf("resolved headers = %#v", target.Headers)
	}

	result, err := Generate(context.Background(), server.Client(), GenerateRequest{
		Endpoint: target.Endpoint,
		Protocol: target.WireAPI,
		Prompt:   "hello",
		Model:    target.Model.ID,
		Headers:  target.Headers,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if result.Text != "ok" {
		t.Fatalf("result = %#v", result)
	}
	if got.Get("Bizscenario") != "mobile-learning" || got.Get("Branchid") != "010801" || got.Get("Servid") != "1001" {
		t.Fatalf("upstream headers = %#v", got)
	}
	if got.Get("Authorization") != "Bearer gateway-key" {
		t.Fatalf("authorization = %q", got.Get("Authorization"))
	}
	if !strings.HasPrefix(got.Get("Content-Type"), "application/json") {
		t.Fatalf("content-type = %q", got.Get("Content-Type"))
	}
}
