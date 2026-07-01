package agentcompose

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

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

var topicEventNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type TopicEventRecord = model.TopicEventRecord
type TopicEventFilter = model.TopicEventFilter
type WebhookSource = model.WebhookSource
type EventDelivery = model.EventDelivery
type EventSessionLink = model.EventSessionLink
type EventSessionTraceItem = model.EventSessionTraceItem

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

func normalizeEventDeliveryStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case EventDeliveryStatusMatched:
		return EventDeliveryStatusMatched
	case EventDeliveryStatusRunStarted:
		return EventDeliveryStatusRunStarted
	case EventDeliveryStatusRunSucceeded:
		return EventDeliveryStatusRunSucceeded
	case EventDeliveryStatusRunFailed:
		return EventDeliveryStatusRunFailed
	case EventDeliveryStatusSkipped:
		return EventDeliveryStatusSkipped
	default:
		return ""
	}
}

func topicEventPayloadSHA256(payloadJSON string) string {
	sum := sha256.Sum256([]byte(payloadJSON))
	return "sha256:" + hex.EncodeToString(sum[:])
}
