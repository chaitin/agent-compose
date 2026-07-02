package events

import (
	"agent-compose/pkg/model"
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

func validateTopicEventName(topic string) error {
	return model.ValidateTopicEventName(topic)
}

func ValidateTopicEventName(topic string) error {
	return validateTopicEventName(topic)
}

func topicEventPayloadSHA256(payloadJSON string) string {
	return model.TopicEventPayloadSHA256(payloadJSON)
}

func TopicEventPayloadSHA256(payloadJSON string) string {
	return topicEventPayloadSHA256(payloadJSON)
}
