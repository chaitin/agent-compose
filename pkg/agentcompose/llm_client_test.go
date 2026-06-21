package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestLLMClientResolveEndpointPrefersGlobalEnvThenProcessEnvThenDefault(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)
	client := &LLMClient{config: &appconfig.Config{LLMAPIEndpoint: "https://config.example.invalid"}, configDB: store}

	t.Setenv("LLM_API_ENDPOINT", "https://env.example.invalid")
	if got := client.resolveEndpoint(ctx); got != "https://env.example.invalid/v1/responses" {
		t.Fatalf("resolveEndpoint from process env = %q, want %q", got, "https://env.example.invalid/v1/responses")
	}

	if _, err := store.ReplaceGlobalEnv(ctx, []SessionEnvVar{{Name: "LLM_API_ENDPOINT", Value: "https://db.example.invalid", Secret: false}}); err != nil {
		t.Fatalf("ReplaceGlobalEnv returned error: %v", err)
	}
	if got := client.resolveEndpoint(ctx); got != "https://db.example.invalid/v1/responses" {
		t.Fatalf("resolveEndpoint from db env = %q, want %q", got, "https://db.example.invalid/v1/responses")
	}

	if _, err := store.ReplaceGlobalEnv(ctx, nil); err != nil {
		t.Fatalf("ReplaceGlobalEnv(reset) returned error: %v", err)
	}
	if err := os.Unsetenv("LLM_API_ENDPOINT"); err != nil {
		t.Fatalf("Unsetenv returned error: %v", err)
	}
	if got := client.resolveEndpoint(ctx); got != "https://config.example.invalid/v1/responses" {
		t.Fatalf("resolveEndpoint from config fallback = %q, want %q", got, "https://config.example.invalid/v1/responses")
	}

	client.config.LLMAPIEndpoint = ""
	if got := client.resolveEndpoint(ctx); got != "https://api.openai.com/v1/responses" {
		t.Fatalf("resolveEndpoint default = %q, want %q", got, "https://api.openai.com/v1/responses")
	}
}

func TestNormalizeLLMAPIEndpointKeepsExplicitPath(t *testing.T) {
	if got := normalizeLLMAPIEndpoint("https://api.example.invalid/v1/responses"); got != "https://api.example.invalid/v1/responses" {
		t.Fatalf("normalizeLLMAPIEndpoint explicit path = %q, want unchanged", got)
	}
	if got := normalizeLLMAPIEndpoint("https://api.example.invalid/custom/path"); got != "https://api.example.invalid/custom/path" {
		t.Fatalf("normalizeLLMAPIEndpoint custom path = %q, want unchanged", got)
	}
	if got := normalizeLLMAPIEndpointForProtocol("https://api.example.invalid", llmAPIProtocolChatCompletions); got != "https://api.example.invalid/v1/chat/completions" {
		t.Fatalf("normalizeLLMAPIEndpointForProtocol chat base = %q, want chat completions path", got)
	}
	if got := normalizeLLMAPIEndpointForProtocol("https://api.example.invalid/v1", llmAPIProtocolChatCompletions); got != "https://api.example.invalid/v1/chat/completions" {
		t.Fatalf("normalizeLLMAPIEndpointForProtocol chat v1 = %q, want chat completions path", got)
	}
	if got := normalizeLLMAPIEndpointForProtocol("https://api.example.invalid/custom/chat", llmAPIProtocolChatCompletions); got != "https://api.example.invalid/custom/chat" {
		t.Fatalf("normalizeLLMAPIEndpointForProtocol chat custom = %q, want unchanged", got)
	}
}

func TestLLMClientGenerateHandlesSuccessAndFailures(t *testing.T) {
	testLLMClientGenerateHandlesSuccessAndFailures(t)
}

func testLLMClientGenerateHandlesSuccessAndFailures(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	t.Setenv("LLM_API_ENDPOINT", "")
	store := newTestConfigStore(t)
	if _, err := store.ReplaceGlobalEnv(ctx, []SessionEnvVar{
		{Name: "LLM_API_KEY", Value: "env-key", Secret: true},
	}); err != nil {
		t.Fatalf("ReplaceGlobalEnv returned error: %v", err)
	}

	var gotAuth string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBody = readRequestBodyForTest(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-1","model":"model-a","status":"completed","output":[{"finish_reason":"stop","content":[{"text":"hello"},{"text":"world"}]}]}`))
	}))
	t.Cleanup(server.Close)

	client := &LLMClient{
		config:   &appconfig.Config{LLMAPIEndpoint: server.URL, LLMAPIKey: "fallback-key", LLMModel: "fallback-model"},
		configDB: store,
		client:   server.Client(),
	}
	result, err := client.Generate(ctx, "prompt", "model-a", "")
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if result.Text != "hello\nworld" || result.Model != "model-a" || result.ResponseID != "resp-1" || result.FinishReason != "stop" {
		t.Fatalf("unexpected generate result: %+v", result)
	}
	if gotAuth != "Bearer env-key" {
		t.Fatalf("authorization header = %q, want %q", gotAuth, "Bearer env-key")
	}
	if !strings.Contains(gotBody, `"input":"prompt"`) || !strings.Contains(gotBody, `"model":"model-a"`) {
		t.Fatalf("request body missing prompt/model: %s", gotBody)
	}

	if _, err := client.Generate(ctx, "   ", "model-a", ""); err == nil || !strings.Contains(err.Error(), "prompt is required") {
		t.Fatalf("Generate(empty prompt) error = %v, want prompt error", err)
	}

	failureServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	t.Cleanup(failureServer.Close)
	client.config.LLMAPIEndpoint = failureServer.URL
	client.client = failureServer.Client()
	if _, err := client.Generate(ctx, "prompt", "model-a", ""); err == nil || !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("Generate(failure response) error = %v, want bad request", err)
	}
}

func TestLLMClientGenerateSendsOutputSchema(t *testing.T) {
	ctx := context.Background()
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = readRequestBodyForTest(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-1","model":"model-a","status":"completed","output_text":"{\"answer\":\"ok\"}"}`))
	}))
	t.Cleanup(server.Close)

	client := &LLMClient{
		config: &appconfig.Config{
			LLMAPIEndpoint: server.URL,
			LLMModel:       "model-a",
		},
		configDB: newTestConfigStore(t),
		client:   server.Client(),
	}
	schema := `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`
	result, err := client.Generate(ctx, "prompt", "", schema)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if result.Text != `{"answer":"ok"}` {
		t.Fatalf("text = %q, want structured JSON text", result.Text)
	}
	for _, want := range []string{`"text"`, `"format"`, `"type":"json_schema"`, `"name":"agent_compose_llm_output"`, `"strict":true`, `"schema":{"type":"object"`} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("request body %s missing %s", gotBody, want)
		}
	}
	if _, err := client.Generate(ctx, "prompt", "", "{bad json"); err == nil || !strings.Contains(err.Error(), "outputSchema must be valid JSON") {
		t.Fatalf("Generate(invalid schema) error = %v, want schema error", err)
	}
}

func TestLLMClientResolveProtocol(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)
	client := &LLMClient{
		config:   &appconfig.Config{LLMAPIProtocol: llmAPIProtocolChatCompletions, LLMAPIEndpoint: "https://api.example.com"},
		configDB: store,
	}

	if got := client.resolveProtocol(ctx); got != llmAPIProtocolChatCompletions {
		t.Fatalf("resolveProtocol from config = %q, want %q", got, llmAPIProtocolChatCompletions)
	}
	if got := client.resolveEndpoint(ctx); got != "https://api.example.com/v1/chat/completions" {
		t.Fatalf("resolveEndpoint with chat_completions = %q, want chat completions path", got)
	}

	t.Setenv("LLM_API_PROTOCOL", "chat")
	client.config.LLMAPIProtocol = ""
	if got := client.resolveProtocol(ctx); got != llmAPIProtocolChatCompletions {
		t.Fatalf("resolveProtocol alias chat = %q, want %q", got, llmAPIProtocolChatCompletions)
	}

	if _, err := store.ReplaceGlobalEnv(ctx, []SessionEnvVar{
		{Name: "LLM_API_PROTOCOL", Value: llmAPIProtocolChatCompletions, Secret: false},
	}); err != nil {
		t.Fatalf("ReplaceGlobalEnv returned error: %v", err)
	}
	if err := os.Unsetenv("LLM_API_PROTOCOL"); err != nil {
		t.Fatalf("Unsetenv returned error: %v", err)
	}
	if got := client.resolveProtocol(ctx); got != llmAPIProtocolChatCompletions {
		t.Fatalf("resolveProtocol from db env = %q, want %q", got, llmAPIProtocolChatCompletions)
	}

	client.config.LLMAPIProtocol = llmAPIProtocolResponses
	if got := client.resolveProtocol(ctx); got != llmAPIProtocolChatCompletions {
		t.Fatalf("resolveProtocol should prefer db env over config = %q, want %q", got, llmAPIProtocolChatCompletions)
	}
}

func TestLLMClientGenerateUnsupportedProtocol(t *testing.T) {
	ctx := context.Background()
	client := &LLMClient{
		config: &appconfig.Config{
			LLMAPIEndpoint: "https://api.example.com",
			LLMAPIProtocol: "unknown_protocol",
			LLMModel:       "model-a",
		},
		configDB: newTestConfigStore(t),
	}
	_, err := client.Generate(ctx, "prompt", "model-a", "")
	if err == nil || !strings.Contains(err.Error(), `unsupported llm api protocol "unknown_protocol"`) {
		t.Fatalf("Generate(unsupported protocol) error = %v, want unsupported protocol", err)
	}
}

func TestLLMClientGenerateChatCompletionsPlainText(t *testing.T) {
	ctx := context.Background()
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = readRequestBodyForTest(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-2","model":"compatible-model","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"},{"message":{"role":"assistant","content":" world"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)

	client := &LLMClient{
		config: &appconfig.Config{
			LLMAPIEndpoint: server.URL,
			LLMAPIProtocol: llmAPIProtocolChatCompletions,
			LLMModel:       "compatible-model",
		},
		configDB: newTestConfigStore(t),
		client:   server.Client(),
	}
	result, err := client.Generate(ctx, "prompt", "", "")
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if result.Text != "hello\nworld" || result.Model != "compatible-model" || result.ResponseID != "chatcmpl-2" || result.FinishReason != "stop" {
		t.Fatalf("unexpected plain text chat completions result: %+v", result)
	}
	if strings.Contains(gotBody, `"response_format"`) {
		t.Fatalf("plain text request should not set response_format: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"messages":[{"role":"user","content":"prompt"}`) {
		t.Fatalf("request body missing user prompt: %s", gotBody)
	}
}

func TestLLMClientGenerateChatCompletions(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)
	if _, err := store.ReplaceGlobalEnv(ctx, []SessionEnvVar{
		{Name: "LLM_API_KEY", Value: "chat-key", Secret: true},
	}); err != nil {
		t.Fatalf("ReplaceGlobalEnv returned error: %v", err)
	}

	var gotAuth string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBody = readRequestBodyForTest(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"compatible-model","choices":[{"message":{"role":"assistant","content":"{\"answer\":\"ok\"}"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)

	client := &LLMClient{
		config: &appconfig.Config{
			LLMAPIEndpoint: server.URL,
			LLMAPIProtocol: llmAPIProtocolChatCompletions,
			LLMModel:       "compatible-model",
		},
		configDB: store,
		client:   server.Client(),
	}
	schema := `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`
	result, err := client.Generate(ctx, "prompt", "", schema)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if result.Text != `{"answer":"ok"}` || result.Model != "compatible-model" || result.ResponseID != "chatcmpl-1" || result.FinishReason != "stop" {
		t.Fatalf("unexpected chat completions result: %+v", result)
	}
	if gotAuth != "Bearer chat-key" {
		t.Fatalf("authorization header = %q, want %q", gotAuth, "Bearer chat-key")
	}
	for _, want := range []string{`"model":"compatible-model"`, `"messages":[{"role":"system"`, `"content":"prompt"`, `"response_format":{"type":"json_object"}`, "JSON Schema"} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("request body %s missing %s", gotBody, want)
		}
	}
}

func TestLLMClientGenerateChatCompletionsValidatesSchemaJSONMode(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"compatible-model","choices":[{"message":{"role":"assistant","content":"not json"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)

	client := &LLMClient{
		config: &appconfig.Config{
			LLMAPIEndpoint: server.URL,
			LLMAPIProtocol: llmAPIProtocolChatCompletions,
			LLMModel:       "compatible-model",
		},
		configDB: newTestConfigStore(t),
		client:   server.Client(),
	}
	_, err := client.Generate(ctx, "prompt", "", `{"type":"object"}`)
	if err == nil || !strings.Contains(err.Error(), "did not contain valid JSON") {
		t.Fatalf("Generate(non-json schema response) error = %v, want valid JSON error", err)
	}
}

func TestLLMClientGenerateChatCompletionsSurfacesErrors(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	t.Cleanup(server.Close)

	client := &LLMClient{
		config: &appconfig.Config{
			LLMAPIEndpoint: server.URL,
			LLMAPIProtocol: llmAPIProtocolChatCompletions,
			LLMModel:       "compatible-model",
		},
		configDB: newTestConfigStore(t),
		client:   server.Client(),
	}
	_, err := client.Generate(ctx, "prompt", "", "")
	if err == nil || !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("Generate(chat error response) error = %v, want invalid api key", err)
	}
}

func TestLLMClientResolveSettingPrefersGlobalEnvThenProcessEnvThenConfig(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)
	client := &LLMClient{
		config:   &appconfig.Config{LLMModel: "config-model"},
		configDB: store,
	}

	t.Setenv("LLM_MODEL", "env-model")
	if got := client.resolveSetting(ctx, client.config.LLMModel, "LLM_MODEL"); got != "env-model" {
		t.Fatalf("resolveSetting from process env = %q, want %q", got, "env-model")
	}

	if _, err := store.ReplaceGlobalEnv(ctx, []SessionEnvVar{
		{Name: "LLM_MODEL", Value: "db-model", Secret: false},
	}); err != nil {
		t.Fatalf("ReplaceGlobalEnv returned error: %v", err)
	}
	if got := client.resolveSetting(ctx, client.config.LLMModel, "LLM_MODEL"); got != "db-model" {
		t.Fatalf("resolveSetting from db env = %q, want %q", got, "db-model")
	}
}

func TestLLMClientGenerateChatCompletionsRejectsInvalidSchema(t *testing.T) {
	ctx := context.Background()
	client := &LLMClient{
		config: &appconfig.Config{
			LLMAPIEndpoint: "https://api.example.com",
			LLMAPIProtocol: llmAPIProtocolChatCompletions,
			LLMModel:       "compatible-model",
		},
		configDB: newTestConfigStore(t),
	}
	_, err := client.Generate(ctx, "prompt", "", "{bad json")
	if err == nil || !strings.Contains(err.Error(), "outputSchema must be valid JSON") {
		t.Fatalf("Generate(invalid schema) error = %v, want schema error", err)
	}
}

func TestLoaderRunHostLLMChatCompletionsProtocol(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)

	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = readRequestBodyForTest(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-loader","model":"model-a","choices":[{"message":{"role":"assistant","content":"loader llm text"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)

	manager := &LoaderManager{
		config:   &appconfig.Config{DataRoot: t.TempDir()},
		configDB: store,
		llm: &LLMClient{
			config: &appconfig.Config{
				LLMAPIEndpoint: server.URL,
				LLMAPIProtocol: llmAPIProtocolChatCompletions,
				LLMModel:       "model-a",
			},
			configDB: store,
			client:   server.Client(),
		},
	}
	host := &loaderRunHost{
		manager: manager,
		loader: Loader{Summary: LoaderSummary{ID: "loader-1"}},
		run:     &LoaderRunSummary{ID: "run-1", LoaderID: "loader-1"},
	}

	result, err := host.LLM(ctx, "summarize lifecycle", LoaderLLMRequest{Model: "model-a"})
	if err != nil {
		t.Fatalf("LLM returned error: %v", err)
	}
	if result.Text != "loader llm text" || result.ResponseID != "chatcmpl-loader" || result.FinishReason != "stop" {
		t.Fatalf("unexpected loader llm result: %+v", result)
	}
	if !strings.Contains(gotBody, `"messages":[{"role":"user","content":"summarize lifecycle"}`) {
		t.Fatalf("expected chat completions request body, got %s", gotBody)
	}
}

func readRequestBodyForTest(t *testing.T, r *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("ReadAll request body returned error: %v", err)
	}
	return string(body)
}
