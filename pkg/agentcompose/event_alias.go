package agentcompose

import (
	eventspkg "agent-compose/pkg/events"
	"agent-compose/pkg/model"
	"context"

	"github.com/labstack/echo/v4"
)

const (
	TopicEventSourceWebhook = model.TopicEventSourceWebhook
	TopicEventSourceLoader  = model.TopicEventSourceLoader
	TopicEventSourceSystem  = model.TopicEventSourceSystem

	TopicEventDispatchPending        = model.TopicEventDispatchPending
	TopicEventDispatchPublishing     = model.TopicEventDispatchPublishing
	TopicEventDispatchPublishedToBus = model.TopicEventDispatchPublishedToBus
	TopicEventDispatchNoSubscriber   = model.TopicEventDispatchNoSubscriber
	TopicEventDispatchRetrying       = model.TopicEventDispatchRetrying
	TopicEventDispatchDeadLetter     = model.TopicEventDispatchDeadLetter

	EventDeliveryStatusMatched      = model.EventDeliveryStatusMatched
	EventDeliveryStatusRunStarted   = model.EventDeliveryStatusRunStarted
	EventDeliveryStatusRunSucceeded = model.EventDeliveryStatusRunSucceeded
	EventDeliveryStatusRunFailed    = model.EventDeliveryStatusRunFailed
	EventDeliveryStatusSkipped      = model.EventDeliveryStatusSkipped
)

type TopicEventRecord = model.TopicEventRecord
type TopicEventFilter = model.TopicEventFilter
type WebhookSource = model.WebhookSource
type EventDelivery = model.EventDelivery
type EventSessionLink = model.EventSessionLink
type EventSessionTraceItem = model.EventSessionTraceItem
type EventDispatcher = eventspkg.EventDispatcher
type WebhookRunQueue = eventspkg.WebhookRunQueue

func NewEventDispatcher(rootCtx context.Context, configDB *ConfigStore, bus *LoaderBus) *EventDispatcher {
	return eventspkg.NewEventDispatcher(rootCtx, configDB, bus)
}

func registerWebhookRoutes(app *echo.Echo, service *Service) {
	eventspkg.RegisterRoutes(app, eventspkg.NewService(service.config, service.configDB))
}
