package loaders

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appconfig "agent-compose/pkg/config"
	llmpkg "agent-compose/pkg/llm"
)

func TestLoaderRunHostLLMChatCompletionsProtocol(t *testing.T) {
	ctx := context.Background()
	store := newLoaderEventTestConfigStore(t)

	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody = readRequestBodyForTest(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-loader","model":"model-a","choices":[{"message":{"role":"assistant","content":"loader llm text"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)

	client := llmpkg.NewClient(&appconfig.Config{
		LLMAPIEndpoint: server.URL,
		LLMAPIProtocol: llmpkg.APIProtocolChatCompletions,
		LLMModel:       "model-a",
	}, store, server.Client())
	manager := newTestLoaderManager(t, ManagerDeps{
		Config:   &appconfig.Config{DataRoot: t.TempDir()},
		ConfigDB: store,
		LLM:      client,
	})
	host := &loaderRunHost{
		manager:      manager,
		loader:       Loader{Summary: LoaderSummary{ID: "loader-1"}},
		run:          &LoaderRunSummary{ID: "run-1", LoaderID: "loader-1"},
		triggerEvent: loaderTriggerEventMetadata{},
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
