package agentcompose

import (
	"github.com/labstack/echo/v4"

	"agent-compose/pkg/events"
)

func registerWebhookRoutes(app *echo.Echo, service *Service) {
	events.RegisterRoutes(app, events.NewService(service.config, service.configDB))
}
