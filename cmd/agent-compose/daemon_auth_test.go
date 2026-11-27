package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/chaitin/agent-compose/pkg/config"
)

func TestDaemonAuthMiddleware(t *testing.T) {
	tests := []struct {
		name       string
		token      string
		header     string
		path       string
		method     string
		trusted    bool
		wantStatus int
	}{
		{name: "disabled", path: "/api/version", method: http.MethodGet, wantStatus: http.StatusNoContent},
		{name: "missing", token: "secret", path: "/api/version", method: http.MethodGet, wantStatus: http.StatusUnauthorized},
		{name: "wrong", token: "secret", header: "Bearer wrong", path: "/api/version", method: http.MethodGet, wantStatus: http.StatusUnauthorized},
		{name: "valid", token: "secret", header: "Bearer secret", path: "/api/version", method: http.MethodGet, wantStatus: http.StatusNoContent},
		{name: "trusted socket", token: "secret", path: "/api/version", method: http.MethodGet, trusted: true, wantStatus: http.StatusNoContent},
		{name: "health exempt", token: "secret", path: "/health.v1.HealthService/Status", method: http.MethodPost, wantStatus: http.StatusNoContent},
		{name: "runtime llm exempt", token: "secret", path: "/api/runtime/sandboxes/s-1/llm/openai/v1/responses", method: http.MethodPost, wantStatus: http.StatusNoContent},
		{name: "jupyter exempt", token: "secret", path: "/jupyter/s-1/lab", method: http.MethodGet, wantStatus: http.StatusNoContent},
		{name: "webhook ingestion exempt", token: "secret", path: "/api/webhooks/webhook.github.push", method: http.MethodPost, wantStatus: http.StatusNoContent},
		{name: "webhook management protected", token: "secret", path: "/api/webhook-sources", method: http.MethodGet, wantStatus: http.StatusUnauthorized},
		{name: "event stream protected", token: "secret", path: "/agentcompose.v2.EventService/WatchEvents", method: http.MethodPost, wantStatus: http.StatusUnauthorized},
		{name: "event stream capabilities protected", token: "secret", path: "/agentcompose.v2.EventService/GetEventStreamCapabilities", method: http.MethodPost, wantStatus: http.StatusUnauthorized},
		{name: "event stream authenticated", token: "secret", header: "Bearer secret", path: "/agentcompose.v2.EventService/WatchEvents", method: http.MethodPost, wantStatus: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := echo.New()
			app.Use(newDaemonAuthMiddleware(&config.Config{DaemonAuthToken: test.token, JupyterProxyBasePath: "/jupyter"}))
			app.Any("/*", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })
			req := httptest.NewRequest(test.method, test.path, nil)
			if test.header != "" {
				req.Header.Set(echo.HeaderAuthorization, test.header)
			}
			if test.trusted {
				req = req.WithContext(context.WithValue(req.Context(), localUnixSocketRequestKey{}, true))
			}
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, test.wantStatus, rec.Body.String())
			}
			if test.wantStatus == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("WWW-Authenticate header is empty")
			}
		})
	}
}
