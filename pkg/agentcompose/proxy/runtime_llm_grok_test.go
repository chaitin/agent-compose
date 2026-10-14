package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestRuntimeLLMFacadeRewritesGrokIntegralFloats(t *testing.T) {
	upstream := `{"object":"response","output":[{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"go test\",\"timeout_ms\":66121.0,\"yield_time_ms\":2500.0,\"ratio\":1.5}"}]}`
	rec := serveRuntimeLLM(t, "grok-4.7", &fakeRuntimeLLMHTTPClient{status: http.StatusOK, body: upstream})
	got := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(got, "66121.0") || strings.Contains(got, "2500.0") || !strings.Contains(got, "66121") || !strings.Contains(got, "1.5") {
		t.Fatalf("grok response status=%d body=%s", rec.Code, got)
	}

	other := serveRuntimeLLM(t, "gpt-5.5", &fakeRuntimeLLMHTTPClient{status: http.StatusOK, body: upstream})
	if other.Code != http.StatusOK || !strings.Contains(other.Body.String(), "66121.0") {
		t.Fatalf("non-grok response status=%d body=%s", other.Code, other.Body.String())
	}

	errorBody := `{"error":{"message":"upstream rejected timeout_ms 66121.0"}}`
	failed := serveRuntimeLLM(t, "grok-4.7", &fakeRuntimeLLMHTTPClient{
		status: http.StatusBadRequest,
		header: http.Header{"Content-Type": []string{"application/json"}, "Content-Length": []string{"57"}},
		body:   errorBody,
	})
	if failed.Code != http.StatusBadRequest || failed.Body.String() != errorBody {
		t.Fatalf("grok error status=%d body=%s", failed.Code, failed.Body.String())
	}
}

func serveRuntimeLLM(t *testing.T, model string, client *fakeRuntimeLLMHTTPClient) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	RegisterRuntimeLLMFacadeRoutes(e, RuntimeLLMOptions{
		Tokens:      fakeRuntimeLLMTokens{token: llms.FacadeToken{SandboxID: "sandbox-1", Model: model, ProviderID: "provider-1", WireAPI: llms.APIProtocolResponses, ExpiresAt: time.Now().Add(time.Hour)}},
		Sandboxes:   fakeRuntimeLLMSessions{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}}},
		Connections: fakeRuntimeLLMTargetResolver("http://upstream.test/v1"),
		Client:      client,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses", strings.NewReader(`{"model":"`+model+`","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer raw-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// A codex sandbox publishes its Responses-bound token as OPENAI_API_KEY, and a
// second CLI the entrypoint starts posts chat completions with it. The request
// is converted to the Responses connection, and the Grok reply comes back as a
// chat completion with whole numbers in the tool call.
func TestRuntimeLLMFacadeServesGrokChatCompletionsThroughAResponsesToken(t *testing.T) {
	upstream := `{"id":"resp_1","object":"response","status":"completed","model":"grok-4.6","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"exec_command","arguments":"{\"timeout_ms\":66121.0,\"ratio\":1.5}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	client := &fakeRuntimeLLMHTTPClient{status: http.StatusOK, body: upstream}
	rec := serveRuntimeLLMChat(t, client, `{"model":"grok-4.6","messages":[{"role":"user","content":"hi"}]}`)
	got := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, got)
	}
	if !strings.Contains(client.requestBody, `"input"`) || strings.Contains(client.requestBody, `"messages"`) {
		t.Fatalf("upstream was not sent a Responses request: %s", client.requestBody)
	}
	if !strings.Contains(got, `"tool_calls"`) || strings.Contains(got, "66121.0") || !strings.Contains(got, "66121") || !strings.Contains(got, "1.5") {
		t.Fatalf("chat completion body=%s", got)
	}
}

func serveRuntimeLLMChat(t *testing.T, client *fakeRuntimeLLMHTTPClient, body string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	RegisterRuntimeLLMFacadeRoutes(e, RuntimeLLMOptions{
		Tokens:      fakeRuntimeLLMTokens{token: llms.FacadeToken{SandboxID: "sandbox-1", Model: "feature/gpt-5.6-terra", ProviderID: "provider-1", WireAPI: llms.APIProtocolResponses, ExpiresAt: time.Now().Add(time.Hour)}},
		Sandboxes:   fakeRuntimeLLMSessions{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}}},
		Connections: fakeRuntimeLLMTargetResolver("http://upstream.test/v1"),
		Client:      client,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer raw-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}
