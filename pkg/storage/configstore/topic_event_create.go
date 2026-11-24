package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/event"
	"github.com/chaitin/agent-compose/pkg/events"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/dialect"
)

// newEntTxClient binds a dao client to an in-flight SQL transaction. The event
// insert and its sequenced-payload rewrite must stay in the caller's
// transaction together with the dialect's sequence lock, so the ent builders
// run over that *sql.Tx instead of opening a separate ent transaction.
func newEntTxClient(d dialect.Dialect, tx *sql.Tx) *dao.Client {
	return dao.NewClient(dao.Driver(entsql.NewDriver(d.EntDialect(), entsql.Conn{ExecQuerier: tx})))
}

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
	client := newEntTxClient(s.dialect, tx)

	// PostgreSQL sequences do not guarantee commit order; event pagination
	// relies on sequence order matching visibility. SQLite's single writer
	// already provides this, so its dialect returns an empty statement.
	// Kept as SQL: ent has no table-lock statement.
	if lock := s.dialect.LockEventSequenceStatement(); lock != "" {
		if _, err := tx.ExecContext(ctx, lock); err != nil {
			return domain.TopicEventRecord{}, fmt.Errorf("lock event sequence: %w", err)
		}
	}

	sequence, err := insertTopicEvent(ctx, client, normalized)
	if err != nil {
		_ = tx.Rollback()
		return s.resolveTopicEventInsertError(ctx, normalized, err)
	}
	normalized.Sequence = sequence
	if err := storeSequencedTopicEventPayload(ctx, client, &normalized); err != nil {
		return domain.TopicEventRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.TopicEventRecord{}, fmt.Errorf("commit event: %w", err)
	}
	return normalized, nil
}

// insertTopicEvent inserts the event without its sequence and returns the
// allocated sequence. Leaving the ID unset keeps the column's database default
// (autoincrement / identity) and ent reads it back with RETURNING.
func insertTopicEvent(ctx context.Context, client *dao.Client, item domain.TopicEventRecord) (int64, error) {
	entity, err := client.Event.Create().
		SetEventID(item.ID).
		SetTopic(item.Topic).
		SetSource(item.Source).
		SetProvider(item.Provider).
		SetIntent(item.Intent).
		SetCorrelationID(item.CorrelationID).
		SetIdempotencyKey(item.IdempotencyKey).
		SetDeliveryID(item.DeliveryID).
		SetPayloadHash(item.PayloadHash).
		SetPayloadJSON(item.PayloadJSON).
		SetDispatchStatus(item.DispatchStatus).
		SetParentEventID(item.ParentEventID).
		SetPublisherType(item.PublisherType).
		SetPublisherID(item.PublisherID).
		SetPublisherRunID(item.PublisherRunID).
		SetPublisherProjectID(item.PublisherProjectID).
		SetReplayOfEventID(item.ReplayOfEventID).
		SetClaimID(item.ClaimID).
		SetClaimUntil(domain.NonZeroTimeUnixMilli(item.ClaimUntil)).
		SetAttemptCount(item.AttemptCount).
		SetNextAttemptAt(domain.NonZeroTimeUnixMilli(item.NextAttemptAt)).
		SetLastError(item.LastError).
		SetDeadLetterAt(domain.NonZeroTimeUnixMilli(item.DeadLetterAt)).
		SetCreatedAt(item.CreatedAt.UnixMilli()).
		SetDispatchedAt(domain.NonZeroTimeUnixMilli(item.DispatchedAt)).
		Save(ctx)
	if err != nil {
		return 0, err
	}
	return entity.ID, nil
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

func storeSequencedTopicEventPayload(ctx context.Context, client *dao.Client, item *domain.TopicEventRecord) error {
	sequencedPayload, changed, err := topicEventPayloadWithSequence(item.PayloadJSON, item.Sequence)
	if err != nil {
		return fmt.Errorf("sequence event payload: %w", err)
	}
	if !changed {
		return nil
	}
	item.PayloadJSON = sequencedPayload
	item.PayloadHash = events.PayloadSHA256(sequencedPayload)
	updated, err := client.Event.Update().
		Where(event.EventIDEQ(item.ID)).
		SetPayloadHash(item.PayloadHash).
		SetPayloadJSON(item.PayloadJSON).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("store sequenced event payload: %w", err)
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
