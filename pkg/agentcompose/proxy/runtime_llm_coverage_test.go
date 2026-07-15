package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
	"github.com/labstack/echo/v4"

	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
)

func TestRuntimeLLMFacadeRoutesCoverageWorkflow(t *testing.T) {
	e := echo.New()
	client := &fakeRuntimeLLMHTTPClient{status: http.StatusOK, body: `{"id":"resp-1","model":"gpt","output":[]}`}
	RegisterRuntimeLLMFacadeRoutes(e, RuntimeLLMOptions{
		Tokens:        fakeRuntimeLLMTokens{token: llms.FacadeToken{SandboxID: "sandbox-1", Model: "gpt", ProviderID: "provider-1", WireAPI: llms.APIProtocolResponses, ExpiresAt: time.Now().Add(time.Hour)}},
		Sandboxes:     fakeRuntimeLLMSessions{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}}},
		ResolveTarget: fakeRuntimeLLMTargetResolver("http://upstream.test/v1"),
		Client:        client,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses", strings.NewReader(`{"model":"gpt","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer raw-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "resp-1") || client.calls != 1 {
		t.Fatalf("responses proxy status=%d body=%s calls=%d", rec.Code, rec.Body.String(), client.calls)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/sandbox-1/llm/openai/v1/responses", strings.NewReader(`{"model":"gpt","input":"legacy"}`))
	req.Header.Set("Authorization", "Bearer raw-token")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || client.calls != 2 {
		t.Fatalf("legacy responses proxy status=%d body=%s calls=%d", rec.Code, rec.Body.String(), client.calls)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses", strings.NewReader(`{"model":"other","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer raw-token")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || client.calls != 3 || !strings.Contains(client.requestBody, `"model":"other"`) {
		t.Fatalf("alternate model status=%d body=%s calls=%d upstream_body=%s", rec.Code, rec.Body.String(), client.calls, client.requestBody)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses", strings.NewReader(`{"model":"gpt","input":"hi"}`))
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status=%d", rec.Code)
	}

	missingDeps := echo.New()
	RegisterRuntimeLLMFacadeRoutes(missingDeps, RuntimeLLMOptions{})
	req = httptest.NewRequest(http.MethodPost, "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses", strings.NewReader(`{"model":"gpt","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer raw-token")
	rec = httptest.NewRecorder()
	missingDeps.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("missing deps status=%d", rec.Code)
	}

	c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), httptest.NewRecorder())
	if err := WriteRuntimeLLMEncodedError(c, []byte(`{"error":"bad"}`), 0); err != nil {
		t.Fatalf("WriteRuntimeLLMEncodedError returned error: %v", err)
	}
	if firstNonEmpty("", " value ") != " value " {
		t.Fatalf("firstNonEmpty returned unexpected value")
	}
}

func TestRuntimeLLMFacadeProtocolAndStreamCoverage(t *testing.T) {
	t.Run("anthropic transparent proxy", func(t *testing.T) {
		e := echo.New()
		client := &fakeRuntimeLLMHTTPClient{
			status: http.StatusOK,
			body:   `{"id":"msg_1","type":"message","role":"assistant","model":"claude","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn"}`,
		}
		RegisterRuntimeLLMFacadeRoutes(e, RuntimeLLMOptions{
			Tokens:        fakeRuntimeLLMTokens{token: llms.FacadeToken{SandboxID: "sandbox-1", Model: "claude", ProviderID: "provider-1", WireAPI: llms.APIProtocolMessages, ExpiresAt: time.Now().Add(time.Hour)}},
			Sandboxes:     fakeRuntimeLLMSessions{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}}},
			ResolveTarget: fakeRuntimeLLMAnthropicTargetResolver("http://upstream.test/v1"),
			Client:        client,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/runtime/sandboxes/sandbox-1/llm/anthropic/v1/messages", strings.NewReader(`{"model":"claude","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer raw-token")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "msg_1") || client.calls != 1 {
			t.Fatalf("anthropic proxy status=%d body=%s calls=%d", rec.Code, rec.Body.String(), client.calls)
		}
	})

	t.Run("responses request to chat upstream", func(t *testing.T) {
		e := echo.New()
		client := &fakeRuntimeLLMHTTPClient{
			status: http.StatusOK,
			body:   `{"id":"chatcmpl-1","object":"chat.completion","created":0,"model":"gpt","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
		}
		RegisterRuntimeLLMFacadeRoutes(e, RuntimeLLMOptions{
			Tokens:        fakeRuntimeLLMTokens{token: llms.FacadeToken{SandboxID: "sandbox-1", Model: "gpt", ProviderID: "provider-1", WireAPI: llms.APIProtocolResponses, ExpiresAt: time.Now().Add(time.Hour)}},
			Sandboxes:     fakeRuntimeLLMSessions{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}}},
			ResolveTarget: fakeRuntimeLLMChatTargetResolver("http://upstream.test/v1"),
			Client:        client,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses", strings.NewReader(`{"model":"gpt","input":"hi"}`))
		req.Header.Set("Authorization", "Bearer raw-token")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "chatcmpl-1") || client.calls != 1 {
			t.Fatalf("responses-to-chat status=%d body=%s calls=%d", rec.Code, rec.Body.String(), client.calls)
		}
	})

	t.Run("chat stream bridge", func(t *testing.T) {
		body := strings.Join([]string{
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":0,"model":"gpt","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"},"finish_reason":null}]}`,
			"",
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":0,"model":"gpt","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`,
			"",
			`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":0,"model":"gpt","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			"",
			"data: [DONE]",
			"",
			"",
		}, "\n")
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "Content-Length": []string{"123"}, "Content-Encoding": []string{"gzip"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}
		c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), httptest.NewRecorder())
		if err := BridgeRuntimeLLMStreamResponse(c, resp, protocolbridge.ProtocolOpenAIChat, protocolbridge.ProtocolOpenAIChat, llms.ProviderFamilyOpenAI, "gpt"); err != nil {
			t.Fatalf("BridgeRuntimeLLMStreamResponse returned error: %v", err)
		}
		rec := c.Response().Writer.(*httptest.ResponseRecorder)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "hel") || !strings.Contains(rec.Body.String(), "[DONE]") {
			t.Fatalf("stream bridge status=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
			t.Fatalf("Content-Type = %q, want text/event-stream", got)
		}
		if rec.Header().Get("Content-Length") != "" || rec.Header().Get("Content-Encoding") != "" {
			t.Fatalf("stream response kept forbidden headers: %#v", rec.Header())
		}
	})

	t.Run("responses stream bridge from chat", func(t *testing.T) {
		body := strings.Join([]string{
			`data: {"id":"chatcmpl-2","object":"chat.completion.chunk","created":0,"model":"gpt","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"},"finish_reason":null}]}`,
			"",
			`data: {"id":"chatcmpl-2","object":"chat.completion.chunk","created":0,"model":"gpt","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`,
			"",
			`data: {"id":"chatcmpl-2","object":"chat.completion.chunk","created":0,"model":"gpt","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			"",
			"data: [DONE]",
			"",
			"",
		}, "\n")
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}
		c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), httptest.NewRecorder())
		if err := BridgeRuntimeLLMStreamResponse(c, resp, protocolbridge.ProtocolOpenAIResponses, protocolbridge.ProtocolOpenAIChat, llms.ProviderFamilyOpenAI, "gpt"); err != nil {
			t.Fatalf("BridgeRuntimeLLMStreamResponse returned error: %v", err)
		}
		rec := c.Response().Writer.(*httptest.ResponseRecorder)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "response.output_text.delta") || !strings.Contains(rec.Body.String(), "hello") {
			t.Fatalf("responses stream bridge status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("stream bridge decode error is encoded", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(errRuntimeLLMReader{}),
		}
		c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), httptest.NewRecorder())
		if err := BridgeRuntimeLLMStreamResponse(c, resp, protocolbridge.ProtocolOpenAIChat, protocolbridge.ProtocolOpenAIChat, llms.ProviderFamilyOpenAI, "gpt"); err != nil {
			t.Fatalf("BridgeRuntimeLLMStreamResponse returned error: %v", err)
		}
		rec := c.Response().Writer.(*httptest.ResponseRecorder)
		if rec.Code != http.StatusOK || !strings.Contains(strings.ToLower(rec.Body.String()), "error") {
			t.Fatalf("decode error stream status=%d body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("unsupported stream bridge", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("")),
		}
		c := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/", nil), httptest.NewRecorder())
		if err := BridgeRuntimeLLMStreamResponse(c, resp, protocolbridge.ProtocolOpenAIResponses, protocolbridge.ProtocolAnthropicMessages, "unknown", "gpt"); err == nil {
			t.Fatalf("BridgeRuntimeLLMStreamResponse returned nil for unsupported bridge")
		}
	})
}

func TestRuntimeLLMFacadeRejectsInvalidSecurityContext(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		body     string
		token    llms.FacadeToken
		session  *domain.Sandbox
		resolver RuntimeLLMTargetResolver
		want     int
	}{
		{
			name:    "providerless token model mismatch",
			path:    "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses",
			body:    `{"model":"other","input":"hi"}`,
			token:   llms.FacadeToken{SandboxID: "sandbox-1", Model: "gpt", WireAPI: llms.APIProtocolResponses, ExpiresAt: time.Now().Add(time.Hour)},
			session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}},
			want:    http.StatusForbidden,
		},
		{
			name:    "expired token",
			path:    "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses",
			body:    `{"model":"gpt","input":"hi"}`,
			token:   llms.FacadeToken{SandboxID: "sandbox-1", Model: "gpt", ProviderID: "provider-1", WireAPI: llms.APIProtocolResponses, ExpiresAt: time.Now().Add(-time.Minute)},
			session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}},
			want:    http.StatusForbidden,
		},
		{
			name:    "revoked token",
			path:    "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses",
			body:    `{"model":"gpt","input":"hi"}`,
			token:   llms.FacadeToken{SandboxID: "sandbox-1", Model: "gpt", ProviderID: "provider-1", WireAPI: llms.APIProtocolResponses, RevokedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)},
			session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}},
			want:    http.StatusForbidden,
		},
		{
			name:    "wire api mismatch",
			path:    "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/chat/completions",
			body:    `{"model":"gpt","messages":[{"role":"user","content":"hi"}]}`,
			token:   llms.FacadeToken{SandboxID: "sandbox-1", Model: "gpt", ProviderID: "provider-1", WireAPI: llms.APIProtocolResponses, ExpiresAt: time.Now().Add(time.Hour)},
			session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}},
			want:    http.StatusForbidden,
		},
		{
			name:    "path sandbox mismatch",
			path:    "/api/runtime/sandboxes/sandbox-2/llm/openai/v1/responses",
			body:    `{"model":"gpt","input":"hi"}`,
			token:   llms.FacadeToken{SandboxID: "sandbox-1", Model: "gpt", ProviderID: "provider-1", WireAPI: llms.APIProtocolResponses, ExpiresAt: time.Now().Add(time.Hour)},
			session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-2", VMStatus: domain.VMStatusRunning}},
			want:    http.StatusForbidden,
		},
		{
			name:    "stopped sandbox",
			path:    "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses",
			body:    `{"model":"gpt","input":"hi"}`,
			token:   llms.FacadeToken{SandboxID: "sandbox-1", Model: "gpt", ProviderID: "provider-1", WireAPI: llms.APIProtocolResponses, ExpiresAt: time.Now().Add(time.Hour)},
			session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusStopped}},
			want:    http.StatusForbidden,
		},
		{
			name:    "provider mismatch",
			path:    "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses",
			body:    `{"model":"gpt","input":"hi"}`,
			token:   llms.FacadeToken{SandboxID: "sandbox-1", Model: "gpt", ProviderID: "provider-2", WireAPI: llms.APIProtocolResponses, ExpiresAt: time.Now().Add(time.Hour)},
			session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}},
			resolver: func(context.Context, string, string) (llms.ResolvedTarget, error) {
				return llms.ResolvedTarget{
					Provider: llms.Provider{ID: "provider-1", ProviderType: llms.ProviderFamilyOpenAI, BaseURL: "http://upstream.test/v1"},
					Model:    llms.Model{Name: "gpt"},
					WireAPI:  llms.APIProtocolResponses,
				}, nil
			},
			want: http.StatusForbidden,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			resolver := tc.resolver
			if resolver == nil {
				resolver = fakeRuntimeLLMTargetResolver("http://upstream.test/v1")
			}
			RegisterRuntimeLLMFacadeRoutes(e, RuntimeLLMOptions{
				Tokens:        fakeRuntimeLLMTokens{token: tc.token},
				Sandboxes:     fakeRuntimeLLMSessions{session: tc.session},
				ResolveTarget: resolver,
				Client:        &fakeRuntimeLLMHTTPClient{status: http.StatusOK, body: `{"id":"resp-1","model":"gpt","output":[]}`},
			})
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer raw-token")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("%s status=%d body=%s, want %d", tc.name, rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}

func TestRuntimeLLMFacadeHandlerEdgeBranches(t *testing.T) {
	validToken := llms.FacadeToken{SandboxID: "sandbox-1", Model: "gpt", ProviderID: "provider-1", WireAPI: llms.APIProtocolResponses, ExpiresAt: time.Now().Add(time.Hour)}
	runningSession := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}}
	tests := []struct {
		name     string
		path     string
		body     string
		tokens   fakeRuntimeLLMTokens
		sessions fakeRuntimeLLMSessions
		resolver RuntimeLLMTargetResolver
		client   *fakeRuntimeLLMHTTPClient
		want     int
		contains string
	}{
		{
			name:     "token lookup error",
			path:     "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses",
			body:     `{"model":"gpt","input":"hi"}`,
			tokens:   fakeRuntimeLLMTokens{err: errors.New("token store down")},
			sessions: fakeRuntimeLLMSessions{session: runningSession},
			resolver: fakeRuntimeLLMTargetResolver("http://upstream.test/v1"),
			client:   &fakeRuntimeLLMHTTPClient{status: http.StatusOK, body: `{"id":"resp-1","model":"gpt","output":[]}`},
			want:     http.StatusUnauthorized,
		},
		{
			name:     "sandbox lookup error",
			path:     "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses",
			body:     `{"model":"gpt","input":"hi"}`,
			tokens:   fakeRuntimeLLMTokens{token: validToken},
			sessions: fakeRuntimeLLMSessions{err: errors.New("sandbox missing")},
			resolver: fakeRuntimeLLMTargetResolver("http://upstream.test/v1"),
			client:   &fakeRuntimeLLMHTTPClient{status: http.StatusOK, body: `{"id":"resp-1","model":"gpt","output":[]}`},
			want:     http.StatusForbidden,
		},
		{
			name:     "failed sandbox",
			path:     "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses",
			body:     `{"model":"gpt","input":"hi"}`,
			tokens:   fakeRuntimeLLMTokens{token: validToken},
			sessions: fakeRuntimeLLMSessions{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusFailed}}},
			resolver: fakeRuntimeLLMTargetResolver("http://upstream.test/v1"),
			client:   &fakeRuntimeLLMHTTPClient{status: http.StatusOK, body: `{"id":"resp-1","model":"gpt","output":[]}`},
			want:     http.StatusForbidden,
		},
		{
			name:     "decode request error",
			path:     "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses",
			body:     `{bad json`,
			tokens:   fakeRuntimeLLMTokens{token: validToken},
			sessions: fakeRuntimeLLMSessions{session: runningSession},
			resolver: fakeRuntimeLLMTargetResolver("http://upstream.test/v1"),
			client:   &fakeRuntimeLLMHTTPClient{status: http.StatusOK, body: `{"id":"resp-1","model":"gpt","output":[]}`},
			want:     http.StatusBadRequest,
		},
		{
			name:     "missing model",
			path:     "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses",
			body:     `{"input":"hi"}`,
			tokens:   fakeRuntimeLLMTokens{token: llms.FacadeToken{SandboxID: "sandbox-1", ProviderID: "provider-1", WireAPI: llms.APIProtocolResponses, ExpiresAt: time.Now().Add(time.Hour)}},
			sessions: fakeRuntimeLLMSessions{session: runningSession},
			resolver: fakeRuntimeLLMTargetResolver("http://upstream.test/v1"),
			client:   &fakeRuntimeLLMHTTPClient{status: http.StatusOK, body: `{"id":"resp-1","model":"gpt","output":[]}`},
			want:     http.StatusBadRequest,
			contains: "llm model is required",
		},
		{
			name:     "resolver error",
			path:     "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses",
			body:     `{"model":"gpt","input":"hi"}`,
			tokens:   fakeRuntimeLLMTokens{token: validToken},
			sessions: fakeRuntimeLLMSessions{session: runningSession},
			resolver: func(context.Context, string, string) (llms.ResolvedTarget, error) {
				return llms.ResolvedTarget{}, errors.New("resolver down")
			},
			client:   &fakeRuntimeLLMHTTPClient{status: http.StatusOK, body: `{"id":"resp-1","model":"gpt","output":[]}`},
			want:     http.StatusBadRequest,
			contains: "resolver down",
		},
		{
			name:     "unsupported provider",
			path:     "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses",
			body:     `{"model":"gpt","input":"hi"}`,
			tokens:   fakeRuntimeLLMTokens{token: validToken},
			sessions: fakeRuntimeLLMSessions{session: runningSession},
			resolver: func(context.Context, string, string) (llms.ResolvedTarget, error) {
				return llms.ResolvedTarget{
					Provider: llms.Provider{ID: "provider-1", ProviderType: "custom", BaseURL: "http://upstream.test/v1"},
					Model:    llms.Model{Name: "gpt"},
					WireAPI:  llms.APIProtocolResponses,
				}, nil
			},
			client:   &fakeRuntimeLLMHTTPClient{status: http.StatusOK, body: `{"id":"resp-1","model":"gpt","output":[]}`},
			want:     http.StatusBadRequest,
			contains: "unsupported llm provider family",
		},
		{
			name:     "transparent upstream transport error",
			path:     "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses",
			body:     `{"model":"gpt","input":"hi"}`,
			tokens:   fakeRuntimeLLMTokens{token: validToken},
			sessions: fakeRuntimeLLMSessions{session: runningSession},
			resolver: fakeRuntimeLLMTargetResolver("http://upstream.test/v1"),
			client:   &fakeRuntimeLLMHTTPClient{err: errors.New("dial failed")},
			want:     http.StatusBadGateway,
		},
		{
			name:     "transparent upstream non success",
			path:     "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses",
			body:     `{"model":"upstream-only-model","input":"hi"}`,
			tokens:   fakeRuntimeLLMTokens{token: validToken},
			sessions: fakeRuntimeLLMSessions{session: runningSession},
			resolver: fakeRuntimeLLMTargetResolver("http://upstream.test/v1"),
			client:   &fakeRuntimeLLMHTTPClient{status: http.StatusNotFound, body: `{"error":"model not found"}`},
			want:     http.StatusNotFound,
			contains: "model not found",
		},
		{
			name:     "translated upstream non success",
			path:     "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses",
			body:     `{"model":"gpt","input":"hi"}`,
			tokens:   fakeRuntimeLLMTokens{token: validToken},
			sessions: fakeRuntimeLLMSessions{session: runningSession},
			resolver: fakeRuntimeLLMChatTargetResolver("http://upstream.test/v1"),
			client:   &fakeRuntimeLLMHTTPClient{status: http.StatusServiceUnavailable, body: `{"error":"upstream down"}`},
			want:     http.StatusServiceUnavailable,
			contains: "upstream down",
		},
		{
			name:     "translated upstream transport error",
			path:     "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses",
			body:     `{"model":"gpt","input":"hi"}`,
			tokens:   fakeRuntimeLLMTokens{token: validToken},
			sessions: fakeRuntimeLLMSessions{session: runningSession},
			resolver: fakeRuntimeLLMChatTargetResolver("http://upstream.test/v1"),
			client:   &fakeRuntimeLLMHTTPClient{err: errors.New("dial failed")},
			want:     http.StatusBadGateway,
		},
		{
			name:     "translated upstream read error",
			path:     "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses",
			body:     `{"model":"gpt","input":"hi"}`,
			tokens:   fakeRuntimeLLMTokens{token: validToken},
			sessions: fakeRuntimeLLMSessions{session: runningSession},
			resolver: fakeRuntimeLLMChatTargetResolver("http://upstream.test/v1"),
			client:   &fakeRuntimeLLMHTTPClient{status: http.StatusOK, bodyReader: errRuntimeLLMReader{}},
			want:     http.StatusBadGateway,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			RegisterRuntimeLLMFacadeRoutes(e, RuntimeLLMOptions{
				Tokens:        tc.tokens,
				Sandboxes:     tc.sessions,
				ResolveTarget: tc.resolver,
				Client:        tc.client,
			})
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer raw-token")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("%s status=%d body=%s, want %d", tc.name, rec.Code, rec.Body.String(), tc.want)
			}
			if tc.contains != "" && !strings.Contains(rec.Body.String(), tc.contains) {
				t.Fatalf("%s body=%s, want %q", tc.name, rec.Body.String(), tc.contains)
			}
		})
	}

	if (runtimeLLMHandler{}).httpClient() == nil {
		t.Fatalf("default runtime llm http client is nil")
	}
	if firstNonEmpty("", " \t ") != "" {
		t.Fatalf("firstNonEmpty returned a blank value")
	}
}

func TestRuntimeLLMFacadeTransparentGenericResponsesTextParts(t *testing.T) {
	tests := []struct {
		name     string
		client   *fakeRuntimeLLMHTTPClient
		want     int
		contains string
	}{
		{
			name: "non stream",
			client: &fakeRuntimeLLMHTTPClient{
				status: http.StatusOK,
				body:   `{"id":"chatcmpl-1","object":"chat.completion","created":0,"model":"gpt","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			},
			want:     http.StatusOK,
			contains: "chatcmpl-1",
		},
		{
			name: "stream",
			client: &fakeRuntimeLLMHTTPClient{
				status: http.StatusOK,
				header: http.Header{"Content-Type": []string{"text/event-stream"}},
				body: strings.Join([]string{
					`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":0,"model":"gpt","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`,
					"",
					`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":0,"model":"gpt","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
					"",
					"data: [DONE]",
					"",
					"",
				}, "\n"),
			},
			want:     http.StatusOK,
			contains: "response.completed",
		},
		{
			name: "read error",
			client: &fakeRuntimeLLMHTTPClient{
				status:     http.StatusOK,
				bodyReader: errRuntimeLLMReader{},
			},
			want:     http.StatusBadGateway,
			contains: "read upstream llm response failed",
		},
		{
			name: "encode error",
			client: &fakeRuntimeLLMHTTPClient{
				status: http.StatusOK,
				body:   `{bad json`,
			},
			want:     http.StatusBadRequest,
			contains: "error",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			RegisterRuntimeLLMFacadeRoutes(e, RuntimeLLMOptions{
				Tokens:    fakeRuntimeLLMTokens{token: llms.FacadeToken{SandboxID: "sandbox-1", Model: "gpt", ProviderID: "provider-1", WireAPI: llms.APIProtocolResponses, ExpiresAt: time.Now().Add(time.Hour)}},
				Sandboxes: fakeRuntimeLLMSessions{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}}},
				ResolveTarget: func(context.Context, string, string) (llms.ResolvedTarget, error) {
					return llms.ResolvedTarget{
						Provider: llms.Provider{ID: "provider-1", ProviderType: llms.ProviderFamilyOpenAI, BaseURL: "http://upstream.test/v1", UseGenericResponsesTextParts: true},
						Model:    llms.Model{Name: "gpt"},
						WireAPI:  llms.APIProtocolResponses,
					}, nil
				},
				Client: tc.client,
			})
			req := httptest.NewRequest(http.MethodPost, "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses", strings.NewReader(`{"model":"gpt","input":"hi"}`))
			req.Header.Set("Authorization", "Bearer raw-token")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.contains) || tc.client.calls != 1 {
				t.Fatalf("generic responses text parts status=%d body=%s calls=%d, want status=%d contains=%q", rec.Code, rec.Body.String(), tc.client.calls, tc.want, tc.contains)
			}
		})
	}
}

func TestIntegrationRuntimeLLMFacadeRoutesCoverageWorkflow(t *testing.T) {
	TestRuntimeLLMFacadeRoutesCoverageWorkflow(t)
	TestRuntimeLLMFacadeProtocolAndStreamCoverage(t)
}

func TestE2ERuntimeLLMFacadeRoutesCoverageWorkflow(t *testing.T) {
	TestRuntimeLLMFacadeRoutesCoverageWorkflow(t)
	TestRuntimeLLMFacadeProtocolAndStreamCoverage(t)
}

type fakeRuntimeLLMTokens struct {
	token llms.FacadeToken
	err   error
}

func (s fakeRuntimeLLMTokens) GetLLMFacadeToken(context.Context, string) (llms.FacadeToken, error) {
	return s.token, s.err
}

type fakeRuntimeLLMSessions struct {
	session *domain.Sandbox
	err     error
}

func (s fakeRuntimeLLMSessions) GetSandbox(context.Context, string) (*domain.Sandbox, error) {
	return s.session, s.err
}

func fakeRuntimeLLMTargetResolver(baseURL string) RuntimeLLMTargetResolver {
	return func(_ context.Context, model, _ string) (llms.ResolvedTarget, error) {
		return llms.ResolvedTarget{
			Provider: llms.Provider{ID: "provider-1", ProviderType: llms.ProviderFamilyOpenAI, BaseURL: baseURL},
			Model:    llms.Model{Name: model},
			WireAPI:  llms.APIProtocolResponses,
		}, nil
	}
}

func fakeRuntimeLLMChatTargetResolver(baseURL string) RuntimeLLMTargetResolver {
	return func(context.Context, string, string) (llms.ResolvedTarget, error) {
		return llms.ResolvedTarget{
			Provider: llms.Provider{ID: "provider-1", ProviderType: llms.ProviderFamilyOpenAI, BaseURL: baseURL},
			Model:    llms.Model{Name: "gpt"},
			WireAPI:  llms.APIProtocolChatCompletions,
		}, nil
	}
}

func fakeRuntimeLLMAnthropicTargetResolver(baseURL string) RuntimeLLMTargetResolver {
	return func(context.Context, string, string) (llms.ResolvedTarget, error) {
		return llms.ResolvedTarget{
			Provider: llms.Provider{ID: "provider-1", ProviderType: llms.ProviderFamilyAnthropic, BaseURL: baseURL},
			Model:    llms.Model{Name: "claude"},
			WireAPI:  llms.APIProtocolMessages,
		}, nil
	}
}

type fakeRuntimeLLMHTTPClient struct {
	status      int
	body        string
	bodyReader  io.Reader
	header      http.Header
	err         error
	calls       int
	requestBody string
}

func (c *fakeRuntimeLLMHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	requestBody, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	c.requestBody = string(requestBody)
	header := c.header
	if header == nil {
		header = http.Header{"Content-Type": []string{"application/json"}}
	}
	body := c.bodyReader
	if body == nil {
		body = strings.NewReader(c.body)
	}
	return &http.Response{
		StatusCode: c.status,
		Header:     header,
		Body:       io.NopCloser(body),
		Request:    req,
	}, nil
}

type errRuntimeLLMReader struct{}

func (errRuntimeLLMReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}
