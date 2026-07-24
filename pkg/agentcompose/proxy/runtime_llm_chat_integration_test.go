package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
)

func TestIntegrationRuntimeLLMFacadeUsesChatOnlyUpstream(t *testing.T) {
	tests := []struct {
		name       string
		wireAPI    string
		facadePath string
		body       string
	}{
		{
			name:       "Codex and OpenCode responses ingress",
			wireAPI:    llms.APIProtocolResponses,
			facadePath: "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses",
			body:       `{"model":"gpt","input":"hi"}`,
		},
		{
			name:       "OpenCode compatible chat ingress",
			wireAPI:    llms.APIProtocolChatCompletions,
			facadePath: "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/chat/completions",
			body:       `{"model":"gpt","messages":[{"role":"user","content":"hi"}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upstream, upstreamPaths := newChatOnlyUpstream(t)

			e := echo.New()
			RegisterRuntimeLLMFacadeRoutes(e, RuntimeLLMOptions{
				Tokens: fakeRuntimeLLMTokens{token: llms.FacadeToken{
					SandboxID:  "sandbox-1",
					Model:      "gpt",
					ProviderID: "provider-1",
					WireAPI:    tc.wireAPI,
					ExpiresAt:  time.Now().Add(time.Hour),
				}},
				Sandboxes: fakeRuntimeLLMSessions{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}}},
				ResolveTarget: func(context.Context, string, string) (llms.ResolvedTarget, error) {
					return llms.ResolvedTarget{
						Provider: llms.Provider{ID: "provider-1", ProviderType: llms.ProviderFamilyOpenAI, BaseURL: upstream.URL + "/v1"},
						Model:    llms.Model{Name: "gpt"},
						WireAPI:  llms.APIProtocolChatCompletions,
					}, nil
				},
				Client: upstream.Client(),
			})
			req := httptest.NewRequest(http.MethodPost, tc.facadePath, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer facade-token")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("facade status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if upstreamPath := <-upstreamPaths; upstreamPath != "/v1/chat/completions" {
				t.Fatalf("upstream path = %q", upstreamPath)
			}
		})
	}
}

func newChatOnlyUpstream(t *testing.T) (*httptest.Server, <-chan string) {
	t.Helper()
	paths := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "chat completions only", http.StatusNotFound)
			return
		}
		requestBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream request: %v", err)
			http.Error(w, "read request", http.StatusBadRequest)
			return
		}
		if !strings.Contains(string(requestBody), `"messages"`) {
			t.Errorf("upstream request is not Chat Completions: %s", requestBody)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"id":"chatcmpl-only","object":"chat.completion","created":0,"model":"gpt","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`); err != nil {
			t.Errorf("write upstream response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server, paths
}
