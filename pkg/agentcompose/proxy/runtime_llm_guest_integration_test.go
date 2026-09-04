package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// TestIntegrationRuntimeLLMFacadeAcceptsGuestInjectedCredentials verifies the
// host side of the runtime.llm sandbox contract: a guest that received the
// managed facade environment (base URL + token) can call the authenticated
// sandbox facade with the credential carried in the x-api-key header, exactly
// as the runtime SDK does, and the request is proxied upstream.
func TestIntegrationRuntimeLLMFacadeAcceptsGuestInjectedCredentials(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("upstream path = %q, want /v1/responses", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_host_guest","model":"gpt","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"host facade reply"}]}]}`)
	}))
	defer upstream.Close()

	e := echo.New()
	RegisterRuntimeLLMFacadeRoutes(e, RuntimeLLMOptions{
		Tokens: fakeRuntimeLLMTokens{token: llms.FacadeToken{
			SandboxID:  "sandbox-1",
			Model:      "gpt",
			ProviderID: "provider-1",
			WireAPI:    llms.APIProtocolResponses,
			ExpiresAt:  time.Now().Add(time.Hour),
		}},
		Sandboxes:     fakeRuntimeLLMSessions{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}}},
		ResolveTarget: fakeRuntimeLLMTargetResolver(upstream.URL + "/v1"),
		Client:        upstream.Client(),
	})

	// Shape the request exactly like the runtime SDK does inside a managed
	// guest: POST <base>/responses with the facade token in x-api-key and an
	// OpenAI Responses payload built from the prompt.
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses", strings.NewReader(`{"model":"gpt","input":"hello from guest"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "injected-facade-token")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("facade status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		ID     string `json:"id"`
		Model  string `json:"model"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode facade response: %v; body = %s", err, rec.Body.String())
	}
	if payload.ID != "resp_host_guest" || payload.Status != "completed" {
		t.Fatalf("unexpected facade response payload: %+v", payload)
	}
}

// TestIntegrationRuntimeLLMFacadeRejectsGuestWithoutToken guards the negative
// half of the contract: the same guest-shaped request without the injected
// credential must be rejected before reaching any upstream.
func TestIntegrationRuntimeLLMFacadeRejectsGuestWithoutToken(t *testing.T) {
	e := echo.New()
	RegisterRuntimeLLMFacadeRoutes(e, RuntimeLLMOptions{
		Tokens:        fakeRuntimeLLMTokens{token: llms.FacadeToken{SandboxID: "sandbox-1", ExpiresAt: time.Now().Add(time.Hour)}},
		Sandboxes:     fakeRuntimeLLMSessions{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}}},
		ResolveTarget: fakeRuntimeLLMTargetResolver("http://127.0.0.1:1"),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses", strings.NewReader(`{"model":"gpt","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("facade status without token = %d, want %d; body = %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}
