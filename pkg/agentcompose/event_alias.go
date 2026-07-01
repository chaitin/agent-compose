package agentcompose

import (
	eventspkg "agent-compose/pkg/events"
	"agent-compose/pkg/model"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

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

var topicEventNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func NewEventDispatcher(rootCtx context.Context, configDB *ConfigStore, bus *LoaderBus) *EventDispatcher {
	return eventspkg.NewEventDispatcher(rootCtx, configDB, bus)
}

func registerWebhookRoutes(app *echo.Echo, service *Service) {
	eventspkg.RegisterRoutes(app, eventspkg.NewService(service.config, service.configDB))
}

func newWebhookRunQueue(defaultWorkers int) *WebhookRunQueue {
	return eventspkg.NewWebhookRunQueue(defaultWorkers)
}

func validateTopicEventName(topic string) error {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return fmt.Errorf("topic is required")
	}
	if len(topic) > 128 {
		return fmt.Errorf("topic is too long")
	}
	if !topicEventNamePattern.MatchString(topic) {
		return fmt.Errorf("topic contains invalid characters")
	}
	return nil
}

func normalizeTopicEventSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case TopicEventSourceWebhook:
		return TopicEventSourceWebhook
	case TopicEventSourceLoader:
		return TopicEventSourceLoader
	case TopicEventSourceSystem:
		return TopicEventSourceSystem
	default:
		return ""
	}
}

func normalizeTopicEventDispatchStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", TopicEventDispatchPending:
		return TopicEventDispatchPending
	case TopicEventDispatchPublishing:
		return TopicEventDispatchPublishing
	case TopicEventDispatchPublishedToBus:
		return TopicEventDispatchPublishedToBus
	case TopicEventDispatchNoSubscriber:
		return TopicEventDispatchNoSubscriber
	case TopicEventDispatchRetrying:
		return TopicEventDispatchRetrying
	case TopicEventDispatchDeadLetter:
		return TopicEventDispatchDeadLetter
	default:
		return ""
	}
}

func topicEventPayloadSHA256(payloadJSON string) string {
	sum := sha256.Sum256([]byte(payloadJSON))
	return "sha256:" + hex.EncodeToString(sum[:])
}
