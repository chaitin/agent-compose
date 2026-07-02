package agentcompose

import (
	"github.com/labstack/echo/v4"

	"agent-compose/internal/agentcompose/transport/httpapi"
)

func registerWebhookRoutes(app *echo.Echo, service *Service) {
	receiver := httpapi.WebhookReceiver{
		Store:              service.configDB,
		DefaultBodyLimit:   service.config.WebhookBodyLimitBytes,
		MarshalJSONCompact: marshalJSONCompact,
	}
	management := httpapi.WebhookManagementAPI{Store: service.configDB}
	httpapi.RegisterWebhookRoutes(app, httpapi.WebhookHandlers{
		HandleWebhook:          receiver.HandleWebhook,
		HandleListSources:      management.HandleListSources,
		HandlePutSource:        management.HandlePutSource,
		HandleDeleteSource:     management.HandleDeleteSource,
		HandleListEvents:       management.HandleListEvents,
		HandleGetEventSessions: management.HandleGetEventSessions,
		HandleGetEventRuns:     management.HandleGetEventRuns,
		HandleGetEvent:         management.HandleGetEvent,
	})
}

func webhookTokenHash(token string) string {
	return httpapi.WebhookTokenHash(token)
}
