package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/chaitin/agent-compose/pkg/events"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func (s *eventStore) CreateEvent(ctx context.Context, item domain.TopicEventRecord) (domain.TopicEventRecord, error) {
	normalized, err := normalizeTopicEventRecord(item, true)
	if err != nil {
		return domain.TopicEventRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.TopicEventRecord{}, fmt.Errorf("begin event transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := insertTopicEvent(ctx, tx, normalized)
	if err != nil {
		_ = tx.Rollback()
		return s.resolveTopicEventInsertError(ctx, normalized, err)
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return domain.TopicEventRecord{}, fmt.Errorf("read event sequence: %w", err)
	}
	normalized.Sequence = sequence
	if err := storeSequencedTopicEventPayload(ctx, tx, &normalized); err != nil {
		return domain.TopicEventRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.TopicEventRecord{}, fmt.Errorf("commit event: %w", err)
	}
	return normalized, nil
}

func insertTopicEvent(ctx context.Context, tx *sql.Tx, item domain.TopicEventRecord) (sql.Result, error) {
	return tx.ExecContext(ctx, `INSERT INTO event(
		id, topic, source, provider, intent, correlation_id, idempotency_key, delivery_id, payload_hash, payload_json,
		dispatch_status, parent_event_id, publisher_type, publisher_id, publisher_run_id, publisher_project_id,
		replay_of_event_id, claim_id, claim_until, attempt_count, next_attempt_at, last_error, dead_letter_at, created_at, dispatched_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID,
		item.Topic,
		item.Source,
		item.Provider,
		item.Intent,
		item.CorrelationID,
		item.IdempotencyKey,
		item.DeliveryID,
		item.PayloadHash,
		item.PayloadJSON,
		item.DispatchStatus,
		item.ParentEventID,
		item.PublisherType,
		item.PublisherID,
		item.PublisherRunID,
		item.PublisherProjectID,
		item.ReplayOfEventID,
		item.ClaimID,
		domain.NonZeroTimeUnixMilli(item.ClaimUntil),
		item.AttemptCount,
		domain.NonZeroTimeUnixMilli(item.NextAttemptAt),
		item.LastError,
		domain.NonZeroTimeUnixMilli(item.DeadLetterAt),
		item.CreatedAt.UnixMilli(),
		domain.NonZeroTimeUnixMilli(item.DispatchedAt),
	)
}

func (s *eventStore) resolveTopicEventInsertError(ctx context.Context, item domain.TopicEventRecord, insertErr error) (domain.TopicEventRecord, error) {
	if item.IdempotencyKey != "" {
		if existing, ok, lookupErr := s.FindEventByIdempotencyKey(ctx, item.Topic, item.IdempotencyKey); lookupErr != nil {
			return domain.TopicEventRecord{}, lookupErr
		} else if ok {
			itemPayloadHash, hashErr := topicEventPayloadHashForSequence(item.PayloadJSON, item.PayloadHash, existing.Sequence)
			if hashErr != nil {
				return domain.TopicEventRecord{}, fmt.Errorf("sequence duplicate event payload: %w", hashErr)
			}
			if existing.PayloadHash != itemPayloadHash {
				return domain.TopicEventRecord{}, &domain.TopicEventIdempotencyConflictError{Existing: existing}
			}
			return existing, nil
		}
	}
	return domain.TopicEventRecord{}, fmt.Errorf("insert event %s: %w", item.ID, insertErr)
}

func storeSequencedTopicEventPayload(ctx context.Context, tx *sql.Tx, item *domain.TopicEventRecord) error {
	sequencedPayload, changed, err := topicEventPayloadWithSequence(item.PayloadJSON, item.Sequence)
	if err != nil {
		return fmt.Errorf("sequence event payload: %w", err)
	}
	if !changed {
		return nil
	}
	item.PayloadJSON = sequencedPayload
	item.PayloadHash = events.PayloadSHA256(sequencedPayload)
	update, err := tx.ExecContext(ctx, `UPDATE event SET payload_hash = ?, payload_json = ? WHERE id = ?`, item.PayloadHash, item.PayloadJSON, item.ID)
	if err != nil {
		return fmt.Errorf("store sequenced event payload: %w", err)
	}
	updated, err := update.RowsAffected()
	if err != nil {
		return fmt.Errorf("read sequenced event update count: %w", err)
	}
	if updated != 1 {
		return fmt.Errorf("updated %d sequenced events, want 1", updated)
	}
	return nil
}

func topicEventPayloadWithSequence(payloadJSON string, sequence int64) (string, bool, error) {
	trimmed := strings.TrimSpace(payloadJSON)
	if trimmed == "" || trimmed[0] != '{' {
		return payloadJSON, false, nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return "", false, err
	}
	if _, ok := payload["sequence"]; !ok {
		return payloadJSON, false, nil
	}
	payload["sequence"] = json.RawMessage(strconv.FormatInt(sequence, 10))
	sequenced, err := domain.MarshalJSONCompact(payload)
	if err != nil {
		return "", false, err
	}
	return sequenced, sequenced != payloadJSON, nil
}

func topicEventPayloadHashForSequence(payloadJSON, payloadHash string, sequence int64) (string, error) {
	sequencedPayload, changed, err := topicEventPayloadWithSequence(payloadJSON, sequence)
	if err != nil {
		return "", err
	}
	if !changed {
		return payloadHash, nil
	}
	return events.PayloadSHA256(sequencedPayload), nil
}
