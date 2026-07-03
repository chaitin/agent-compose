package configstore

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	domain "agent-compose/pkg/model"
)

func normalizeTopicEventRecord(item domain.TopicEventRecord, assignID bool) (domain.TopicEventRecord, error) {
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

func scanTopicEvents(rows *sql.Rows) ([]domain.TopicEventRecord, error) {
	items := make([]domain.TopicEventRecord, 0)
	for rows.Next() {
		item, err := scanTopicEvent(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return items, nil
}

func scanTopicEvent(scan func(dest ...any) error) (domain.TopicEventRecord, error) {
	var item domain.TopicEventRecord
	var claimUntilRaw int64
	var nextAttemptAtRaw int64
	var deadLetterAtRaw int64
	var createdAtRaw int64
	var dispatchedAtRaw int64
	if err := scan(
		&item.Sequence,
		&item.ID,
		&item.Topic,
		&item.Source,
		&item.Provider,
		&item.Intent,
		&item.CorrelationID,
		&item.IdempotencyKey,
		&item.DeliveryID,
		&item.PayloadHash,
		&item.PayloadJSON,
		&item.DispatchStatus,
		&item.ParentEventID,
		&item.PublisherType,
		&item.PublisherID,
		&item.PublisherRunID,
		&item.ReplayOfEventID,
		&item.ClaimID,
		&claimUntilRaw,
		&item.AttemptCount,
		&nextAttemptAtRaw,
		&item.LastError,
		&deadLetterAtRaw,
		&createdAtRaw,
		&dispatchedAtRaw,
	); err != nil {
		return domain.TopicEventRecord{}, fmt.Errorf("scan event: %w", err)
	}
	item.ClaimUntil = ParseStoredUnixTimeAuto(claimUntilRaw)
	item.NextAttemptAt = ParseStoredUnixTimeAuto(nextAttemptAtRaw)
	item.DeadLetterAt = ParseStoredUnixTimeAuto(deadLetterAtRaw)
	item.CreatedAt = ParseStoredUnixTimeAuto(createdAtRaw)
	item.DispatchedAt = ParseStoredUnixTimeAuto(dispatchedAtRaw)
	return item, nil
}

func selectTopicEventSQL() string {
	return `SELECT sequence, id, topic, source, provider, intent, correlation_id, idempotency_key, delivery_id,
		payload_hash, payload_json, dispatch_status, parent_event_id, publisher_type, publisher_id, publisher_run_id,
		replay_of_event_id, claim_id, claim_until, attempt_count, next_attempt_at, last_error, dead_letter_at, created_at, dispatched_at
		FROM event`
}
