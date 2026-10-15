package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
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

// grokFunctionCallStream is a streamed Grok Responses reply whose tool-call
// arguments carry 66121.0, split across two deltas as Grok can send it.
func grokFunctionCallStream() string {
	events := []string{
		`{"type":"response.created","response":{"id":"resp_2","object":"response","status":"in_progress","model":"grok-4.6"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","status":"in_progress","name":"exec_command","call_id":"call_1","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"timeout_ms\":66121."}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"0,\"ratio\":1.5}"}`,
		`{"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":"{\"timeout_ms\":66121.0,\"ratio\":1.5}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","status":"completed","name":"exec_command","call_id":"call_1","arguments":"{\"timeout_ms\":66121.0,\"ratio\":1.5}"}}`,
		`{"type":"response.completed","response":{"id":"resp_2","object":"response","status":"completed","model":"grok-4.6","output":[],"usage":{"input_tokens":4,"output_tokens":6,"total_tokens":10}}}`,
	}
	var stream strings.Builder
	for _, event := range events {
		stream.WriteString("data: " + event + "\n\n")
	}
	return stream.String()
}

func TestRuntimeLLMFacadeRewritesGrokIntegralFloatsInABridgedStream(t *testing.T) {
	run := func(model string) string {
		t.Helper()
		client := &fakeRuntimeLLMHTTPClient{
			status:     http.StatusOK,
			header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			bodyReader: iotest.OneByteReader(strings.NewReader(grokFunctionCallStream())),
		}
		rec := serveRuntimeLLMChat(t, client, `{"model":"`+model+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", model, rec.Code, rec.Body.String())
		}
		return streamedChatToolArguments(t, rec.Body.String())
	}
	if got := run("grok-4.6"); got != `{"timeout_ms":66121,"ratio":1.5}` {
		t.Fatalf("grok streamed arguments = %q", got)
	}
	if got := run("gpt-5.5"); got != `{"timeout_ms":66121.0,"ratio":1.5}` {
		t.Fatalf("non-grok streamed arguments = %q", got)
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

func streamedChatToolArguments(t *testing.T, body string) string {
	t.Helper()
	var arguments strings.Builder
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Function struct {
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("chunk %s: %v", data, err)
		}
		for _, choice := range chunk.Choices {
			for _, call := range choice.Delta.ToolCalls {
				arguments.WriteString(call.Function.Arguments)
			}
		}
	}
	return arguments.String()
}

// A connection with generic Responses text parts re-encodes its stream through
// the bridge even when the guest speaks Responses too, so it needs the same
// rewrite as a cross-protocol stream.
func TestRuntimeLLMFacadeRewritesGrokIntegralFloatsForGenericTextParts(t *testing.T) {
	run := func(model string) string {
		t.Helper()
		e := echo.New()
		RegisterRuntimeLLMFacadeRoutes(e, RuntimeLLMOptions{
			Tokens:    fakeRuntimeLLMTokens{token: llms.FacadeToken{SandboxID: "sandbox-1", Model: model, ProviderID: "provider-1", WireAPI: llms.APIProtocolResponses, ExpiresAt: time.Now().Add(time.Hour)}},
			Sandboxes: fakeRuntimeLLMSessions{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}}},
			Connections: func(_ context.Context, _, model string) (llms.ResolvedTarget, error) {
				return llms.ResolvedTarget{
					Provider: llms.Provider{ID: "provider-1", ProviderType: llms.ProviderFamilyOpenAI, BaseURL: "http://upstream.test/v1", UseGenericResponsesTextParts: true},
					Model:    llms.Model{Name: model},
					WireAPI:  llms.APIProtocolResponses,
				}, nil
			},
			Client: &fakeRuntimeLLMHTTPClient{
				status:     http.StatusOK,
				header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				bodyReader: iotest.OneByteReader(strings.NewReader(grokFunctionCallStream())),
			},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses", strings.NewReader(`{"model":"`+model+`","stream":true,"input":"hi"}`))
		req.Header.Set("Authorization", "Bearer raw-token")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", model, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	if got := run("grok-4.6"); strings.Contains(got, "66121.0") || !strings.Contains(got, "66121") || !strings.Contains(got, "1.5") {
		t.Fatalf("grok stream through generic text parts:\n%s", got)
	}
	if got := run("gpt-5.5"); !strings.Contains(got, "66121.0") {
		t.Fatalf("non-grok stream was rewritten:\n%s", got)
	}
}

// A buffered Grok reply on a generic-text-parts connection is re-encoded
// through the bridge too, so its integral floats leak as 66121.0 unless the
// rewrite runs on the Responses body before EncodeRuntimeClientResponse.
func TestRuntimeLLMFacadeRewritesGrokIntegralFloatsForGenericTextPartsBuffered(t *testing.T) {
	upstream := `{"id":"resp_1","object":"response","status":"completed","model":"grok-4.6","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"exec_command","arguments":"{\"timeout_ms\":66121.0,\"ratio\":1.5}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	run := func(model string) string {
		t.Helper()
		e := echo.New()
		RegisterRuntimeLLMFacadeRoutes(e, RuntimeLLMOptions{
			Tokens:    fakeRuntimeLLMTokens{token: llms.FacadeToken{SandboxID: "sandbox-1", Model: model, ProviderID: "provider-1", WireAPI: llms.APIProtocolResponses, ExpiresAt: time.Now().Add(time.Hour)}},
			Sandboxes: fakeRuntimeLLMSessions{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}}},
			Connections: func(_ context.Context, _, model string) (llms.ResolvedTarget, error) {
				return llms.ResolvedTarget{
					Provider: llms.Provider{ID: "provider-1", ProviderType: llms.ProviderFamilyOpenAI, BaseURL: "http://upstream.test/v1", UseGenericResponsesTextParts: true},
					Model:    llms.Model{Name: model},
					WireAPI:  llms.APIProtocolResponses,
				}, nil
			},
			Client: &fakeRuntimeLLMHTTPClient{status: http.StatusOK, body: upstream},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses", strings.NewReader(`{"model":"`+model+`","input":"hi"}`))
		req.Header.Set("Authorization", "Bearer raw-token")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", model, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	if got := run("grok-4.6"); strings.Contains(got, "66121.0") || !strings.Contains(got, "66121") || !strings.Contains(got, "1.5") {
		t.Fatalf("grok buffered body through generic text parts:\n%s", got)
	}
	if got := run("gpt-5.5"); !strings.Contains(got, "66121.0") {
		t.Fatalf("non-grok buffered body was rewritten:\n%s", got)
	}
}

// A Claude client behind a Grok Responses connection reaches it through the
// messages -> responses bridge. The streamed tool input it reads back must carry
// the same whole numbers as a chat or Responses client.
func TestRuntimeLLMFacadeRewritesGrokIntegralFloatsForAMessagesClient(t *testing.T) {
	run := func(model string) string {
		t.Helper()
		e := echo.New()
		RegisterRuntimeLLMFacadeRoutes(e, RuntimeLLMOptions{
			Tokens:      fakeRuntimeLLMTokens{token: llms.FacadeToken{SandboxID: "sandbox-1", Model: model, ProviderID: "provider-1", WireAPI: llms.APIProtocolMessages, ExpiresAt: time.Now().Add(time.Hour)}},
			Sandboxes:   fakeRuntimeLLMSessions{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}}},
			Connections: fakeRuntimeLLMTargetResolver("http://upstream.test/v1"),
			Client: &fakeRuntimeLLMHTTPClient{
				status:     http.StatusOK,
				header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				bodyReader: iotest.OneByteReader(strings.NewReader(grokFunctionCallStream())),
			},
		})
		body := `{"model":"` + model + `","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
		req := httptest.NewRequest(http.MethodPost, "/api/runtime/sandboxes/sandbox-1/llm/anthropic/v1/messages", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer raw-token")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", model, rec.Code, rec.Body.String())
		}
		return streamedAnthropicToolInput(t, rec.Body.String())
	}
	if got := run("grok-4.6"); got != `{"timeout_ms":66121,"ratio":1.5}` {
		t.Fatalf("grok streamed tool input = %q", got)
	}
	if got := run("gpt-5.5"); got != `{"timeout_ms":66121.0,"ratio":1.5}` {
		t.Fatalf("non-grok streamed tool input = %q", got)
	}
}

// streamedAnthropicToolInput joins the input_json_delta fragments of an
// Anthropic messages stream.
func streamedAnthropicToolInput(t *testing.T, body string) string {
	t.Helper()
	var input strings.Builder
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var event struct {
			Type  string `json:"type"`
			Delta struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatalf("event %s: %v", data, err)
		}
		if event.Type == "content_block_delta" && event.Delta.Type == "input_json_delta" {
			input.WriteString(event.Delta.PartialJSON)
		}
	}
	return input.String()
}
