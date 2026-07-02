package agentcompose

import "agent-compose/internal/agentcompose/events"

const (
	TopicEventSourceWebhook = events.TopicEventSourceWebhook
	TopicEventSourceLoader  = events.TopicEventSourceLoader
	TopicEventSourceSystem  = events.TopicEventSourceSystem

	TopicEventDispatchPending        = events.TopicEventDispatchPending
	TopicEventDispatchPublishing     = events.TopicEventDispatchPublishing
	TopicEventDispatchPublishedToBus = events.TopicEventDispatchPublishedToBus
	TopicEventDispatchNoSubscriber   = events.TopicEventDispatchNoSubscriber
	TopicEventDispatchRetrying       = events.TopicEventDispatchRetrying
	TopicEventDispatchDeadLetter     = events.TopicEventDispatchDeadLetter

	EventDeliveryStatusMatched      = events.EventDeliveryStatusMatched
	EventDeliveryStatusRunStarted   = events.EventDeliveryStatusRunStarted
	EventDeliveryStatusRunSucceeded = events.EventDeliveryStatusRunSucceeded
	EventDeliveryStatusRunFailed    = events.EventDeliveryStatusRunFailed
	EventDeliveryStatusSkipped      = events.EventDeliveryStatusSkipped
)

type TopicEventRecord = events.TopicEventRecord
type TopicEventFilter = events.TopicEventFilter
type WebhookSource = events.WebhookSource
type EventDelivery = events.EventDelivery
type EventSessionLink = events.EventSessionLink
type EventSessionTraceItem = events.EventSessionTraceItem

func validateTopicEventName(topic string) error {
	return events.ValidateTopicEventName(topic)
}

func normalizeTopicEventSource(source string) string {
	return events.NormalizeTopicEventSource(source)
}

func normalizeTopicEventDispatchStatus(status string) string {
	return events.NormalizeTopicEventDispatchStatus(status)
}

func normalizeEventDeliveryStatus(status string) string {
	return events.NormalizeEventDeliveryStatus(status)
}

func topicEventPayloadSHA256(payloadJSON string) string {
	return events.TopicEventPayloadSHA256(payloadJSON)
}
