package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/event"
	"github.com/chaitin/agent-compose/ent/dao/eventdelivery"
	"github.com/chaitin/agent-compose/ent/dao/eventsandboxlink"
	"github.com/chaitin/agent-compose/ent/dao/webhooksource"
	"github.com/chaitin/agent-compose/pkg/events"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/dialect"
	"github.com/chaitin/agent-compose/pkg/storedtime"
)

// eventStore owns topic events, webhook sources, deliveries, and session links.
type eventStore struct {
	db      *sql.DB
	dialect dialect.Dialect
	client  *dao.Client
}

func (s *eventStore) GetEvent(ctx context.Context, eventID string) (domain.TopicEventRecord, error) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return domain.TopicEventRecord{}, fmt.Errorf("event id is required")
	}
	entity, err := s.client.Event.Query().Where(event.EventIDEQ(eventID)).Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return domain.TopicEventRecord{}, domain.ResourceError(domain.ErrNotFound, "event", eventID, fmt.Sprintf("event %s not found", eventID), sql.ErrNoRows)
		}
		return domain.TopicEventRecord{}, err
	}
	return topicEventFromEntity(entity), nil
}

func (s *eventStore) FindEventByIdempotencyKey(ctx context.Context, topic, key string) (domain.TopicEventRecord, bool, error) {
	topic = strings.TrimSpace(topic)
	key = strings.TrimSpace(key)
	if topic == "" || key == "" {
		return domain.TopicEventRecord{}, false, nil
	}
	entity, err := s.client.Event.Query().
		Where(event.TopicEQ(topic), event.IdempotencyKeyEQ(key)).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return domain.TopicEventRecord{}, false, nil
		}
		return domain.TopicEventRecord{}, false, err
	}
	return topicEventFromEntity(entity), true, nil
}

func (s *eventStore) ListPendingEvents(ctx context.Context, limit int) ([]domain.TopicEventRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	entities, err := s.client.Event.Query().
		Where(event.DispatchStatusEQ(domain.TopicEventDispatchPending)).
		Order(event.ByID()).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query pending events: %w", err)
	}
	return topicEventsFromEntities(entities), nil
}

func (s *eventStore) ListEvents(ctx context.Context, filter domain.TopicEventFilter) ([]domain.TopicEventRecord, int, error) {
	list, err := buildEventListFilter(filter)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.countEventList(ctx, list)
	if err != nil {
		return nil, 0, err
	}
	entities, err := eventPageQuery(s.client, list).All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("query events: %w", err)
	}
	return topicEventsFromEntities(entities), total, nil
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
	affected, err := s.client.Event.Update().
		Where(event.EventIDEQ(eventID), event.ClaimIDEQ(claimID)).
		SetDispatchStatus(domain.TopicEventDispatchPublishedToBus).
		SetDispatchedAt(dispatchedAt.UTC().UnixMilli()).
		SetClaimID("").
		SetClaimUntil(0).
		SetLastError("").
		Save(ctx)
	if err != nil {
		return fmt.Errorf("mark event published: %w", err)
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
	affected, err := s.client.Event.Update().
		Where(event.EventIDEQ(eventID)).
		SetPayloadHash(payloadHash).
		SetPayloadJSON(payloadJSON).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("update event payload: %w", err)
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
	entities, err := s.client.Event.Query().
		Where(
			event.DispatchStatusIn(
				domain.TopicEventDispatchPending,
				domain.TopicEventDispatchRetrying,
				domain.TopicEventDispatchPublishing,
			),
			event.Or(event.NextAttemptAtEQ(0), event.NextAttemptAtLTE(nowMillis)),
			event.Or(event.ClaimUntilEQ(0), event.ClaimUntilLTE(nowMillis)),
		).
		Order(event.ByID()).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query dispatchable events: %w", err)
	}
	return topicEventsFromEntities(entities), nil
}

func (s *eventStore) ClaimEvent(ctx context.Context, eventID, claimID string, now, until time.Time) (bool, error) {
	eventID = strings.TrimSpace(eventID)
	claimID = strings.TrimSpace(claimID)
	if eventID == "" || claimID == "" {
		return false, fmt.Errorf("event claim id is required")
	}
	nowMillis := now.UTC().UnixMilli()
	affected, err := s.client.Event.Update().
		Where(
			event.EventIDEQ(eventID),
			event.DispatchStatusIn(
				domain.TopicEventDispatchPending,
				domain.TopicEventDispatchRetrying,
				domain.TopicEventDispatchPublishing,
			),
			event.Or(event.NextAttemptAtEQ(0), event.NextAttemptAtLTE(nowMillis)),
			event.Or(event.ClaimUntilEQ(0), event.ClaimUntilLTE(nowMillis)),
		).
		SetDispatchStatus(domain.TopicEventDispatchPublishing).
		SetClaimID(claimID).
		SetClaimUntil(until.UTC().UnixMilli()).
		AddAttemptCount(1).
		SetLastError("").
		Save(ctx)
	if err != nil {
		return false, fmt.Errorf("claim event: %w", err)
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
	if _, err := s.client.Event.Update().
		Where(event.EventIDEQ(eventID), event.ClaimIDEQ(claimID)).
		SetDispatchStatus(status).
		SetClaimID("").
		SetClaimUntil(0).
		SetNextAttemptAt(domain.NonZeroTimeUnixMilli(req.NextAttemptAt)).
		SetLastError(strings.TrimSpace(req.LastError)).
		Save(ctx); err != nil {
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
	affected, err := s.client.Event.Update().
		Where(event.EventIDEQ(eventID), event.ClaimIDEQ(claimID)).
		SetDispatchStatus(domain.TopicEventDispatchNoSubscriber).
		SetDispatchedAt(dispatchedAt.UTC().UnixMilli()).
		SetClaimID("").
		SetClaimUntil(0).
		SetLastError("").
		Save(ctx)
	if err != nil {
		return fmt.Errorf("mark event no subscriber: %w", err)
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
	// Kept as SQL: the conflict update reads both the proposed row and the
	// stored row in CASE expressions (scheduler_run_id and status/error
	// retention); ent's upsert setters cannot express that condition.
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
	if err := s.client.EventSandboxLink.Create().
		SetID(link.EventID).
		SetSandboxID(link.SandboxID).
		SetRelation(link.Relation).
		SetSchedulerID(link.SchedulerID).
		SetSchedulerRunID(link.RunID).
		SetTriggerID(link.TriggerID).
		SetSchedulerEventID(link.SchedulerEventID).
		SetCreatedAt(link.CreatedAt.UTC().UnixMilli()).
		OnConflict(
			entsql.ConflictColumns(
				eventsandboxlink.FieldID,
				eventsandboxlink.FieldSandboxID,
				eventsandboxlink.FieldRelation,
				eventsandboxlink.FieldSchedulerRunID,
			),
			entsql.ResolveWithIgnore(),
		).
		Exec(ctx); err != nil {
		return fmt.Errorf("insert event sandbox link: %w", err)
	}
	return nil
}

// CancelEventDispatch withdraws the given events from the dispatch queue so
// they never start a run. Only waiting events are withdrawn:
// ListDispatchableEvents and ClaimEvent both admit pending, retrying, and
// publishing_to_bus, so the canceled status alone removes an event from both.
//
// Publishing events are left in flight, expired claims included: the original
// worker may still be creating a delivery, and clearing its claim could leave
// that run alive with nobody to stop it. They are reported instead, split by
// whether the claim still holds. The split matters to the caller: an in-flight
// claim produces its run in a moment, so retrying the stop request shortly
// stops it, while a stale claim waits for another dispatch loop to reclaim the
// event, so an immediate retry would find nothing to stop.
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

	args := make([]any, 0, len(ids)+4)
	args = append(args, domain.TopicEventDispatchCanceled, strings.TrimSpace(reason))
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, domain.TopicEventDispatchPending, domain.TopicEventDispatchRetrying)
	// Kept as SQL: the IN list is built dynamically and the reset must report
	// the affected count; ent cannot carry the builder without losing either.
	result, err := s.db.ExecContext(ctx, `UPDATE event
		SET dispatch_status = ?, last_error = ?, claim_id = '', claim_until = 0, next_attempt_at = 0
		WHERE id IN (`+idList+`) AND dispatch_status IN (?, ?)`, args...)
	if err != nil {
		return domain.EventDispatchCancellation{}, fmt.Errorf("cancel event dispatch: %w", err)
	}
	canceled, err := result.RowsAffected()
	if err != nil {
		return domain.EventDispatchCancellation{}, fmt.Errorf("read canceled event count: %w", err)
	}

	// A claim_until of 0 counts as stale: ListDispatchableEvents admits it, so
	// the event is queued for another loop rather than held by a live worker.
	countArgs := make([]any, 0, len(ids)+3)
	countArgs = append(countArgs, nowMillis, nowMillis)
	for _, id := range ids {
		countArgs = append(countArgs, id)
	}
	countArgs = append(countArgs, domain.TopicEventDispatchPublishing)
	var inFlight, stale int
	// Kept as SQL: the CASE aggregate splits publishing claims by claim_until.
	if err := s.db.QueryRowContext(ctx, `SELECT
			COALESCE(SUM(CASE WHEN claim_until >  ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN claim_until <= ? THEN 1 ELSE 0 END), 0)
		FROM event WHERE id IN (`+idList+`) AND dispatch_status = ?`,
		countArgs...).Scan(&inFlight, &stale); err != nil {
		return domain.EventDispatchCancellation{}, fmt.Errorf("count in-flight events: %w", err)
	}
	return domain.EventDispatchCancellation{Canceled: int(canceled), InFlight: inFlight, Stale: stale}, nil
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
	entities, err := s.client.EventDelivery.Query().
		Where(eventdelivery.IDIn(ids...)).
		Order(
			eventdelivery.ByUpdatedAt(),
			eventdelivery.BySchedulerID(),
			eventdelivery.ByTriggerID(),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query event deliveries: %w", err)
	}
	items := make([]domain.EventDelivery, 0, len(entities))
	for _, entity := range entities {
		items = append(items, eventDeliveryFromEntity(entity))
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
	entities, err := s.client.EventSandboxLink.Query().
		Where(eventsandboxlink.IDIn(ids...)).
		Order(eventsandboxlink.ByCreatedAt(), eventsandboxlink.BySandboxID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query event sandbox links: %w", err)
	}
	items := make([]domain.EventSandboxTraceItem, 0, len(entities))
	for _, entity := range entities {
		items = append(items, eventSandboxLinkFromEntity(entity))
	}
	return items, nil
}

// maxDescendantEventIDs bounds one descendant walk. Callers pass their own
// limit -- the stop endpoint probes with its cap plus one to detect an
// oversized tree -- and this ceiling only keeps a caller that asks for
// everything from walking an unbounded tree one query per node.
const maxDescendantEventIDs = 10000

func (s *eventStore) ListDescendantEventIDs(ctx context.Context, rootEventID string, limit int) ([]string, error) {
	rootEventID = strings.TrimSpace(rootEventID)
	if rootEventID == "" {
		return nil, fmt.Errorf("event id is required")
	}
	if limit <= 0 || limit > maxDescendantEventIDs {
		limit = maxDescendantEventIDs
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

// ListCorrelatedEventIDs returns the IDs of every event sharing eventID's
// correlation_id, eventID included. A webhook forwarder that POSTs a
// follow-up event into a separate topic never sets parent_event_id on it, so
// correlation_id is the only link between such an event and the one that
// triggered it; ListDescendantEventIDs alone would miss it.
func (s *eventStore) ListCorrelatedEventIDs(ctx context.Context, eventID string, limit int) ([]string, error) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return nil, fmt.Errorf("event id is required")
	}
	if limit <= 0 || limit > maxDescendantEventIDs {
		limit = maxDescendantEventIDs
	}
	entity, err := s.client.Event.Query().
		Select(event.FieldCorrelationID).
		Where(event.EventIDEQ(eventID)).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return nil, domain.ResourceError(domain.ErrNotFound, "event", eventID, fmt.Sprintf("event %s not found", eventID), sql.ErrNoRows)
		}
		return nil, fmt.Errorf("load event correlation id: %w", err)
	}
	correlationID := strings.TrimSpace(entity.CorrelationID)
	if correlationID == "" {
		return []string{eventID}, nil
	}
	entities, err := s.client.Event.Query().
		Select(event.FieldEventID).
		Where(event.CorrelationIDEQ(correlationID)).
		Order(event.ByID()).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query correlated events: %w", err)
	}
	ids := make([]string, 0, limit)
	for _, entity := range entities {
		ids = append(ids, entity.EventID)
	}
	return ids, nil
}

// childEventIDs reads the direct children of one event. The traversal above
// visits many parents, so each call keeps its own result set rather than
// sharing a cursor to the end of the walk.
func (s *eventStore) childEventIDs(ctx context.Context, parent string) ([]string, error) {
	entities, err := s.client.Event.Query().
		Select(event.FieldEventID).
		Where(event.ParentEventID(parent)).
		Order(event.ByID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query descendant events: %w", err)
	}
	var children []string
	for _, entity := range entities {
		children = append(children, entity.EventID)
	}
	return children, nil
}

func (s *eventStore) ListEnabledWebhookSourcesForTopic(ctx context.Context, topic string) ([]domain.WebhookSource, error) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return nil, fmt.Errorf("topic is required")
	}
	entities, err := s.client.WebhookSource.Query().
		Where(webhooksource.EnabledEQ(1)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query webhook sources: %w", err)
	}
	items := make([]domain.WebhookSource, 0, len(entities))
	for _, entity := range entities {
		item := webhookSourceFromEntity(entity)
		if webhookSourceTopicMatches(topic, item.TopicPrefix) {
			items = append(items, item)
		}
	}
	// Selecting a topic first means the longest matching prefix wins. The
	// released query ordered in SQL by length(topic_prefix) DESC, id ASC; the
	// same order is applied here over the already-filtered rows.
	sort.Slice(items, func(i, j int) bool {
		left, right := utf8.RuneCountInString(items[i].TopicPrefix), utf8.RuneCountInString(items[j].TopicPrefix)
		if left != right {
			return left > right
		}
		return items[i].ID < items[j].ID
	})
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
	entities, err := s.client.WebhookSource.Query().
		Order(webhooksource.ByID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query webhook sources: %w", err)
	}
	items := make([]domain.WebhookSource, 0, len(entities))
	for _, entity := range entities {
		items = append(items, webhookSourceFromEntity(entity))
	}
	return items, nil
}

func (s *eventStore) GetWebhookSource(ctx context.Context, sourceID string) (domain.WebhookSource, bool, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return domain.WebhookSource{}, false, fmt.Errorf("webhook source id is required")
	}
	entity, err := s.client.WebhookSource.Query().
		Where(webhooksource.IDEQ(sourceID)).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return domain.WebhookSource{}, false, nil
		}
		return domain.WebhookSource{}, false, fmt.Errorf("get webhook source: %w", err)
	}
	return webhookSourceFromEntity(entity), true, nil
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
	if err := s.client.WebhookSource.Create().
		SetID(source.ID).
		SetName(source.Name).
		SetEnabled(enabled).
		SetProvider(source.Provider).
		SetTopicPrefix(source.TopicPrefix).
		SetTokenHash(source.TokenHash).
		SetTokenHeader(source.TokenHeader).
		SetSignatureType(source.SignatureType).
		SetSignatureSecret(source.SignatureSecret).
		SetBodyLimitBytes(source.BodyLimitBytes).
		SetCreatedAt(source.CreatedAt.UTC().UnixMilli()).
		SetUpdatedAt(source.UpdatedAt.UTC().UnixMilli()).
		OnConflict(entsql.ConflictColumns(webhooksource.FieldID)).
		Update(func(u *dao.WebhookSourceUpsert) {
			u.SetName(source.Name)
			u.SetEnabled(enabled)
			u.SetProvider(source.Provider)
			u.SetTopicPrefix(source.TopicPrefix)
			u.SetTokenHash(source.TokenHash)
			u.SetTokenHeader(source.TokenHeader)
			u.SetSignatureType(source.SignatureType)
			u.SetSignatureSecret(source.SignatureSecret)
			u.SetBodyLimitBytes(source.BodyLimitBytes)
			u.SetUpdatedAt(source.UpdatedAt.UTC().UnixMilli())
		}).
		Exec(ctx); err != nil {
		return domain.WebhookSource{}, fmt.Errorf("upsert webhook source: %w", err)
	}
	return source, nil
}

func (s *eventStore) DeleteWebhookSource(ctx context.Context, sourceID string) error {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return fmt.Errorf("webhook source id is required")
	}
	affected, err := s.client.WebhookSource.Delete().
		Where(webhooksource.IDEQ(sourceID)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete webhook source: %w", err)
	}
	if affected == 0 {
		return domain.ResourceError(domain.ErrNotFound, "webhook source", sourceID, "webhook source not found", nil)
	}
	return nil
}

// topicEventsFromEntities maps event rows exactly like scanTopicEvents did.
func topicEventsFromEntities(entities []*dao.Event) []domain.TopicEventRecord {
	items := make([]domain.TopicEventRecord, 0, len(entities))
	for _, entity := range entities {
		items = append(items, topicEventFromEntity(entity))
	}
	return items
}

// topicEventFromEntity maps one event row exactly like scanTopicEvent.
func topicEventFromEntity(entity *dao.Event) domain.TopicEventRecord {
	return domain.TopicEventRecord{
		ID:                 entity.EventID,
		Sequence:           entity.ID,
		Topic:              entity.Topic,
		Source:             entity.Source,
		Provider:           entity.Provider,
		Intent:             entity.Intent,
		CorrelationID:      entity.CorrelationID,
		IdempotencyKey:     entity.IdempotencyKey,
		DeliveryID:         entity.DeliveryID,
		PayloadHash:        entity.PayloadHash,
		PayloadJSON:        entity.PayloadJSON,
		DispatchStatus:     entity.DispatchStatus,
		ParentEventID:      entity.ParentEventID,
		PublisherType:      entity.PublisherType,
		PublisherID:        entity.PublisherID,
		PublisherRunID:     entity.PublisherRunID,
		PublisherProjectID: entity.PublisherProjectID,
		ReplayOfEventID:    entity.ReplayOfEventID,
		ClaimID:            entity.ClaimID,
		ClaimUntil:         storedtime.ParseStoredUnixTimeAuto(entity.ClaimUntil),
		AttemptCount:       entity.AttemptCount,
		NextAttemptAt:      storedtime.ParseStoredUnixTimeAuto(entity.NextAttemptAt),
		LastError:          entity.LastError,
		DeadLetterAt:       storedtime.ParseStoredUnixTimeAuto(entity.DeadLetterAt),
		CreatedAt:          storedtime.ParseStoredUnixTimeAuto(entity.CreatedAt),
		DispatchedAt:       storedtime.ParseStoredUnixTimeAuto(entity.DispatchedAt),
	}
}

// eventDeliveryFromEntity maps one delivery row exactly like the released scan.
func eventDeliveryFromEntity(entity *dao.EventDelivery) domain.EventDelivery {
	return domain.EventDelivery{
		EventID:     entity.ID,
		SchedulerID: entity.SchedulerID,
		TriggerID:   entity.TriggerID,
		RunID:       entity.SchedulerRunID,
		Status:      entity.Status,
		Error:       entity.Error,
		CreatedAt:   storedtime.ParseStoredUnixTimeAuto(entity.CreatedAt),
		UpdatedAt:   storedtime.ParseStoredUnixTimeAuto(entity.UpdatedAt),
	}
}

// eventSandboxLinkFromEntity maps one link row exactly like the released scan.
func eventSandboxLinkFromEntity(entity *dao.EventSandboxLink) domain.EventSandboxTraceItem {
	return domain.EventSandboxTraceItem{
		SandboxID:        entity.SandboxID,
		Relation:         entity.Relation,
		SchedulerID:      entity.SchedulerID,
		RunID:            entity.SchedulerRunID,
		TriggerID:        entity.TriggerID,
		SchedulerEventID: entity.SchedulerEventID,
		EventID:          entity.ID,
		CreatedAt:        storedtime.ParseStoredUnixTimeAuto(entity.CreatedAt),
	}
}

// webhookSourceFromEntity maps one webhook_source row exactly like the released
// scan; enabled stays a stored integer.
func webhookSourceFromEntity(entity *dao.WebhookSource) domain.WebhookSource {
	return domain.WebhookSource{
		ID:              entity.ID,
		Name:            entity.Name,
		Enabled:         entity.Enabled != 0,
		Provider:        entity.Provider,
		TopicPrefix:     entity.TopicPrefix,
		TokenHash:       entity.TokenHash,
		TokenHeader:     entity.TokenHeader,
		SignatureType:   entity.SignatureType,
		SignatureSecret: entity.SignatureSecret,
		BodyLimitBytes:  entity.BodyLimitBytes,
		CreatedAt:       storedtime.ParseStoredUnixTimeAuto(entity.CreatedAt),
		UpdatedAt:       storedtime.ParseStoredUnixTimeAuto(entity.UpdatedAt),
	}
}
