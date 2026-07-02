package agentcompose

import (
	"context"

	"github.com/labstack/echo/v4"

	"agent-compose/internal/agentcompose/transport/httpapi"
)

func registerProxyRoutes(app *echo.Echo, service *Service) {
	httpapi.RegisterProxyRoutes(app, httpapi.ProxyRoutes{
		BasePath: service.config.JupyterProxyBasePath,
		EnsureReady: func(ctx context.Context, sessionID string) (httpapi.ProxyState, error) {
			_, proxyState, err := service.ensureSessionProxyReady(ctx, sessionID)
			return toHTTPProxyState(proxyState), err
		},
		GetState: func(sessionID string) (httpapi.ProxyState, error) {
			proxyState, err := service.store.GetProxyState(sessionID)
			return toHTTPProxyState(proxyState), err
		},
	})
}

func toHTTPProxyState(state ProxyState) httpapi.ProxyState {
	return httpapi.ProxyState{
		ProxyPath:  state.ProxyPath,
		GuestHost:  state.GuestHost,
		HostPort:   state.HostPort,
		GuestPort:  state.GuestPort,
		JupyterURL: state.JupyterURL,
		Token:      state.Token,
	}
}
