package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	agentcomposeapp "agent-compose/pkg/agentcompose/app"
	controlauth "agent-compose/pkg/auth"
	"agent-compose/pkg/config"
	"agent-compose/proto/health/v1/healthv1connect"
)

const bearerScheme = "Bearer "

type daemonAuthenticator interface {
	Initialized() bool
	Authenticate(context.Context, string) (controlauth.Identity, error)
	RegisterRequest(string, context.CancelFunc) func()
}

func newDaemonAuthMiddleware(conf *config.Config, authenticator daemonAuthenticator) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			request := c.Request()
			if daemonAuthExemptRequest(request, conf.JupyterProxyBasePath) {
				return next(c)
			}
			if trustedLocalSocketRequest(request) {
				c.SetRequest(request.WithContext(controlauth.WithIdentity(request.Context(), controlauth.Identity{
					TokenName: "local-admin", Role: controlauth.RoleAdmin, Origin: controlauth.OriginLocal,
				})))
				return next(c)
			}

			values := request.Header.Values(echo.HeaderAuthorization)
			if len(values) == 0 && !authenticator.Initialized() {
				c.SetRequest(request.WithContext(controlauth.WithIdentity(request.Context(), controlauth.Identity{
					TokenName: "anonymous-admin", Role: controlauth.RoleAdmin, Origin: controlauth.OriginAnonymous,
				})))
				return next(c)
			}
			presented, ok := requestBearerToken(request)
			if !ok {
				return daemonUnauthorized(c)
			}
			identity, err := authenticator.Authenticate(request.Context(), presented)
			if err != nil {
				return daemonUnauthorized(c)
			}
			requestCtx, cancel := context.WithCancel(controlauth.WithIdentity(request.Context(), identity))
			unregister := authenticator.RegisterRequest(identity.TokenID, cancel)
			defer func() {
				unregister()
				cancel()
			}()
			c.SetRequest(request.WithContext(requestCtx))
			return next(c)
		}
	}
}

func daemonUnauthorized(c echo.Context) error {
	c.Response().Header().Set("WWW-Authenticate", `Bearer realm="agent-compose"`)
	c.Response().Header().Set("Cache-Control", "no-store")
	return echo.NewHTTPError(http.StatusUnauthorized, "daemon authentication required")
}

func trustedLocalSocketRequest(r *http.Request) bool {
	trusted, _ := r.Context().Value(localUnixSocketRequestKey{}).(bool)
	return trusted
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
