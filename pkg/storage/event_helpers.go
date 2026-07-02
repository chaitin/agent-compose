package storage

import (
	"bytes"
	"encoding/json"
	"strings"

	"agent-compose/pkg/model"
)

func validateTopicEventName(topic string) error {
	return model.ValidateTopicEventName(topic)
}

func normalizeTopicEventSource(source string) string {
	return model.NormalizeTopicEventSource(source)
}

func normalizeTopicEventDispatchStatus(status string) string {
	return model.NormalizeTopicEventDispatchStatus(status)
}

func normalizeEventDeliveryStatus(status string) string {
	return model.NormalizeEventDeliveryStatus(status)
}

func topicEventPayloadSHA256(payloadJSON string) string {
	return model.TopicEventPayloadSHA256(payloadJSON)
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
