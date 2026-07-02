package agentcompose

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestRuntimeLLMFacadeWrapperRecognizesRegisteredRoutes(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/session-1/llm/openai/v1/responses", nil)
	if !IsRuntimeLLMFacadeRequest(req) {
		t.Fatalf("IsRuntimeLLMFacadeRequest returned false for runtime facade route")
	}
}

func TestRuntimeLLMFacadeRoutesRegistered(t *testing.T) {
	service, _, _ := newTestServiceAPIHarness(t)
	app := echo.New()
	registerRuntimeLLMFacadeRoutes(app, service)

	for _, route := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/api/runtime/sessions/:session_id/llm/openai/v1/responses"},
		{method: http.MethodPost, path: "/api/runtime/sessions/:session_id/llm/openai/v1/chat/completions"},
		{method: http.MethodPost, path: "/api/runtime/sessions/:session_id/llm/anthropic/v1/messages"},
	} {
		if !hasEchoRoute(app, route.method, route.path) {
			t.Fatalf("%s %s route was not registered", route.method, route.path)
		}
	}
}
