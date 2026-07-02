package agentcompose

import (
	"github.com/labstack/echo/v4"

	"agent-compose/pkg/sessions"
)

func registerProxyRoutes(app *echo.Echo, service *Service) {
	sessions.RegisterProxyRoutes(app, service.config, service.store, service.sessions)
}
