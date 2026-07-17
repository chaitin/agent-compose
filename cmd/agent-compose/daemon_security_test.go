package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	controlauth "agent-compose/pkg/auth"
	"github.com/labstack/echo/v4"
)

func TestRawHTTPPolicyCoversManagementRoutes(t *testing.T) {
	tests := []struct {
		method string
		path   string
		access controlauth.Access
		action string
		apply  bool
	}{
		{http.MethodGet, "/api/webhook-sources", controlauth.AccessRead, "", true},
		{http.MethodGet, "/api/agent-compose/workspaces/ws/files", controlauth.AccessRead, "", true},
		{http.MethodPost, "/api/agent-compose/workspaces/ws/upload", controlauth.AccessOperation, "workspace.upload", true},
		{http.MethodPut, "/api/webhook-sources/github", controlauth.AccessOperation, "webhook-source.update", true},
		{http.MethodDelete, "/api/webhook-sources/github", controlauth.AccessOperation, "webhook-source.delete", true},
		{http.MethodPost, "/api/webhooks/webhook.github.push", "", "", false},
		{http.MethodPost, "/agentcompose.v2.AuthService/CreateToken", "", "", false},
		{http.MethodGet, "/jupyter/sandbox-1", "", "", false},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, nil)
		policy, applies := rawHTTPPolicy(request, "/jupyter")
		if applies != test.apply || policy.access != test.access || policy.action != test.action {
			t.Errorf("%s %s policy = %#v/%v", test.method, test.path, policy, applies)
		}
	}
}

func TestRequestIDMiddlewarePreservesValidAndReplacesInvalidIDs(t *testing.T) {
	for _, test := range []struct{ provided string }{{"request-1"}, {"bad\nrequest"}, {""}} {
		request := httptest.NewRequest(http.MethodGet, "/api/version", nil)
		request.Header.Set("X-Request-ID", test.provided)
		response := httptest.NewRecorder()
		handler := newDaemonRequestIDMiddleware()(func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })
		app := echo.New()
		app.GET("/api/version", handler)
		app.ServeHTTP(response, request)
		got := response.Header().Get("X-Request-ID")
		if got == "" || got == "bad\nrequest" || test.provided == "request-1" && got != test.provided {
			t.Errorf("provided %q produced %q", test.provided, got)
		}
	}
}
