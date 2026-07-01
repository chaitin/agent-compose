package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (s *ConfigStore) ensureEventSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS event (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			id TEXT NOT NULL UNIQUE,
			topic TEXT NOT NULL,
			source TEXT NOT NULL,
			provider TEXT NOT NULL DEFAULT '',
			intent TEXT NOT NULL DEFAULT '',
			correlation_id TEXT NOT NULL,
			idempotency_key TEXT NOT NULL DEFAULT '',
			delivery_id TEXT NOT NULL DEFAULT '',
			payload_hash TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			dispatch_status TEXT NOT NULL,
			parent_event_id TEXT NOT NULL DEFAULT '',
			publisher_type TEXT NOT NULL DEFAULT '',
			publisher_id TEXT NOT NULL DEFAULT '',
			publisher_run_id TEXT NOT NULL DEFAULT '',
			replay_of_event_id TEXT NOT NULL DEFAULT '',
			claim_id TEXT NOT NULL DEFAULT '',
			claim_until INTEGER NOT NULL DEFAULT 0,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			next_attempt_at INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			dead_letter_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			dispatched_at INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS webhook_source (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			provider TEXT NOT NULL DEFAULT '',
			topic_prefix TEXT NOT NULL,
			token_hash TEXT NOT NULL DEFAULT '',
			signature_type TEXT NOT NULL DEFAULT '',
			signature_secret TEXT NOT NULL DEFAULT '',
			body_limit_bytes INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_webhook_source_enabled_topic ON webhook_source(enabled, topic_prefix);`,
		`CREATE TABLE IF NOT EXISTS event_delivery (
			event_id TEXT NOT NULL,
			loader_id TEXT NOT NULL,
			trigger_id TEXT NOT NULL,
			run_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			error TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(event_id, loader_id, trigger_id)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_event_delivery_run ON event_delivery(run_id);`,
		`CREATE INDEX IF NOT EXISTS idx_event_delivery_status ON event_delivery(status, updated_at);`,
		`CREATE TABLE IF NOT EXISTS event_session_link (
			event_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			relation TEXT NOT NULL,
			loader_id TEXT NOT NULL DEFAULT '',
			run_id TEXT NOT NULL DEFAULT '',
			trigger_id TEXT NOT NULL DEFAULT '',
			loader_event_id TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			PRIMARY KEY(event_id, session_id, relation, run_id)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_event_session_link_session ON event_session_link(session_id, created_at);`,
		`CREATE INDEX IF NOT EXISTS idx_event_session_link_run ON event_session_link(run_id);`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create event schema: %w", err)
		}
	}
	for column, definition := range map[string]string{
		"replay_of_event_id": "TEXT NOT NULL DEFAULT ''",
		"claim_id":           "TEXT NOT NULL DEFAULT ''",
		"claim_until":        "INTEGER NOT NULL DEFAULT 0",
		"attempt_count":      "INTEGER NOT NULL DEFAULT 0",
		"next_attempt_at":    "INTEGER NOT NULL DEFAULT 0",
		"last_error":         "TEXT NOT NULL DEFAULT ''",
		"dead_letter_at":     "INTEGER NOT NULL DEFAULT 0",
	} {
		if err := ensureColumn(ctx, s.db, "event", column, definition); err != nil {
			return fmt.Errorf("ensure event %s column: %w", column, err)
		}
	}
	indexStatements := []string{
		`CREATE INDEX IF NOT EXISTS idx_event_correlation ON event(correlation_id, sequence);`,
		`CREATE INDEX IF NOT EXISTS idx_event_topic_sequence ON event(topic, sequence);`,
		`CREATE INDEX IF NOT EXISTS idx_event_dispatch ON event(dispatch_status, sequence);`,
		`CREATE INDEX IF NOT EXISTS idx_event_dispatch_attempt ON event(dispatch_status, next_attempt_at, sequence);`,
		`CREATE INDEX IF NOT EXISTS idx_event_parent ON event(parent_event_id, sequence);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_event_idempotency ON event(topic, idempotency_key) WHERE idempotency_key != '';`,
	}
	for _, stmt := range indexStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create event schema: %w", err)
		}
	}
	return nil
}

func (s *ConfigStore) EnsureEventSchema(ctx context.Context) error {
	return s.ensureEventSchema(ctx)
}

func normalizeTopicEventRecord(item TopicEventRecord, assignID bool) (TopicEventRecord, error) {
	item.ID = strings.TrimSpace(item.ID)
	if assignID && item.ID == "" {
		item.ID = "evt_" + uuid.NewString()
	}
	if item.ID == "" {
		return TopicEventRecord{}, fmt.Errorf("event id is required")
	}
	item.Topic = strings.TrimSpace(item.Topic)
	if err := validateTopicEventName(item.Topic); err != nil {
		return TopicEventRecord{}, err
	}
	item.Source = normalizeTopicEventSource(item.Source)
	if item.Source == "" {
		return TopicEventRecord{}, fmt.Errorf("event source is required")
	}
	item.DispatchStatus = normalizeTopicEventDispatchStatus(item.DispatchStatus)
	if item.DispatchStatus == "" {
		return TopicEventRecord{}, fmt.Errorf("event dispatch status is invalid")
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
	if _, err := normalizeJSONDocument(item.PayloadJSON); err != nil {
		return TopicEventRecord{}, err
	}
	item.PayloadHash = strings.TrimSpace(item.PayloadHash)
	if item.PayloadHash == "" {
		item.PayloadHash = topicEventPayloadSHA256(item.PayloadJSON)
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

func NormalizeTopicEventRecord(item TopicEventRecord, assignID bool) (TopicEventRecord, error) {
	return normalizeTopicEventRecord(item, assignID)
}

func (s *ConfigStore) CreateEvent(ctx context.Context, item TopicEventRecord) (TopicEventRecord, error) {
	normalized, err := normalizeTopicEventRecord(item, true)
	if err != nil {
		return TopicEventRecord{}, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO event(
		id, topic, source, provider, intent, correlation_id, idempotency_key, delivery_id, payload_hash, payload_json,
		dispatch_status, parent_event_id, publisher_type, publisher_id, publisher_run_id, replay_of_event_id,
		claim_id, claim_until, attempt_count, next_attempt_at, last_error, dead_letter_at, created_at, dispatched_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		normalized.ID,
		normalized.Topic,
		normalized.Source,
		normalized.Provider,
		normalized.Intent,
		normalized.CorrelationID,
		normalized.IdempotencyKey,
		normalized.DeliveryID,
		normalized.PayloadHash,
		normalized.PayloadJSON,
		normalized.DispatchStatus,
		normalized.ParentEventID,
		normalized.PublisherType,
		normalized.PublisherID,
		normalized.PublisherRunID,
		normalized.ReplayOfEventID,
		normalized.ClaimID,
		nonZeroTimeUnixMilli(normalized.ClaimUntil),
		normalized.AttemptCount,
		nonZeroTimeUnixMilli(normalized.NextAttemptAt),
		normalized.LastError,
		nonZeroTimeUnixMilli(normalized.DeadLetterAt),
		normalized.CreatedAt.UnixMilli(),
		nonZeroTimeUnixMilli(normalized.DispatchedAt),
	)
	if err != nil {
		if normalized.IdempotencyKey != "" {
			if existing, ok, lookupErr := s.FindEventByIdempotencyKey(ctx, normalized.Topic, normalized.IdempotencyKey); lookupErr != nil {
				return TopicEventRecord{}, lookupErr
			} else if ok {
				if existing.PayloadHash != normalized.PayloadHash {
					return TopicEventRecord{}, fmt.Errorf("event idempotency conflict for topic %q", normalized.Topic)
				}
				return existing, nil
			}
		}
		return TopicEventRecord{}, fmt.Errorf("insert event %s: %w", normalized.ID, err)
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return TopicEventRecord{}, fmt.Errorf("read event sequence: %w", err)
	}
	normalized.Sequence = sequence
	return s.GetEvent(ctx, normalized.ID)
}

func (s *ConfigStore) GetEvent(ctx context.Context, eventID string) (TopicEventRecord, error) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return TopicEventRecord{}, fmt.Errorf("event id is required")
	}
	row := s.db.QueryRowContext(ctx, selectTopicEventSQL()+` WHERE id = ?`, eventID)
	item, err := scanTopicEvent(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TopicEventRecord{}, fmt.Errorf("event %s not found: %w", eventID, err)
		}
		return TopicEventRecord{}, err
	}
	return item, nil
}

func (s *ConfigStore) FindEventByIdempotencyKey(ctx context.Context, topic, key string) (TopicEventRecord, bool, error) {
	topic = strings.TrimSpace(topic)
	key = strings.TrimSpace(key)
	if topic == "" || key == "" {
		return TopicEventRecord{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, selectTopicEventSQL()+` WHERE topic = ? AND idempotency_key = ?`, topic, key)
	item, err := scanTopicEvent(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TopicEventRecord{}, false, nil
		}
		return TopicEventRecord{}, false, err
	}
	return item, true, nil
}

func (s *ConfigStore) ListPendingEvents(ctx context.Context, limit int) ([]TopicEventRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, selectTopicEventSQL()+` WHERE dispatch_status = ? ORDER BY sequence ASC LIMIT ?`, TopicEventDispatchPending, limit)
	if err != nil {
		return nil, fmt.Errorf("query pending events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanTopicEvents(rows)
}

func (s *ConfigStore) ListEvents(ctx context.Context, filter TopicEventFilter) ([]TopicEventRecord, error) {
	if strings.TrimSpace(filter.Topic) == "" && strings.TrimSpace(filter.CorrelationID) == "" {
		return nil, fmt.Errorf("topic or correlation id is required")
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	clauses := make([]string, 0, 4)
	args := make([]any, 0, 5)
	if topic := strings.TrimSpace(filter.Topic); topic != "" {
		if err := validateTopicEventName(topic); err != nil {
			return nil, err
		}
		clauses = append(clauses, "topic = ?")
		args = append(args, topic)
	}
	if correlationID := strings.TrimSpace(filter.CorrelationID); correlationID != "" {
		clauses = append(clauses, "correlation_id = ?")
		args = append(args, correlationID)
	}
	if filter.AfterSequence > 0 {
		clauses = append(clauses, "sequence > ?")
		args = append(args, filter.AfterSequence)
	}
	if status := normalizeTopicEventDispatchStatus(filter.DispatchStatus); status != "" && strings.TrimSpace(filter.DispatchStatus) != "" {
		clauses = append(clauses, "dispatch_status = ?")
		args = append(args, status)
	}
	args = append(args, limit)
	query := selectTopicEventSQL() + ` WHERE ` + strings.Join(clauses, " AND ") + ` ORDER BY sequence ASC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanTopicEvents(rows)
}

func (s *ConfigStore) MarkEventPublished(ctx context.Context, eventID, claimID string, dispatchedAt time.Time) error {
	eventID = strings.TrimSpace(eventID)
	claimID = strings.TrimSpace(claimID)
	if eventID == "" || claimID == "" {
		return fmt.Errorf("event id and claim id are required")
	}
	if dispatchedAt.IsZero() {
		dispatchedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `UPDATE event SET dispatch_status = ?, dispatched_at = ?, claim_id = '', claim_until = 0, last_error = '' WHERE id = ? AND claim_id = ?`,
		TopicEventDispatchPublishedToBus, dispatchedAt.UTC().UnixMilli(), eventID, claimID)
	if err != nil {
		return fmt.Errorf("mark event published: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read event update count: %w", err)
	}
	if affected == 0 {
		if _, err := s.GetEvent(ctx, eventID); err != nil {
			return fmt.Errorf("event %s not found", eventID)
		}
		return nil
	}
	return nil
}

func selectTopicEventSQL() string {
	return `SELECT sequence, id, topic, source, provider, intent, correlation_id, idempotency_key, delivery_id,
		payload_hash, payload_json, dispatch_status, parent_event_id, publisher_type, publisher_id, publisher_run_id,
		replay_of_event_id, claim_id, claim_until, attempt_count, next_attempt_at, last_error, dead_letter_at, created_at, dispatched_at
		FROM event`
}

func (s *ConfigStore) UpdateEventPayload(ctx context.Context, eventID, payloadJSON string) error {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return fmt.Errorf("event id is required")
	}
	payloadJSON = strings.TrimSpace(payloadJSON)
	if payloadJSON == "" {
		return fmt.Errorf("event payload json is required")
	}
	if _, err := normalizeJSONDocument(payloadJSON); err != nil {
		return err
	}
	payloadHash := topicEventPayloadSHA256(payloadJSON)
	result, err := s.db.ExecContext(ctx, `UPDATE event SET payload_hash = ?, payload_json = ? WHERE id = ?`, payloadHash, payloadJSON, eventID)
	if err != nil {
		return fmt.Errorf("update event payload: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read event payload update count: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("event %s not found", eventID)
	}
	return nil
}

func scanTopicEvents(rows *sql.Rows) ([]TopicEventRecord, error) {
	items := make([]TopicEventRecord, 0)
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

func scanTopicEvent(scan func(dest ...any) error) (TopicEventRecord, error) {
	var item TopicEventRecord
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
		return TopicEventRecord{}, fmt.Errorf("scan event: %w", err)
	}
	item.ClaimUntil = parseStoredUnixTimeAuto(claimUntilRaw)
	item.NextAttemptAt = parseStoredUnixTimeAuto(nextAttemptAtRaw)
	item.DeadLetterAt = parseStoredUnixTimeAuto(deadLetterAtRaw)
	item.CreatedAt = parseStoredUnixTimeAuto(createdAtRaw)
	item.DispatchedAt = parseStoredUnixTimeAuto(dispatchedAtRaw)
	return item, nil
}

func (s *ConfigStore) ListDispatchableEvents(ctx context.Context, now time.Time, limit int) ([]TopicEventRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	nowMillis := now.UTC().UnixMilli()
	rows, err := s.db.QueryContext(ctx, selectTopicEventSQL()+` WHERE dispatch_status IN (?, ?, ?) AND (next_attempt_at = 0 OR next_attempt_at <= ?) AND (claim_until = 0 OR claim_until <= ?) ORDER BY sequence ASC LIMIT ?`,
		TopicEventDispatchPending,
		TopicEventDispatchRetrying,
		TopicEventDispatchPublishing,
		nowMillis,
		nowMillis,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query dispatchable events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanTopicEvents(rows)
}

func (s *ConfigStore) ClaimEvent(ctx context.Context, eventID, claimID string, now, until time.Time) (bool, error) {
	eventID = strings.TrimSpace(eventID)
	claimID = strings.TrimSpace(claimID)
	if eventID == "" || claimID == "" {
		return false, fmt.Errorf("event claim id is required")
	}
	nowMillis := now.UTC().UnixMilli()
	result, err := s.db.ExecContext(ctx, `UPDATE event
		SET dispatch_status = ?, claim_id = ?, claim_until = ?, attempt_count = attempt_count + 1, last_error = ''
		WHERE id = ?
		  AND dispatch_status IN (?, ?, ?)
		  AND (next_attempt_at = 0 OR next_attempt_at <= ?)
		  AND (claim_until = 0 OR claim_until <= ?)`,
		TopicEventDispatchPublishing,
		claimID,
		until.UTC().UnixMilli(),
		eventID,
		TopicEventDispatchPending,
		TopicEventDispatchRetrying,
		TopicEventDispatchPublishing,
		nowMillis,
		nowMillis,
	)
	if err != nil {
		return false, fmt.Errorf("claim event: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read event claim count: %w", err)
	}
	return affected > 0, nil
}

func (s *ConfigStore) ReleaseEventClaim(ctx context.Context, eventID, claimID, status, lastError string, nextAttemptAt time.Time) error {
	eventID = strings.TrimSpace(eventID)
	claimID = strings.TrimSpace(claimID)
	status = normalizeTopicEventDispatchStatus(status)
	if eventID == "" || claimID == "" || status == "" {
		return fmt.Errorf("event claim release requires event, claim, and status")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE event SET dispatch_status = ?, claim_id = '', claim_until = 0, next_attempt_at = ?, last_error = ? WHERE id = ? AND claim_id = ?`,
		status,
		nonZeroTimeUnixMilli(nextAttemptAt),
		strings.TrimSpace(lastError),
		eventID,
		claimID,
	)
	if err != nil {
		return fmt.Errorf("release event claim: %w", err)
	}
	return nil
}

func (s *ConfigStore) MarkEventNoSubscriber(ctx context.Context, eventID, claimID string, dispatchedAt time.Time) error {
	eventID = strings.TrimSpace(eventID)
	claimID = strings.TrimSpace(claimID)
	if eventID == "" || claimID == "" {
		return fmt.Errorf("event id and claim id are required")
	}
	if dispatchedAt.IsZero() {
		dispatchedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `UPDATE event SET dispatch_status = ?, dispatched_at = ?, claim_id = '', claim_until = 0, last_error = '' WHERE id = ? AND claim_id = ?`,
		TopicEventDispatchNoSubscriber,
		dispatchedAt.UTC().UnixMilli(),
		eventID,
		claimID,
	)
	if err != nil {
		return fmt.Errorf("mark event no subscriber: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read event no subscriber update count: %w", err)
	}
	if affected == 0 {
		if _, err := s.GetEvent(ctx, eventID); err != nil {
			return fmt.Errorf("event %s not found", eventID)
		}
		return nil
	}
	return nil
}

func (s *ConfigStore) UpsertEventDelivery(ctx context.Context, delivery EventDelivery) error {
	delivery.EventID = strings.TrimSpace(delivery.EventID)
	delivery.LoaderID = strings.TrimSpace(delivery.LoaderID)
	delivery.TriggerID = strings.TrimSpace(delivery.TriggerID)
	delivery.RunID = strings.TrimSpace(delivery.RunID)
	delivery.Status = normalizeEventDeliveryStatus(delivery.Status)
	delivery.Error = strings.TrimSpace(delivery.Error)
	if delivery.EventID == "" || delivery.LoaderID == "" || delivery.TriggerID == "" || delivery.Status == "" {
		return fmt.Errorf("event delivery requires event, loader, trigger, and status")
	}
	now := time.Now().UTC()
	if delivery.CreatedAt.IsZero() {
		delivery.CreatedAt = now
	}
	if delivery.UpdatedAt.IsZero() {
		delivery.UpdatedAt = now
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO event_delivery(event_id, loader_id, trigger_id, run_id, status, error, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(event_id, loader_id, trigger_id) DO UPDATE SET
			run_id = CASE WHEN excluded.run_id != '' THEN excluded.run_id ELSE event_delivery.run_id END,
			status = CASE
				WHEN excluded.status = ? AND excluded.run_id = '' AND event_delivery.run_id != '' THEN event_delivery.status
				ELSE excluded.status
			END,
			error = CASE
				WHEN excluded.status = ? AND excluded.run_id = '' AND event_delivery.run_id != '' THEN event_delivery.error
				ELSE excluded.error
			END,
			updated_at = excluded.updated_at`,
		delivery.EventID,
		delivery.LoaderID,
		delivery.TriggerID,
		delivery.RunID,
		delivery.Status,
		delivery.Error,
		delivery.CreatedAt.UTC().UnixMilli(),
		delivery.UpdatedAt.UTC().UnixMilli(),
		EventDeliveryStatusMatched,
		EventDeliveryStatusMatched,
	)
	if err != nil {
		return fmt.Errorf("upsert event delivery: %w", err)
	}
	return nil
}

func (s *ConfigStore) AddEventSessionLink(ctx context.Context, link EventSessionLink) error {
	link.EventID = strings.TrimSpace(link.EventID)
	link.SessionID = strings.TrimSpace(link.SessionID)
	link.Relation = strings.TrimSpace(link.Relation)
	link.LoaderID = strings.TrimSpace(link.LoaderID)
	link.RunID = strings.TrimSpace(link.RunID)
	link.TriggerID = strings.TrimSpace(link.TriggerID)
	link.LoaderEventID = strings.TrimSpace(link.LoaderEventID)
	if link.EventID == "" || link.SessionID == "" || link.Relation == "" {
		return fmt.Errorf("event session link requires event, session, and relation")
	}
	if link.CreatedAt.IsZero() {
		link.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO event_session_link(event_id, session_id, relation, loader_id, run_id, trigger_id, loader_event_id, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		link.EventID,
		link.SessionID,
		link.Relation,
		link.LoaderID,
		link.RunID,
		link.TriggerID,
		link.LoaderEventID,
		link.CreatedAt.UTC().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("insert event session link: %w", err)
	}
	return nil
}

func (s *ConfigStore) ListEventDeliveries(ctx context.Context, eventIDs []string) ([]EventDelivery, error) {
	seen := map[string]struct{}{}
	ids := make([]string, 0, len(eventIDs))
	for _, id := range eventIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT event_id, loader_id, trigger_id, run_id, status, error, created_at, updated_at
		FROM event_delivery WHERE event_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY updated_at ASC, loader_id ASC, trigger_id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query event deliveries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]EventDelivery, 0)
	for rows.Next() {
		var item EventDelivery
		var createdAtRaw int64
		var updatedAtRaw int64
		if err := rows.Scan(&item.EventID, &item.LoaderID, &item.TriggerID, &item.RunID, &item.Status, &item.Error, &createdAtRaw, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan event delivery: %w", err)
		}
		item.CreatedAt = parseStoredUnixTimeAuto(createdAtRaw)
		item.UpdatedAt = parseStoredUnixTimeAuto(updatedAtRaw)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate event deliveries: %w", err)
	}
	return items, nil
}

func (s *ConfigStore) ListEventSessionLinks(ctx context.Context, eventIDs []string) ([]EventSessionTraceItem, error) {
	seen := map[string]struct{}{}
	ids := make([]string, 0, len(eventIDs))
	for _, id := range eventIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT event_id, session_id, relation, loader_id, run_id, trigger_id, loader_event_id, created_at
		FROM event_session_link WHERE event_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY created_at ASC, session_id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query event session links: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]EventSessionTraceItem, 0)
	for rows.Next() {
		var item EventSessionTraceItem
		var createdAtRaw int64
		if err := rows.Scan(&item.EventID, &item.SessionID, &item.Relation, &item.LoaderID, &item.RunID, &item.TriggerID, &item.LoaderEventID, &createdAtRaw); err != nil {
			return nil, fmt.Errorf("scan event session link: %w", err)
		}
		item.CreatedAt = parseStoredUnixTimeAuto(createdAtRaw)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate event session links: %w", err)
	}
	return items, nil
}

func (s *ConfigStore) ListDescendantEventIDs(ctx context.Context, rootEventID string, limit int) ([]string, error) {
	rootEventID = strings.TrimSpace(rootEventID)
	if rootEventID == "" {
		return nil, fmt.Errorf("event id is required")
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	ids := []string{rootEventID}
	seen := map[string]struct{}{rootEventID: {}}
	queue := []string{rootEventID}
	for len(queue) > 0 && len(ids) < limit {
		parent := queue[0]
		queue = queue[1:]
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM event WHERE parent_event_id = ? ORDER BY sequence ASC`, parent)
		if err != nil {
			return nil, fmt.Errorf("query descendant events: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan descendant event: %w", err)
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
			queue = append(queue, id)
			if len(ids) >= limit {
				break
			}
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close descendant event rows: %w", err)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate descendant events: %w", err)
		}
	}
	return ids, nil
}

func (s *ConfigStore) ListEnabledWebhookSourcesForTopic(ctx context.Context, topic string) ([]WebhookSource, error) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return nil, fmt.Errorf("topic is required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, enabled, provider, topic_prefix, token_hash, signature_type, signature_secret, body_limit_bytes, created_at, updated_at
		FROM webhook_source WHERE enabled = 1 ORDER BY length(topic_prefix) DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query webhook sources: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]WebhookSource, 0)
	for rows.Next() {
		var item WebhookSource
		var enabled int
		var createdAtRaw int64
		var updatedAtRaw int64
		if err := rows.Scan(&item.ID, &item.Name, &enabled, &item.Provider, &item.TopicPrefix, &item.TokenHash, &item.SignatureType, &item.SignatureSecret, &item.BodyLimitBytes, &createdAtRaw, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan webhook source: %w", err)
		}
		item.Enabled = enabled != 0
		item.CreatedAt = parseStoredUnixTimeAuto(createdAtRaw)
		item.UpdatedAt = parseStoredUnixTimeAuto(updatedAtRaw)
		if webhookSourceTopicMatches(topic, item.TopicPrefix) {
			items = append(items, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate webhook sources: %w", err)
	}
	return items, nil
}

func webhookSourceTopicMatches(topic, topicPrefix string) bool {
	topic = strings.TrimSpace(topic)
	topicPrefix = strings.TrimSpace(topicPrefix)
	if topic == "" || topicPrefix == "" {
		return false
	}
	if strings.HasPrefix(topic, topicPrefix) {
		return true
	}
	return strings.HasSuffix(topicPrefix, ".") && topic == strings.TrimSuffix(topicPrefix, ".")
}

func WebhookSourceTopicMatches(topic, topicPrefix string) bool {
	return webhookSourceTopicMatches(topic, topicPrefix)
}

func (s *ConfigStore) ListWebhookSources(ctx context.Context) ([]WebhookSource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, enabled, provider, topic_prefix, token_hash, signature_type, signature_secret, body_limit_bytes, created_at, updated_at
		FROM webhook_source ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query webhook sources: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]WebhookSource, 0)
	for rows.Next() {
		var item WebhookSource
		var enabled int
		var createdAtRaw int64
		var updatedAtRaw int64
		if err := rows.Scan(&item.ID, &item.Name, &enabled, &item.Provider, &item.TopicPrefix, &item.TokenHash, &item.SignatureType, &item.SignatureSecret, &item.BodyLimitBytes, &createdAtRaw, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan webhook source: %w", err)
		}
		item.Enabled = enabled != 0
		item.CreatedAt = parseStoredUnixTimeAuto(createdAtRaw)
		item.UpdatedAt = parseStoredUnixTimeAuto(updatedAtRaw)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate webhook sources: %w", err)
	}
	return items, nil
}

func (s *ConfigStore) GetWebhookSource(ctx context.Context, sourceID string) (WebhookSource, bool, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return WebhookSource{}, false, fmt.Errorf("webhook source id is required")
	}
	row := s.db.QueryRowContext(ctx, `SELECT id, name, enabled, provider, topic_prefix, token_hash, signature_type, signature_secret, body_limit_bytes, created_at, updated_at
		FROM webhook_source WHERE id = ?`, sourceID)
	var item WebhookSource
	var enabled int
	var createdAtRaw int64
	var updatedAtRaw int64
	if err := row.Scan(&item.ID, &item.Name, &enabled, &item.Provider, &item.TopicPrefix, &item.TokenHash, &item.SignatureType, &item.SignatureSecret, &item.BodyLimitBytes, &createdAtRaw, &updatedAtRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return WebhookSource{}, false, nil
		}
		return WebhookSource{}, false, fmt.Errorf("get webhook source: %w", err)
	}
	item.Enabled = enabled != 0
	item.CreatedAt = parseStoredUnixTimeAuto(createdAtRaw)
	item.UpdatedAt = parseStoredUnixTimeAuto(updatedAtRaw)
	return item, true, nil
}

func (s *ConfigStore) UpsertWebhookSource(ctx context.Context, source WebhookSource) (WebhookSource, error) {
	source.ID = strings.TrimSpace(source.ID)
	source.Name = strings.TrimSpace(source.Name)
	source.Provider = strings.TrimSpace(source.Provider)
	source.TopicPrefix = strings.TrimSpace(source.TopicPrefix)
	source.TokenHash = strings.TrimSpace(source.TokenHash)
	source.SignatureType = strings.TrimSpace(source.SignatureType)
	source.SignatureSecret = strings.TrimSpace(source.SignatureSecret)
	if source.ID == "" || source.TopicPrefix == "" {
		return WebhookSource{}, fmt.Errorf("webhook source id and topic prefix are required")
	}
	if !strings.HasPrefix(source.TopicPrefix, "webhook.") {
		return WebhookSource{}, fmt.Errorf("webhook source topic prefix must use webhook.* prefix")
	}
	if !strings.HasSuffix(source.TopicPrefix, ".") {
		return WebhookSource{}, fmt.Errorf("webhook source topic prefix must end with dot")
	}
	if err := validateTopicEventName(strings.TrimSuffix(source.TopicPrefix, ".")); err != nil {
		return WebhookSource{}, fmt.Errorf("webhook source topic prefix is invalid: %w", err)
	}
	if source.Name == "" {
		source.Name = source.ID
	}
	now := time.Now().UTC()
	if source.CreatedAt.IsZero() {
		source.CreatedAt = now
	}
	source.UpdatedAt = now
	enabled := 0
	if source.Enabled {
		enabled = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO webhook_source(id, name, enabled, provider, topic_prefix, token_hash, signature_type, signature_secret, body_limit_bytes, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, enabled = excluded.enabled, provider = excluded.provider, topic_prefix = excluded.topic_prefix,
			token_hash = excluded.token_hash, signature_type = excluded.signature_type, signature_secret = excluded.signature_secret,
			body_limit_bytes = excluded.body_limit_bytes, updated_at = excluded.updated_at`,
		source.ID,
		source.Name,
		enabled,
		source.Provider,
		source.TopicPrefix,
		source.TokenHash,
		source.SignatureType,
		source.SignatureSecret,
		source.BodyLimitBytes,
		source.CreatedAt.UTC().UnixMilli(),
		source.UpdatedAt.UTC().UnixMilli(),
	)
	if err != nil {
		return WebhookSource{}, fmt.Errorf("upsert webhook source: %w", err)
	}
	return source, nil
}

func (s *ConfigStore) DeleteWebhookSource(ctx context.Context, sourceID string) error {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return fmt.Errorf("webhook source id is required")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM webhook_source WHERE id = ?`, sourceID)
	if err != nil {
		return fmt.Errorf("delete webhook source: %w", err)
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return fmt.Errorf("webhook source not found")
	}
	return nil
}
