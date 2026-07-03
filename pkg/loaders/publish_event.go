package loaders

import (
	"encoding/json"
	"fmt"
	"strings"

	domain "agent-compose/pkg/model"

	"github.com/google/uuid"
)

type TriggerEventMetadata struct {
	EventID       string
	Sequence      int64
	CorrelationID string
}

type PublishedTopicEvent struct {
	Record   domain.TopicEventRecord
	Envelope map[string]any
}

func NewPublishedTopicEvent(topic, payloadJSON string, trigger TriggerEventMetadata, loaderID, runID string) (PublishedTopicEvent, error) {
	topic = strings.TrimSpace(topic)
	if err := ValidatePublishTopic(topic); err != nil {
		return PublishedTopicEvent{}, err
	}
	payloadJSON, err := domain.NormalizeJSONDocument(payloadJSON)
	if err != nil {
		return PublishedTopicEvent{}, err
	}
	payload, err := ParseJSONObject(payloadJSON)
	if err != nil {
		return PublishedTopicEvent{}, fmt.Errorf("scheduler.event.publish payload must be an object")
	}
	eventID := "evt_" + uuid.NewString()
	correlationID := StringFromMap(payload, "correlationId")
	if correlationID == "" {
		correlationID = StringFromMap(payload, "correlation_id")
	}
	if correlationID == "" {
		correlationID = trigger.CorrelationID
	}
	if correlationID == "" {
		correlationID = eventID
	}
	parentEventID := trigger.EventID
	if explicitParent := StringFromMap(payload, "parentEventId"); explicitParent != "" {
		parentEventID = explicitParent
	}
	provider := StringFromMap(payload, "provider")
	envelope := map[string]any{
		"eventId":       eventID,
		"sequence":      int64(0),
		"source":        domain.TopicEventSourceLoader,
		"provider":      provider,
		"topic":         topic,
		"correlationId": correlationID,
		"body":          payload,
	}
	if parentEventID != "" {
		envelope["parentEventId"] = parentEventID
	}
	envelopeJSON, err := domain.MarshalJSONCompact(envelope)
	if err != nil {
		return PublishedTopicEvent{}, err
	}
	return PublishedTopicEvent{
		Record: domain.TopicEventRecord{
			ID:             eventID,
			Topic:          topic,
			Source:         domain.TopicEventSourceLoader,
			Provider:       provider,
			CorrelationID:  correlationID,
			PayloadHash:    domain.TopicEventPayloadSHA256(envelopeJSON),
			PayloadJSON:    envelopeJSON,
			DispatchStatus: domain.TopicEventDispatchPending,
			ParentEventID:  parentEventID,
			PublisherType:  domain.TopicEventSourceLoader,
			PublisherID:    strings.TrimSpace(loaderID),
			PublisherRunID: strings.TrimSpace(runID),
		},
		Envelope: envelope,
	}, nil
}

func UpdatePublishedTopicEventSequence(event PublishedTopicEvent, sequence int64) (domain.TopicEventRecord, error) {
	event.Record.Sequence = sequence
	if event.Envelope == nil {
		return event.Record, nil
	}
	event.Envelope["sequence"] = sequence
	envelopeJSON, err := domain.MarshalJSONCompact(event.Envelope)
	if err != nil {
		return domain.TopicEventRecord{}, err
	}
	event.Record.PayloadJSON = envelopeJSON
	event.Record.PayloadHash = domain.TopicEventPayloadSHA256(envelopeJSON)
	return event.Record, nil
}

func ParseTriggerEventMetadata(payloadJSON string) TriggerEventMetadata {
	payloadJSON = strings.TrimSpace(payloadJSON)
	if payloadJSON == "" {
		return TriggerEventMetadata{}
	}
	var envelope struct {
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &envelope); err != nil {
		return TriggerEventMetadata{}
	}
	return TriggerEventMetadata{
		EventID:       StringFromMap(envelope.Payload, "eventId"),
		CorrelationID: StringFromMap(envelope.Payload, "correlationId"),
		Sequence:      Int64FromMap(envelope.Payload, "sequence"),
	}
}

func ValidatePublishTopic(topic string) error {
	if err := domain.ValidateTopicEventName(topic); err != nil {
		return err
	}
	if strings.HasPrefix(topic, "runtime.") || strings.HasPrefix(topic, "workflow.") || strings.HasPrefix(topic, "external.") {
		return nil
	}
	return fmt.Errorf("loader event topic must use runtime.*, workflow.*, or external.* prefix")
}

func ParseJSONObject(payloadJSON string) (map[string]any, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil || payload == nil {
		return nil, err
	}
	return payload, nil
}

func IsJSONObject(payloadJSON string) bool {
	payload, err := ParseJSONObject(payloadJSON)
	return err == nil && payload != nil
}

func StringFromMap(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func Int64FromMap(values map[string]any, key string) int64 {
	if values == nil {
		return 0
	}
	switch value := values[key].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	default:
		return 0
	}
}
