package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var topicEventNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

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

func normalizeJSONDocument(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}", nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return "", err
	}
	return marshalJSONCompact(decoded)
}

func marshalJSONCompact(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return "", err
	}
	return compact.String(), nil
}
