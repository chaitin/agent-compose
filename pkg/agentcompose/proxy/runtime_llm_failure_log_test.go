package proxy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// A 502 reaches the guest as a fixed message, so the log is the only place the
// cause survives. An upstream that fails and a guest that hung up mid-call must
// be told apart there, on both the transparent and the bridged path.
func TestRuntimeLLMFacadeLogsWhyAnUpstreamCallFailed(t *testing.T) {
	routes := []struct {
		name  string
		path  string
		route string
		body  string
	}{
		{"transparent", "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/responses", "/llm/openai/v1/responses", `{"model":"gpt","input":"hi"}`},
		{"bridged", "/api/runtime/sandboxes/sandbox-1/llm/openai/v1/chat/completions", "/llm/openai/v1/chat/completions", `{"model":"gpt","messages":[{"role":"user","content":"hi"}]}`},
	}
	for _, route := range routes {
		t.Run(route.name+"/upstream failed", func(t *testing.T) {
			logs := serveRuntimeLLMFailure(t, route.path, route.body, errors.New("dial tcp 10.0.0.9:443: connect: connection refused"), false)
			for _, want := range []string{"level=WARN", `msg="call upstream llm failed"`, "sandbox_id=sandbox-1", route.route, "connection=provider-1", "model=gpt", "upstream_protocol=openai_responses", "connection refused"} {
				if !strings.Contains(logs, want) {
					t.Fatalf("log missing %q:\n%s", want, logs)
				}
			}
			if strings.Contains(logs, "disconnected") {
				t.Fatalf("an upstream failure was logged as a guest disconnect:\n%s", logs)
			}
		})
		t.Run(route.name+"/guest disconnected", func(t *testing.T) {
			logs := serveRuntimeLLMFailure(t, route.path, route.body, context.Canceled, true)
			if !strings.Contains(logs, "level=INFO") || !strings.Contains(logs, "the guest disconnected") || !strings.Contains(logs, "context canceled") {
				t.Fatalf("guest disconnect log:\n%s", logs)
			}
		})
	}
}

func serveRuntimeLLMFailure(t *testing.T, path, body string, upstreamErr error, cancel bool) string {
	t.Helper()
	var logs bytes.Buffer
	e := echo.New()
	RegisterRuntimeLLMFacadeRoutes(e, RuntimeLLMOptions{
		Tokens:      fakeRuntimeLLMTokens{token: llms.FacadeToken{SandboxID: "sandbox-1", Model: "gpt", ProviderID: "provider-1", WireAPI: llms.APIProtocolResponses, ExpiresAt: time.Now().Add(time.Hour)}},
		Sandboxes:   fakeRuntimeLLMSessions{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}}},
		Connections: fakeRuntimeLLMTargetResolver("http://upstream.test/v1"),
		Client:      &fakeRuntimeLLMHTTPClient{err: upstreamErr},
		Logger:      slog.New(slog.NewTextHandler(&logs, nil)),
	})
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer raw-token")
	req.Header.Set("Content-Type", "application/json")
	if cancel {
		ctx, stop := context.WithCancel(req.Context())
		stop()
		req = req.WithContext(ctx)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	return logs.String()
}
