package httpapi

import (
	"github.com/labstack/echo/v4"

	"agent-compose/internal/agentcompose/transport/connectv1"
	"agent-compose/internal/agentcompose/transport/connectv2"
)

type ConnectService interface {
	connectv1.Service
	connectv2.Service
}

func RegisterConnectHandlers(app *echo.Echo, service ConnectService) {
	connectv1.RegisterHandlers(app, service)
	connectv2.RegisterHandlers(app, service)
}
