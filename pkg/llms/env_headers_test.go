package llms

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

	t.Run("merges custom and extra headers", func(t *testing.T) {
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
		if headers["Anthropic-Version"] != "2023-06-01" || headers["Bizscenario"] != "mobile-learning" || len(headers) != 2 {
			t.Fatalf("headers = %#v", headers)
		}
	})

	t.Run("rejects overriding provider managed headers", func(t *testing.T) {
		_, err := envProviderHeadersJSON(mapLookup(map[string]string{
			llmAPIHeadersEnv: `{"ANTHROPIC-version":"2024-01-01"}`,
		}), map[string]string{"anthropic-version": "2023-06-01"})
		if !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("error = %v, want failed precondition", err)
		}
	})

	t.Run("rejects non-object and invalid values", func(t *testing.T) {
		tests := []string{`[]`, `"x"`, `null`, `{"A":1}`, `{"A":"1","B":true}`, `{"A":null}`, "not-json"}
		for _, raw := range tests {
			if _, err := envProviderHeadersJSON(mapLookup(map[string]string{llmAPIHeadersEnv: raw}), nil); !errors.Is(err, domain.ErrFailedPrecondition) {
				t.Fatalf("raw %q error = %v, want failed precondition", raw, err)
			}
		}
	})

	t.Run("rejects invalid HTTP names and values", func(t *testing.T) {
		tests := []string{
			`{"":"x"}`,
			`{"Bad Header":"x"}`,
			"{\"A\\n\":\"x\"}",
			"{\"A\":\"x\\r\\n\"}",
			"{\"A\":\"x\\u0000\"}",
		}
		for _, raw := range tests {
			if _, err := envProviderHeadersJSON(mapLookup(map[string]string{llmAPIHeadersEnv: raw}), nil); !errors.Is(err, domain.ErrFailedPrecondition) {
				t.Fatalf("raw %q error = %v, want failed precondition", raw, err)
			}
		}
	})

	t.Run("rejects duplicate names case insensitively", func(t *testing.T) {
		tests := []string{
			`{"X-Tenant":"a","x-tenant":"b"}`,
			`{"X-Tenant":"a"," X-Tenant ":"b"}`,
			`{"X-Tenant":"a","X-Tenant":"b"}`,
		}
		for _, raw := range tests {
			if _, err := envProviderHeadersJSON(mapLookup(map[string]string{llmAPIHeadersEnv: raw}), nil); !errors.Is(err, domain.ErrFailedPrecondition) {
				t.Fatalf("raw %q error = %v, want failed precondition", raw, err)
			}
		}
	})

	t.Run("rejects duplicate extra names case insensitively", func(t *testing.T) {
		_, err := envProviderHeadersJSON(nil, map[string]string{"X-Tenant": "a", "x-tenant": "b"})
		if !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("error = %v, want failed precondition", err)
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
	store := &projectionStore{}
	id, err := ensureOpenAIEnvProvider(context.Background(), store, mapLookup(values), reg)
	if err != nil {
		t.Fatalf("ensureOpenAIEnvProvider() error = %v", err)
	}
	if id == "" || len(store.upserts) != 1 {
		t.Fatalf("provider id = %q upserts = %#v", id, store.upserts)
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(store.upserts[0].Provider.HeadersJSON), &headers); err != nil {
		t.Fatalf("decode HeadersJSON %q: %v", store.upserts[0].Provider.HeadersJSON, err)
	}
	if headers["Bizscenario"] != "mobile-learning" {
		t.Fatalf("HeadersJSON = %s", store.upserts[0].Provider.HeadersJSON)
	}

	values[llmAPIHeadersEnv] = `{"Branchid":"010801"}`
	if _, err := ensureOpenAIEnvProvider(context.Background(), store, mapLookup(values), reg); err != nil {
		t.Fatalf("update error = %v", err)
	}
	headers = map[string]string{}
	if err := json.Unmarshal([]byte(store.upserts[len(store.upserts)-1].Provider.HeadersJSON), &headers); err != nil {
		t.Fatalf("decode updated HeadersJSON: %v", err)
	}
	if headers["Branchid"] != "010801" || headers["Bizscenario"] != "" {
		t.Fatalf("updated headers = %#v", headers)
	}

	values[llmAPIHeadersEnv] = "{}"
	if _, err := ensureOpenAIEnvProvider(context.Background(), store, mapLookup(values), reg); err != nil {
		t.Fatalf("clear error = %v", err)
	}
	if got := store.upserts[len(store.upserts)-1].Provider.HeadersJSON; got != "{}" {
		t.Fatalf("cleared HeadersJSON = %q", got)
	}
}

func TestOpenAIEnvProviderRejectsInvalidLLMAPIHeaders(t *testing.T) {
	store := &projectionStore{}
	_, err := ensureOpenAIEnvProvider(context.Background(), store, mapLookup(map[string]string{
		"LLM_API_ENDPOINT": "https://gateway.example/openai",
		"LLM_API_KEY":      "generic-key",
		"LLM_MODEL":        "test-model",
		llmAPIHeadersEnv:   "{broken",
	}), EnvProviderRegistration{ProviderID: "openai-env", Name: "openai-env", Scope: ProviderScopeEnvDefault})
	if err == nil {
		t.Fatal("ensureOpenAIEnvProvider() returned nil error")
	}
	if len(store.upserts) != 0 {
		t.Fatalf("invalid headers still persisted %#v", store.upserts)
	}
}

func TestAnthropicEnvProviderMergesLLMAPIHeaders(t *testing.T) {
	lookup := mapLookup(map[string]string{
		"LLM_API_ENDPOINT": "https://gateway.example/api/anthropic",
		"LLM_API_KEY":      "generic-key",
		"LLM_MODEL":        "test-model",
		llmAPIHeadersEnv:   `{"Bizscenario":"mobile-learning"}`,
	})
	credential, ok := anthropicCredentialFromValues("", "", lookup("LLM_API_KEY"))
	if !ok {
		t.Fatal("generic key did not produce an Anthropic credential")
	}
	store := &projectionStore{}
	id, err := ensureAnthropicEnvProvider(context.Background(), store, lookup, anthropicEnvProviderInput{
		Credential: credential,
		EnvProviderRegistration: EnvProviderRegistration{
			ProviderID: "anthropic-env", Name: "anthropic-env", Scope: ProviderScopeEnvDefault,
		},
	})
	if err != nil {
		t.Fatalf("ensureAnthropicEnvProvider() error = %v", err)
	}
	if id == "" || len(store.upserts) != 1 {
		t.Fatalf("provider id = %q upserts = %#v", id, store.upserts)
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(store.upserts[0].Provider.HeadersJSON), &headers); err != nil {
		t.Fatalf("decode HeadersJSON %q: %v", store.upserts[0].Provider.HeadersJSON, err)
	}
	if headers["Anthropic-Version"] != "2023-06-01" || headers["Bizscenario"] != "mobile-learning" {
		t.Fatalf("headers = %#v", headers)
	}
}

// TestDefaultEnvGenerateForwardsLLMAPIHeaders covers the daemon's environment
// connection end to end: LLM_API_HEADERS is stored on the projected connection
// and reaches the upstream request, with the provider credential winning over a
// header an operator tries to smuggle in.
func TestDefaultEnvGenerateForwardsLLMAPIHeaders(t *testing.T) {
	isolateLLMEnv(t)

	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"id":"chat-1","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	store := &projectionStore{}
	values := map[string]string{
		"LLM_API_ENDPOINT": server.URL,
		"LLM_API_PROTOCOL": APIProtocolChatCompletions,
		"LLM_API_KEY":      "gateway-key",
		"LLM_MODEL":        "gateway-model",
		llmAPIHeadersEnv:   `{"Bizscenario":"mobile-learning","Branchid":"010801","Servid":"1001","Authorization":"should-not-win"}`,
	}
	if _, err := ensureOpenAIEnvProvider(context.Background(), store, mapLookup(values), EnvProviderRegistration{
		ProviderID: "gateway", Name: "gateway", Scope: ProviderScopeEnvDefault,
	}); err != nil {
		t.Fatalf("ensureOpenAIEnvProvider() error = %v", err)
	}
	target, err := NewResolvedTarget(store.upserts[0].Provider, Model{ID: "gateway-model", Name: "gateway-model"}, ProviderModelConfig{})
	if err != nil {
		t.Fatalf("NewResolvedTarget() error = %v", err)
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
