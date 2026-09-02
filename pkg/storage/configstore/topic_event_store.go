package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/chaitin/agent-compose/pkg/events"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/storeutil"
	"github.com/chaitin/agent-compose/pkg/storedtime"
	"strings"
	"time"
)

// eventStore owns topic events, webhook sources, deliveries, and session links.
type eventStore struct {
	db *sql.DB
}

func (s *eventStore) GetEvent(ctx context.Context, eventID string) (domain.TopicEventRecord, error) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return domain.TopicEventRecord{}, fmt.Errorf("event id is required")
	}
	row := s.db.QueryRowContext(ctx, selectTopicEventSQL()+` WHERE id = ?`, eventID)
	item, err := scanTopicEvent(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.TopicEventRecord{}, domain.ResourceError(domain.ErrNotFound, "event", eventID, fmt.Sprintf("event %s not found", eventID), err)
		}
		return domain.TopicEventRecord{}, err
	}
	return item, nil
}

func (s *eventStore) FindEventByIdempotencyKey(ctx context.Context, topic, key string) (domain.TopicEventRecord, bool, error) {
	topic = strings.TrimSpace(topic)
	key = strings.TrimSpace(key)
	if topic == "" || key == "" {
		return domain.TopicEventRecord{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, selectTopicEventSQL()+` WHERE topic = ? AND idempotency_key = ?`, topic, key)
	item, err := scanTopicEvent(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.TopicEventRecord{}, false, nil
		}
		return domain.TopicEventRecord{}, false, err
	}
	return item, true, nil
}

func (s *eventStore) ListPendingEvents(ctx context.Context, limit int) ([]domain.TopicEventRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, selectTopicEventSQL()+` WHERE dispatch_status = ? ORDER BY sequence ASC LIMIT ?`, domain.TopicEventDispatchPending, limit)
	if err != nil {
		return nil, fmt.Errorf("query pending events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanTopicEvents(rows)
}

func (s *eventStore) ListEvents(ctx context.Context, filter domain.TopicEventFilter) ([]domain.TopicEventRecord, int, error) {
	query, err := buildEventListQuery(filter)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.countEventList(ctx, query)
	if err != nil {
		return nil, 0, err
	}
	args := append(append([]any(nil), query.filterArgs...), query.pageArgs...)
	rows, err := s.db.QueryContext(ctx, selectTopicEventSQL()+query.where+query.page, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items, err := scanTopicEvents(rows)
	return items, total, err
}

func (s *eventStore) MarkEventPublished(ctx context.Context, eventID, claimID string, dispatchedAt time.Time) error {
	eventID = strings.TrimSpace(eventID)
	claimID = strings.TrimSpace(claimID)
	if eventID == "" || claimID == "" {
		return fmt.Errorf("event id and claim id are required")
	}
	if dispatchedAt.IsZero() {
		dispatchedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `UPDATE event SET dispatch_status = ?, dispatched_at = ?, claim_id = '', claim_until = 0, last_error = '' WHERE id = ? AND claim_id = ?`,
		domain.TopicEventDispatchPublishedToBus, dispatchedAt.UTC().UnixMilli(), eventID, claimID)
	if err != nil {
		return fmt.Errorf("mark event published: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read event update count: %w", err)
	}
	if affected == 0 {
		if _, err := s.GetEvent(ctx, eventID); err != nil {
			return domain.ResourceError(domain.ErrNotFound, "event", eventID, fmt.Sprintf("event %s not found", eventID), err)
		}
		return nil
	}
	return nil
}

func (s *eventStore) UpdateEventPayload(ctx context.Context, eventID, payloadJSON string) error {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return fmt.Errorf("event id is required")
	}
	payloadJSON = strings.TrimSpace(payloadJSON)
	if payloadJSON == "" {
		return fmt.Errorf("event payload json is required")
	}
	if _, err := domain.NormalizeJSONDocument(payloadJSON); err != nil {
		return err
	}
	payloadHash := events.PayloadSHA256(payloadJSON)
	result, err := s.db.ExecContext(ctx, `UPDATE event SET payload_hash = ?, payload_json = ? WHERE id = ?`, payloadHash, payloadJSON, eventID)
	if err != nil {
		return fmt.Errorf("update event payload: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read event payload update count: %w", err)
	}
	if affected == 0 {
		return domain.ResourceError(domain.ErrNotFound, "event", eventID, fmt.Sprintf("event %s not found", eventID), nil)
	}
	return nil
}

func (s *eventStore) ListDispatchableEvents(ctx context.Context, now time.Time, limit int) ([]domain.TopicEventRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	nowMillis := now.UTC().UnixMilli()
	rows, err := s.db.QueryContext(ctx, selectTopicEventSQL()+` WHERE dispatch_status IN (?, ?, ?) AND (next_attempt_at = 0 OR next_attempt_at <= ?) AND (claim_until = 0 OR claim_until <= ?) ORDER BY sequence ASC LIMIT ?`,
		domain.TopicEventDispatchPending,
		domain.TopicEventDispatchRetrying,
		domain.TopicEventDispatchPublishing,
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

func (s *eventStore) ClaimEvent(ctx context.Context, eventID, claimID string, now, until time.Time) (bool, error) {
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
		domain.TopicEventDispatchPublishing,
		claimID,
		until.UTC().UnixMilli(),
		eventID,
		domain.TopicEventDispatchPending,
		domain.TopicEventDispatchRetrying,
		domain.TopicEventDispatchPublishing,
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

func (s *eventStore) ReleaseEventClaim(ctx context.Context, req events.ReleaseEventClaimRequest) error {
	eventID := strings.TrimSpace(req.EventID)
	claimID := strings.TrimSpace(req.ClaimID)
	status := events.NormalizeDispatchStatus(req.Status)
	if eventID == "" || claimID == "" || status == "" {
		return fmt.Errorf("event claim release requires event, claim, and status")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE event SET dispatch_status = ?, claim_id = '', claim_until = 0, next_attempt_at = ?, last_error = ? WHERE id = ? AND claim_id = ?`,
		status,
		domain.NonZeroTimeUnixMilli(req.NextAttemptAt),
		strings.TrimSpace(req.LastError),
		eventID,
		claimID,
	)
	if err != nil {
		return fmt.Errorf("release event claim: %w", err)
	}
	return nil
}

func (s *eventStore) MarkEventNoSubscriber(ctx context.Context, eventID, claimID string, dispatchedAt time.Time) error {
	eventID = strings.TrimSpace(eventID)
	claimID = strings.TrimSpace(claimID)
	if eventID == "" || claimID == "" {
		return fmt.Errorf("event id and claim id are required")
	}
	if dispatchedAt.IsZero() {
		dispatchedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `UPDATE event SET dispatch_status = ?, dispatched_at = ?, claim_id = '', claim_until = 0, last_error = '' WHERE id = ? AND claim_id = ?`,
		domain.TopicEventDispatchNoSubscriber,
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
			return domain.ResourceError(domain.ErrNotFound, "event", eventID, fmt.Sprintf("event %s not found", eventID), err)
		}
		return nil
	}
	return nil
}

func (s *eventStore) UpsertEventDelivery(ctx context.Context, delivery domain.EventDelivery) error {
	delivery.EventID = strings.TrimSpace(delivery.EventID)
	delivery.SchedulerID = strings.TrimSpace(delivery.SchedulerID)
	delivery.TriggerID = strings.TrimSpace(delivery.TriggerID)
	delivery.RunID = strings.TrimSpace(delivery.RunID)
	delivery.Status = events.NormalizeDeliveryStatus(delivery.Status)
	delivery.Error = strings.TrimSpace(delivery.Error)
	if delivery.EventID == "" || delivery.SchedulerID == "" || delivery.TriggerID == "" || delivery.Status == "" {
		return fmt.Errorf("event delivery requires event, scheduler, trigger, and status")
	}
	now := time.Now().UTC()
	if delivery.CreatedAt.IsZero() {
		delivery.CreatedAt = now
	}
	if delivery.UpdatedAt.IsZero() {
		delivery.UpdatedAt = now
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO event_delivery(event_id, scheduler_id, trigger_id, scheduler_run_id, status, error, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(event_id, scheduler_id, trigger_id) DO UPDATE SET
			scheduler_run_id = CASE WHEN excluded.scheduler_run_id != '' THEN excluded.scheduler_run_id ELSE event_delivery.scheduler_run_id END,
			status = CASE
				WHEN excluded.status = ? AND excluded.scheduler_run_id = '' AND event_delivery.scheduler_run_id != '' THEN event_delivery.status
				ELSE excluded.status
			END,
			error = CASE
				WHEN excluded.status = ? AND excluded.scheduler_run_id = '' AND event_delivery.scheduler_run_id != '' THEN event_delivery.error
				ELSE excluded.error
			END,
			updated_at = excluded.updated_at`,
		delivery.EventID,
		delivery.SchedulerID,
		delivery.TriggerID,
		delivery.RunID,
		delivery.Status,
		delivery.Error,
		delivery.CreatedAt.UTC().UnixMilli(),
		delivery.UpdatedAt.UTC().UnixMilli(),
		domain.EventDeliveryStatusMatched,
		domain.EventDeliveryStatusMatched,
	)
	if err != nil {
		return fmt.Errorf("upsert event delivery: %w", err)
	}
	return nil
}

func (s *eventStore) AddEventSandboxLink(ctx context.Context, link domain.EventSandboxLink) error {
	link.EventID = strings.TrimSpace(link.EventID)
	link.SandboxID = strings.TrimSpace(link.SandboxID)
	link.Relation = strings.TrimSpace(link.Relation)
	link.SchedulerID = strings.TrimSpace(link.SchedulerID)
	link.RunID = strings.TrimSpace(link.RunID)
	link.TriggerID = strings.TrimSpace(link.TriggerID)
	link.SchedulerEventID = strings.TrimSpace(link.SchedulerEventID)
	if link.EventID == "" || link.SandboxID == "" || link.Relation == "" {
		return fmt.Errorf("event sandbox link requires event, sandbox, and relation")
	}
	if link.CreatedAt.IsZero() {
		link.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO event_sandbox_link(event_id, sandbox_id, relation, scheduler_id, scheduler_run_id, trigger_id, scheduler_event_id, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		link.EventID,
		link.SandboxID,
		link.Relation,
		link.SchedulerID,
		link.RunID,
		link.TriggerID,
		link.SchedulerEventID,
		link.CreatedAt.UTC().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("insert event sandbox link: %w", err)
	}
	return nil
}

// CancelEventDispatch withdraws the given events from the dispatch queue so
// they never start a run. Publishing events whose lease expired are waiting
// again, because ListDispatchableEvents and ClaimEvent can reclaim them. Only
// publishing events with an active lease are left alone and reported in flight.
func (s *eventStore) CancelEventDispatch(ctx context.Context, eventIDs []string, reason string) (domain.EventDispatchCancellation, error) {
	ids := dedupedEventIDs(eventIDs)
	if len(ids) == 0 {
		return domain.EventDispatchCancellation{}, nil
	}
	placeholders := make([]string, len(ids))
	for i := range ids {
		placeholders[i] = "?"
	}
	idList := strings.Join(placeholders, ",")

	nowMillis := time.Now().UTC().UnixMilli()
	args := make([]any, 0, len(ids)+5)
	args = append(args, domain.TopicEventDispatchCanceled, strings.TrimSpace(reason))
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, domain.TopicEventDispatchPending, domain.TopicEventDispatchRetrying,
		domain.TopicEventDispatchPublishing, nowMillis)
	result, err := s.db.ExecContext(ctx, `UPDATE event
		SET dispatch_status = ?, last_error = ?, claim_id = '', claim_until = 0, next_attempt_at = 0
		WHERE id IN (`+idList+`) AND (
			dispatch_status IN (?, ?) OR
			(dispatch_status = ? AND (claim_until = 0 OR claim_until <= ?))
		)`, args...)
	if err != nil {
		return domain.EventDispatchCancellation{}, fmt.Errorf("cancel event dispatch: %w", err)
	}
	canceled, err := result.RowsAffected()
	if err != nil {
		return domain.EventDispatchCancellation{}, fmt.Errorf("read canceled event count: %w", err)
	}

	countArgs := make([]any, 0, len(ids)+2)
	for _, id := range ids {
		countArgs = append(countArgs, id)
	}
	countArgs = append(countArgs, domain.TopicEventDispatchPublishing, nowMillis)
	var inFlight int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event
		WHERE id IN (`+idList+`) AND dispatch_status = ? AND claim_until > ?`, countArgs...).Scan(&inFlight); err != nil {
		return domain.EventDispatchCancellation{}, fmt.Errorf("count in-flight events: %w", err)
	}
	return domain.EventDispatchCancellation{Canceled: int(canceled), InFlight: inFlight}, nil
}

// dedupedEventIDs trims, drops empties, and removes duplicates while keeping
// the caller's order.
func dedupedEventIDs(eventIDs []string) []string {
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
	return ids
}

func (s *eventStore) ListEventDeliveries(ctx context.Context, eventIDs []string) ([]domain.EventDelivery, error) {
	ids := dedupedEventIDs(eventIDs)
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT event_id, scheduler_id, trigger_id, scheduler_run_id, status, error, created_at, updated_at
		FROM event_delivery WHERE event_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY updated_at ASC, scheduler_id ASC, trigger_id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query event deliveries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]domain.EventDelivery, 0)
	for rows.Next() {
		var item domain.EventDelivery
		var createdAtRaw int64
		var updatedAtRaw int64
		if err := rows.Scan(&item.EventID, &item.SchedulerID, &item.TriggerID, &item.RunID, &item.Status, &item.Error, &createdAtRaw, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan event delivery: %w", err)
		}
		item.CreatedAt = storedtime.ParseStoredUnixTimeAuto(createdAtRaw)
		item.UpdatedAt = storedtime.ParseStoredUnixTimeAuto(updatedAtRaw)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate event deliveries: %w", err)
	}
	return items, nil
}

func (s *eventStore) ListEventSandboxLinks(ctx context.Context, eventIDs []string) ([]domain.EventSandboxTraceItem, error) {
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
	rows, err := s.db.QueryContext(ctx, `SELECT event_id, sandbox_id, relation, scheduler_id, scheduler_run_id, trigger_id, scheduler_event_id, created_at
		FROM event_sandbox_link WHERE event_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY created_at ASC, sandbox_id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query event sandbox links: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]domain.EventSandboxTraceItem, 0)
	for rows.Next() {
		var item domain.EventSandboxTraceItem
		var createdAtRaw int64
		if err := rows.Scan(&item.EventID, &item.SandboxID, &item.Relation, &item.SchedulerID, &item.RunID, &item.TriggerID, &item.SchedulerEventID, &createdAtRaw); err != nil {
			return nil, fmt.Errorf("scan event sandbox link: %w", err)
		}
		item.CreatedAt = storedtime.ParseStoredUnixTimeAuto(createdAtRaw)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate event sandbox links: %w", err)
	}
	return items, nil
}

func (s *eventStore) ListDescendantEventIDs(ctx context.Context, rootEventID string, limit int) ([]string, error) {
	rootEventID = strings.TrimSpace(rootEventID)
	if rootEventID == "" {
		return nil, fmt.Errorf("event id is required")
	}
	if limit <= 0 {
		limit = 1000
	}
	ids := []string{rootEventID}
	seen := map[string]struct{}{rootEventID: {}}
	queue := []string{rootEventID}
	for len(queue) > 0 && len(ids) < limit {
		parent := queue[0]
		queue = queue[1:]
		children, err := s.childEventIDs(ctx, parent)
		if err != nil {
			return nil, err
		}
		for _, id := range children {
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
	}
	return ids, nil
}

// childEventIDs reads the direct children of one event. The traversal above
// visits many parents, so the cursor is owned by this call rather than deferred
// to the end of the walk.
func (s *eventStore) childEventIDs(ctx context.Context, parent string) (_ []string, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM event WHERE parent_event_id = ? ORDER BY sequence ASC`, parent)
	if err != nil {
		return nil, fmt.Errorf("query descendant events: %w", err)
	}
	defer func() { storeutil.ReportClose(rows.Close(), &err, "descendant event rows") }()
	var children []string
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, fmt.Errorf("scan descendant event: %w", scanErr)
		}
		children = append(children, id)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("iterate descendant events: %w", rowsErr)
	}
	return children, nil
}

func (s *eventStore) ListEnabledWebhookSourcesForTopic(ctx context.Context, topic string) ([]domain.WebhookSource, error) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return nil, fmt.Errorf("topic is required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, enabled, provider, topic_prefix, token_hash, token_header, signature_type, signature_secret, body_limit_bytes, created_at, updated_at
		FROM webhook_source WHERE enabled = 1 ORDER BY length(topic_prefix) DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query webhook sources: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]domain.WebhookSource, 0)
	for rows.Next() {
		var item domain.WebhookSource
		var enabled int
		var createdAtRaw int64
		var updatedAtRaw int64
		if err := rows.Scan(&item.ID, &item.Name, &enabled, &item.Provider, &item.TopicPrefix, &item.TokenHash, &item.TokenHeader, &item.SignatureType, &item.SignatureSecret, &item.BodyLimitBytes, &createdAtRaw, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan webhook source: %w", err)
		}
		item.Enabled = enabled != 0
		item.CreatedAt = storedtime.ParseStoredUnixTimeAuto(createdAtRaw)
		item.UpdatedAt = storedtime.ParseStoredUnixTimeAuto(updatedAtRaw)
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

func (s *eventStore) ListWebhookSources(ctx context.Context) ([]domain.WebhookSource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, enabled, provider, topic_prefix, token_hash, token_header, signature_type, signature_secret, body_limit_bytes, created_at, updated_at
		FROM webhook_source ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query webhook sources: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]domain.WebhookSource, 0)
	for rows.Next() {
		var item domain.WebhookSource
		var enabled int
		var createdAtRaw int64
		var updatedAtRaw int64
		if err := rows.Scan(&item.ID, &item.Name, &enabled, &item.Provider, &item.TopicPrefix, &item.TokenHash, &item.TokenHeader, &item.SignatureType, &item.SignatureSecret, &item.BodyLimitBytes, &createdAtRaw, &updatedAtRaw); err != nil {
			return nil, fmt.Errorf("scan webhook source: %w", err)
		}
		item.Enabled = enabled != 0
		item.CreatedAt = storedtime.ParseStoredUnixTimeAuto(createdAtRaw)
		item.UpdatedAt = storedtime.ParseStoredUnixTimeAuto(updatedAtRaw)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate webhook sources: %w", err)
	}
	return items, nil
}

func (s *eventStore) GetWebhookSource(ctx context.Context, sourceID string) (domain.WebhookSource, bool, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return domain.WebhookSource{}, false, fmt.Errorf("webhook source id is required")
	}
	row := s.db.QueryRowContext(ctx, `SELECT id, name, enabled, provider, topic_prefix, token_hash, token_header, signature_type, signature_secret, body_limit_bytes, created_at, updated_at
		FROM webhook_source WHERE id = ?`, sourceID)
	var item domain.WebhookSource
	var enabled int
	var createdAtRaw int64
	var updatedAtRaw int64
	if err := row.Scan(&item.ID, &item.Name, &enabled, &item.Provider, &item.TopicPrefix, &item.TokenHash, &item.TokenHeader, &item.SignatureType, &item.SignatureSecret, &item.BodyLimitBytes, &createdAtRaw, &updatedAtRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.WebhookSource{}, false, nil
		}
		return domain.WebhookSource{}, false, fmt.Errorf("get webhook source: %w", err)
	}
	item.Enabled = enabled != 0
	item.CreatedAt = storedtime.ParseStoredUnixTimeAuto(createdAtRaw)
	item.UpdatedAt = storedtime.ParseStoredUnixTimeAuto(updatedAtRaw)
	return item, true, nil
}

func (s *eventStore) UpsertWebhookSource(ctx context.Context, source domain.WebhookSource) (domain.WebhookSource, error) {
	source.ID = strings.TrimSpace(source.ID)
	source.Name = strings.TrimSpace(source.Name)
	source.Provider = strings.TrimSpace(source.Provider)
	source.TopicPrefix = strings.TrimSpace(source.TopicPrefix)
	source.TokenHash = strings.TrimSpace(source.TokenHash)
	tokenHeader, err := events.NormalizeHTTPHeaderName(source.TokenHeader)
	if err != nil {
		return domain.WebhookSource{}, fmt.Errorf("webhook source token header is invalid: %w", err)
	}
	source.TokenHeader = tokenHeader
	source.SignatureType = strings.TrimSpace(source.SignatureType)
	source.SignatureSecret = strings.TrimSpace(source.SignatureSecret)
	if _, err := domain.GitHubWebhookModeForSource(source); err != nil {
		return domain.WebhookSource{}, err
	}
	if source.ID == "" || source.TopicPrefix == "" {
		return domain.WebhookSource{}, fmt.Errorf("webhook source id and topic prefix are required")
	}
	if !strings.HasPrefix(source.TopicPrefix, "webhook.") {
		return domain.WebhookSource{}, fmt.Errorf("webhook source topic prefix must use webhook.* prefix")
	}
	if !strings.HasSuffix(source.TopicPrefix, ".") {
		return domain.WebhookSource{}, fmt.Errorf("webhook source topic prefix must end with dot")
	}
	if err := events.ValidateTopicName(strings.TrimSuffix(source.TopicPrefix, ".")); err != nil {
		return domain.WebhookSource{}, fmt.Errorf("webhook source topic prefix is invalid: %w", err)
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
	_, err = s.db.ExecContext(ctx, `INSERT INTO webhook_source(id, name, enabled, provider, topic_prefix, token_hash, token_header, signature_type, signature_secret, body_limit_bytes, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, enabled = excluded.enabled, provider = excluded.provider, topic_prefix = excluded.topic_prefix,
			token_hash = excluded.token_hash, token_header = excluded.token_header, signature_type = excluded.signature_type, signature_secret = excluded.signature_secret,
			body_limit_bytes = excluded.body_limit_bytes, updated_at = excluded.updated_at`,
		source.ID,
		source.Name,
		enabled,
		source.Provider,
		source.TopicPrefix,
		source.TokenHash,
		source.TokenHeader,
		source.SignatureType,
		source.SignatureSecret,
		source.BodyLimitBytes,
		source.CreatedAt.UTC().UnixMilli(),
		source.UpdatedAt.UTC().UnixMilli(),
	)
	if err != nil {
		return domain.WebhookSource{}, fmt.Errorf("upsert webhook source: %w", err)
	}
	return source, nil
}

func (s *eventStore) DeleteWebhookSource(ctx context.Context, sourceID string) error {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return fmt.Errorf("webhook source id is required")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM webhook_source WHERE id = ?`, sourceID)
	if err != nil {
		return fmt.Errorf("delete webhook source: %w", err)
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return domain.ResourceError(domain.ErrNotFound, "webhook source", sourceID, "webhook source not found", nil)
	}
	return nil
}
