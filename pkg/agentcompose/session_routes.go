package agentcompose

import (
	"time"

	"github.com/labstack/echo/v4"

	"agent-compose/pkg/sessions"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func sessionListOptionsFromProto(req *agentcomposev1.ListSessionsRequest) (SessionListOptions, error) {
	return sessions.SessionListOptionsFromProto(req)
}

func parseOptionalRFC3339(raw, field string) (time.Time, error) {
	return sessions.ParseOptionalRFC3339(raw, field)
}

func registerProxyRoutes(app *echo.Echo, service *Service) {
	sessions.RegisterProxyRoutes(app, service.config, service.store, service.sessions)
}
