package events

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"agent-compose/pkg/agentcompose/domain"
)

func NormalizeTopicEventRecord(item domain.TopicEventRecord, assignID bool) (domain.TopicEventRecord, error) {
	item.ID = strings.TrimSpace(item.ID)
	if assignID && item.ID == "" {
		item.ID = "evt_" + uuid.NewString()
	}
	if item.ID == "" {
		return domain.TopicEventRecord{}, fmt.Errorf("event id is required")
	}
	item.Topic = strings.TrimSpace(item.Topic)
	if err := domain.ValidateTopicEventName(item.Topic); err != nil {
		return domain.TopicEventRecord{}, err
	}
	item.Source = domain.NormalizeTopicEventSource(item.Source)
	if item.Source == "" {
		return domain.TopicEventRecord{}, fmt.Errorf("event source is required")
	}
	item.DispatchStatus = domain.NormalizeTopicEventDispatchStatus(item.DispatchStatus)
	if item.DispatchStatus == "" {
		return domain.TopicEventRecord{}, fmt.Errorf("event dispatch status is invalid")
	}
	item.Provider = strings.TrimSpace(item.Provider)
	item.Intent = strings.TrimSpace(item.Intent)
	item.CorrelationID = strings.TrimSpace(item.CorrelationID)
	if item.CorrelationID == "" {
		item.CorrelationID = item.ID
	}
	item.IdempotencyKey = strings.TrimSpace(item.IdempotencyKey)
	item.DeliveryID = strings.TrimSpace(item.DeliveryID)
	item.PayloadJSON = strings.TrimSpace(item.PayloadJSON)
	if item.PayloadJSON == "" {
		item.PayloadJSON = "{}"
	}
	if _, err := domain.NormalizeJSONDocument(item.PayloadJSON); err != nil {
		return domain.TopicEventRecord{}, err
	}
	item.PayloadHash = strings.TrimSpace(item.PayloadHash)
	if item.PayloadHash == "" {
		item.PayloadHash = domain.TopicEventPayloadSHA256(item.PayloadJSON)
	}
	item.ParentEventID = strings.TrimSpace(item.ParentEventID)
	item.PublisherType = strings.TrimSpace(item.PublisherType)
	item.PublisherID = strings.TrimSpace(item.PublisherID)
	item.PublisherRunID = strings.TrimSpace(item.PublisherRunID)
	item.ReplayOfEventID = strings.TrimSpace(item.ReplayOfEventID)
	item.ClaimID = strings.TrimSpace(item.ClaimID)
	if !item.ClaimUntil.IsZero() {
		item.ClaimUntil = item.ClaimUntil.UTC()
	}
	if item.AttemptCount < 0 {
		item.AttemptCount = 0
	}
	if !item.NextAttemptAt.IsZero() {
		item.NextAttemptAt = item.NextAttemptAt.UTC()
	}
	item.LastError = strings.TrimSpace(item.LastError)
	if !item.DeadLetterAt.IsZero() {
		item.DeadLetterAt = item.DeadLetterAt.UTC()
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	} else {
		item.CreatedAt = item.CreatedAt.UTC()
	}
	if !item.DispatchedAt.IsZero() {
		item.DispatchedAt = item.DispatchedAt.UTC()
	}
	return item, nil
}
