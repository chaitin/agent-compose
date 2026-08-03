package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	agentcomposeapp "agent-compose/pkg/agentcompose/app"
	"agent-compose/pkg/config"
	"agent-compose/pkg/controlplane"
	"agent-compose/proto/health/v1/healthv1connect"
)

const bearerScheme = "Bearer "

func newDaemonAuthMiddleware(conf *config.Config) echo.MiddlewareFunc {
	tokenHash := sha256.Sum256([]byte(conf.DaemonAuthToken))
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if conf.DaemonAuthToken == "" || trustedLocalSocketRequest(c.Request()) || daemonAuthExemptRequest(c.Request(), conf.JupyterProxyBasePath) {
				return next(c)
			}
			presented, ok := requestBearerToken(c.Request())
			presentedHash := sha256.Sum256([]byte(presented))
			if !ok || subtle.ConstantTimeCompare(tokenHash[:], presentedHash[:]) != 1 {
				c.Response().Header().Set("WWW-Authenticate", `Bearer realm="agent-compose"`)
				c.Response().Header().Set("Cache-Control", "no-store")
				return echo.NewHTTPError(http.StatusUnauthorized, "daemon authentication required")
			}
			return next(c)
		}
	}
}

func trustedLocalSocketRequest(r *http.Request) bool {
	return controlplane.IsTrustedLocalRequest(r.Context())
}

func requestBearerToken(r *http.Request) (string, bool) {
	values := r.Header.Values(echo.HeaderAuthorization)
	if len(values) != 1 || !strings.HasPrefix(values[0], bearerScheme) {
		return "", false
	}
	token := strings.TrimPrefix(values[0], bearerScheme)
	return token, token != "" && strings.TrimSpace(token) == token && !strings.ContainsAny(token, " \t\r\n")
}

func daemonAuthExemptRequest(r *http.Request, jupyterBasePath string) bool {
	if r == nil || r.URL == nil {
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/"+healthv1connect.HealthServiceName+"/") {
		return true
	}
	if agentcomposeapp.IsRuntimeLLMFacadeRequest(r) {
		return true
	}
	jupyterBasePath = strings.TrimRight(jupyterBasePath, "/")
	if jupyterBasePath != "" && (r.URL.Path == jupyterBasePath || strings.HasPrefix(r.URL.Path, jupyterBasePath+"/")) {
		return true
	}
	return r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/webhooks/")
}
