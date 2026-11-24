package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/event"
	"github.com/chaitin/agent-compose/ent/dao/predicate"
	"github.com/chaitin/agent-compose/pkg/events"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storedtime"
)

// eventListFilter is the ent form of a topic event listing request. The
// released queries shared one WHERE builder between the count and the page; the
// predicates below keep that single validation path. Ordering is ascending for
// a sequence cursor or an explicit ascending request, and descending with an
// offset otherwise.
type eventListFilter struct {
	predicates []predicate.Event
	limit      int
	offset     int
	ascending  bool
}

func buildEventListFilter(filter domain.TopicEventFilter) (eventListFilter, error) {
	if strings.TrimSpace(filter.Source) == "" && strings.TrimSpace(filter.Topic) == "" && strings.TrimSpace(filter.CorrelationID) == "" {
		return eventListFilter{}, fmt.Errorf("source, topic, or correlation id is required")
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	predicates := make([]predicate.Event, 0, 5)
	if source := strings.TrimSpace(filter.Source); source != "" {
		values := events.SourceFilterValues(source)
		if len(values) == 0 {
			return eventListFilter{}, fmt.Errorf("event source is invalid")
		}
		predicates = append(predicates, event.SourceIn(values...))
	}
	if topic := strings.TrimSpace(filter.Topic); topic != "" {
		if err := events.ValidateTopicName(topic); err != nil {
			return eventListFilter{}, err
		}
		predicates = append(predicates, event.TopicEQ(topic))
	}
	if correlationID := strings.TrimSpace(filter.CorrelationID); correlationID != "" {
		predicates = append(predicates, event.CorrelationIDEQ(correlationID))
	}
	if filter.AfterSequence > 0 {
		predicates = append(predicates, event.IDGT(filter.AfterSequence))
	}
	if status := events.NormalizeDispatchStatus(filter.DispatchStatus); status != "" && strings.TrimSpace(filter.DispatchStatus) != "" {
		predicates = append(predicates, event.DispatchStatusEQ(status))
	}
	list := eventListFilter{
		predicates: predicates,
		limit:      limit,
		ascending:  filter.AfterSequence > 0 || filter.SequenceAsc,
	}
	if !list.ascending {
		list.offset = max(filter.Offset, 0)
	}
	return list, nil
}

// eventPageQuery applies the shared ordering and page bounds to one filter.
func eventPageQuery(client *dao.Client, filter eventListFilter) *dao.EventQuery {
	query := client.Event.Query().Where(filter.predicates...)
	if filter.ascending {
		query.Order(event.ByID())
	} else {
		query.Order(event.ByID(entsql.OrderDesc()))
	}
	query.Limit(filter.limit)
	if !filter.ascending {
		query.Offset(filter.offset)
	}
	return query
}

// buildEventSourcePredicate renders the source IN list for ListEventTopics,
// which stays a SQL aggregate query.
func buildEventSourcePredicate(source string) (string, []any) {
	values := events.SourceFilterValues(source)
	if len(values) == 0 {
		return "", nil
	}
	placeholders := make([]string, len(values))
	args := make([]any, len(values))
	for index, value := range values {
		placeholders[index] = "?"
		args[index] = value
	}
	return "source IN (" + strings.Join(placeholders, ", ") + ")", args
}

func (s *eventStore) countEventList(ctx context.Context, filter eventListFilter) (int, error) {
	total, err := s.client.Event.Query().Where(filter.predicates...).Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("count events: %w", err)
	}
	return total, nil
}

func (s *eventStore) ListEventSummaries(ctx context.Context, filter domain.TopicEventFilter) ([]domain.EventSummary, int, error) {
	list, err := buildEventListFilter(filter)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.countEventList(ctx, list)
	if err != nil {
		return nil, 0, err
	}
	entities, err := eventPageQuery(s.client, list).
		Select(
			event.FieldEventID,
			event.FieldTopic,
			event.FieldSource,
			event.FieldProvider,
			event.FieldIntent,
			event.FieldCorrelationID,
			event.FieldDeliveryID,
			event.FieldDispatchStatus,
			event.FieldParentEventID,
			event.FieldPublisherType,
			event.FieldPublisherID,
			event.FieldPublisherRunID,
			event.FieldCreatedAt,
			event.FieldDispatchedAt,
		).
		All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("query event summaries: %w", err)
	}
	items := make([]domain.EventSummary, 0, len(entities))
	for _, entity := range entities {
		items = append(items, eventSummaryFromEntity(entity))
	}
	return items, total, nil
}

// eventSummaryFromEntity maps the narrow summary projection exactly like
// scanEventSummary.
func eventSummaryFromEntity(entity *dao.Event) domain.EventSummary {
	return domain.EventSummary{
		ID:             entity.EventID,
		Sequence:       entity.ID,
		Topic:          entity.Topic,
		Source:         entity.Source,
		Provider:       entity.Provider,
		Intent:         entity.Intent,
		CorrelationID:  entity.CorrelationID,
		DeliveryID:     entity.DeliveryID,
		DispatchStatus: entity.DispatchStatus,
		ParentEventID:  entity.ParentEventID,
		PublisherType:  entity.PublisherType,
		PublisherID:    entity.PublisherID,
		PublisherRunID: entity.PublisherRunID,
		CreatedAt:      storedtime.ParseStoredTime(entity.CreatedAt),
		DispatchedAt:   storedtime.ParseStoredTime(entity.DispatchedAt),
	}
}

// eventSummaryByID loads one event by exact id. Unknown or blank ids return
// NotFound / argument errors respectively.
//
// Kept as SQL: the helper is reached through the scope walker below, which
// callers (project_run_event_filter.go's eventRunScope) drive with a plain
// *sql.DB; converting would require threading an ent client through
// project_store.go, outside this file's scope.
func eventSummaryByID(ctx context.Context, db *sql.DB, eventID string) (domain.EventSummary, error) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return domain.EventSummary{}, fmt.Errorf("event id is required")
	}
	item, err := scanEventSummary(db.QueryRowContext(ctx, selectEventSummarySQL()+` WHERE id = ?`, eventID).Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.EventSummary{}, domain.ResourceError(domain.ErrNotFound, "event", eventID, fmt.Sprintf("event %s not found", eventID), err)
		}
		return domain.EventSummary{}, err
	}
	return item, nil
}

func scanEventSummary(scan func(dest ...any) error) (domain.EventSummary, error) {
	var item domain.EventSummary
	var createdAt, dispatchedAt any
	if err := scan(
		&item.ID, &item.Sequence, &item.Topic, &item.Source, &item.Provider, &item.Intent,
		&item.CorrelationID, &item.DeliveryID, &item.DispatchStatus, &item.ParentEventID,
		&item.PublisherType, &item.PublisherID, &item.PublisherRunID, &createdAt, &dispatchedAt,
	); err != nil {
		return domain.EventSummary{}, fmt.Errorf("scan event summary: %w", err)
	}
	item.CreatedAt = storedtime.ParseStoredTime(createdAt)
	item.DispatchedAt = storedtime.ParseStoredTime(dispatchedAt)
	return item, nil
}

func selectEventSummarySQL() string {
	return `SELECT id, sequence, topic, source, provider, intent, correlation_id, delivery_id,
		dispatch_status, parent_event_id, publisher_type, publisher_id, publisher_run_id, created_at, dispatched_at
		FROM event`
}

func (s *eventStore) ListEventTopics(ctx context.Context, source string, offset, limit int) ([]domain.EventTopicSummary, int, error) {
	predicate, sourceArgs := buildEventSourcePredicate(source)
	if predicate == "" {
		return nil, 0, fmt.Errorf("event source is required")
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	var total int
	// Kept as SQL: COUNT(DISTINCT topic) is an aggregate.
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT topic) FROM event WHERE `+predicate, sourceArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count event topics: %w", err)
	}
	queryArgs := append(append([]any(nil), sourceArgs...), limit, offset)
	// Kept as SQL: GROUP BY topic with COUNT/MAX aggregates and ordering by MAX.
	rows, err := s.db.QueryContext(ctx, `SELECT topic, COUNT(*), MAX(created_at)
		FROM event WHERE `+predicate+` GROUP BY topic
		ORDER BY MAX(created_at) DESC, topic ASC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("query event topics: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]domain.EventTopicSummary, 0)
	for rows.Next() {
		var item domain.EventTopicSummary
		var latestAt any
		if err := rows.Scan(&item.Topic, &item.EventCount, &latestAt); err != nil {
			return nil, 0, fmt.Errorf("scan event topic: %w", err)
		}
		item.LatestEventAt = storedtime.ParseStoredTime(latestAt)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate event topics: %w", err)
	}
	return items, total, nil
}

func (s *eventStore) GetEventTrace(ctx context.Context, eventID string, descendantLimit int) (domain.EventTrace, error) {
	root, eventIDs, truncated, err := eventScopeIDs(ctx, s.db, eventID, descendantLimit)
	if err != nil {
		return domain.EventTrace{}, err
	}
	runs, err := s.listEventRunTraces(ctx, eventIDs)
	if err != nil {
		return domain.EventTrace{}, err
	}
	links, err := s.ListEventSandboxLinks(ctx, eventIDs)
	if err != nil {
		return domain.EventTrace{}, err
	}
	return domain.EventTrace{
		Event:                root,
		Runs:                 runs,
		SandboxLinks:         links,
		DescendantsTruncated: truncated,
	}, nil
}

// eventScopeIDs resolves the event-scope walk shared by GetEventTrace and
// ListRuns event_id filtering: the event, its parent_event_id descendants,
// and events sharing its correlation id. A non-positive or oversized limit
// falls back to MaxEventScopeEvents.
//
// Kept as SQL: the descendant walk is a recursive CTE and the correlation
// merge is a dynamic NOT IN list, and both helpers are called with the plain
// *sql.DB that project_run_event_filter.go receives.
func eventScopeIDs(ctx context.Context, db *sql.DB, eventID string, limit int) (domain.EventSummary, []string, bool, error) {
	root, err := eventSummaryByID(ctx, db, eventID)
	if err != nil {
		return domain.EventSummary{}, nil, false, err
	}
	if limit <= 0 || limit > domain.MaxEventScopeEvents {
		limit = domain.MaxEventScopeEvents
	}
	eventIDs, truncated, err := listEventDescendantIDs(ctx, db, root.ID, limit)
	if err != nil {
		return domain.EventSummary{}, nil, false, err
	}
	// Webhook forwarders that POST into a separate topic do not set
	// parent_event_id, so the CTE traversal above does not discover them.
	// Collect events that share the same correlation_id but are outside the
	// parent chain so that sandbox links and run traces are still visible.
	eventIDs, correlationTruncated, err := mergeCorrelationEventIDs(ctx, db, root, eventIDs, limit)
	if err != nil {
		return domain.EventSummary{}, nil, false, err
	}
	return root, eventIDs, truncated || correlationTruncated, nil
}

// mergeCorrelationEventIDs appends events that share the root's correlation id
// but are outside its parent chain, up to the remaining limit. It reports
// whether the merged scope was truncated.
func mergeCorrelationEventIDs(ctx context.Context, db *sql.DB, root domain.EventSummary, descendants []string, limit int) ([]string, bool, error) {
	correlationID := strings.TrimSpace(root.CorrelationID)
	if correlationID == "" {
		return descendants, false, nil
	}
	remaining := limit - len(descendants)
	if remaining < 0 {
		remaining = 0
	}

	args := make([]any, 0, len(descendants)+2)
	args = append(args, correlationID)
	for _, id := range descendants {
		args = append(args, id)
	}
	args = append(args, remaining+1)
	rows, err := db.QueryContext(ctx,
		// Kept as SQL: the NOT IN list length follows the descendant count.
		`SELECT id FROM event WHERE correlation_id = ? AND id NOT IN (`+placeholders(len(descendants))+`) ORDER BY sequence ASC LIMIT ?`,
		args...)
	if err != nil {
		return nil, false, fmt.Errorf("query correlation events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	correlationIDs := make([]string, 0, remaining+1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, fmt.Errorf("scan correlation event: %w", err)
		}
		correlationIDs = append(correlationIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate correlation events: %w", err)
	}
	truncated := len(correlationIDs) > remaining
	if truncated {
		correlationIDs = correlationIDs[:remaining]
	}
	return append(descendants, correlationIDs...), truncated, nil
}

// listEventDescendantIDs walks the event's parent_event_id chain starting at
// eventID, capped at limit entries. It reports whether more descendants exist.
func listEventDescendantIDs(ctx context.Context, db *sql.DB, eventID string, limit int) ([]string, bool, error) {
	// Kept as SQL: recursive CTE over parent_event_id.
	rows, err := db.QueryContext(ctx, `WITH RECURSIVE descendants(id, sequence) AS (
		SELECT id, sequence FROM event WHERE id = ?
		UNION
		SELECT child.id, child.sequence FROM event child
		JOIN descendants parent ON child.parent_event_id = parent.id
	)
	SELECT id FROM descendants ORDER BY sequence ASC LIMIT ?`, eventID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("query event descendants: %w", err)
	}
	defer func() { _ = rows.Close() }()
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, fmt.Errorf("scan event descendant: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate event descendants: %w", err)
	}
	truncated := len(ids) > limit
	if truncated {
		ids = ids[:limit]
	}
	return ids, truncated, nil
}

func (s *eventStore) listEventRunTraces(ctx context.Context, eventIDs []string) ([]domain.EventRunTrace, error) {
	if len(eventIDs) == 0 {
		return []domain.EventRunTrace{}, nil
	}
	args := make([]any, len(eventIDs))
	for index, id := range eventIDs {
		args[index] = id
	}
	// Kept as SQL: one consistent read joining event_delivery with the
	// scheduler and scheduler_run projections.
	rows, err := s.db.QueryContext(ctx, `SELECT
		d.event_id, d.scheduler_id, d.trigger_id, d.scheduler_run_id,
		d.status, d.error, d.created_at, d.updated_at,
		COALESCE(s.id, ''), COALESCE(s.project_id, ''), COALESCE(s.agent_name, ''),
		COALESCE(s.spec_json, ''),
		COALESCE(r.run_id, ''), COALESCE(r.status, ''), COALESCE(r.started_at, 0),
		COALESCE(r.completed_at, 0), COALESCE(r.duration_ms, 0), COALESCE(r.error, '')
		FROM event_delivery d
		LEFT JOIN project_scheduler s ON s.id = d.scheduler_id
		LEFT JOIN scheduler_run r ON r.scheduler_id = d.scheduler_id AND r.run_id = d.scheduler_run_id
		WHERE d.event_id IN (`+placeholders(len(eventIDs))+`)
		ORDER BY d.updated_at ASC, d.scheduler_id ASC, d.trigger_id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query event run traces: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]domain.EventRunTrace, 0)
	indexesByRun := make(map[string][]int)
	for rows.Next() {
		var item domain.EventRunTrace
		var schedulerID, projectID, agentName, schedulerSpecJSON string
		var run domain.EventSchedulerRunSummary
		var deliveryCreatedAt, deliveryUpdatedAt, runStartedAt, runCompletedAt any
		if err := rows.Scan(
			&item.Delivery.EventID, &item.Delivery.SchedulerID, &item.Delivery.TriggerID, &item.Delivery.RunID,
			&item.Delivery.Status, &item.Delivery.Error, &deliveryCreatedAt, &deliveryUpdatedAt,
			&schedulerID, &projectID, &agentName, &schedulerSpecJSON,
			&run.ID, &run.Status, &runStartedAt, &runCompletedAt, &run.DurationMs, &run.Error,
		); err != nil {
			return nil, fmt.Errorf("scan event run trace: %w", err)
		}
		item.Delivery.CreatedAt = storedtime.ParseStoredTime(deliveryCreatedAt)
		item.Delivery.UpdatedAt = storedtime.ParseStoredTime(deliveryUpdatedAt)
		if schedulerID != "" {
			item.Scheduler = &domain.EventSchedulerSummary{
				ID:        schedulerID,
				ProjectID: projectID,
				AgentName: agentName,
				Name:      eventSchedulerName(eventSchedulerDisplayName(schedulerSpecJSON), agentName),
			}
		}
		if run.ID != "" {
			run.StartedAt = storedtime.ParseStoredTime(runStartedAt)
			run.CompletedAt = storedtime.ParseStoredTime(runCompletedAt)
			item.Run = &run
			key := eventRunTraceKey(item.Delivery.SchedulerID, run.ID)
			indexesByRun[key] = append(indexesByRun[key], len(items))
		}
		item.Events = []domain.EventSchedulerEventSummary{}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate event run traces: %w", err)
	}
	if err := s.attachEventSchedulerEvents(ctx, eventIDs, items, indexesByRun); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *eventStore) attachEventSchedulerEvents(ctx context.Context, eventIDs []string, traces []domain.EventRunTrace, indexesByRun map[string][]int) error {
	if len(indexesByRun) == 0 {
		return nil
	}
	args := make([]any, len(eventIDs))
	for index, id := range eventIDs {
		args[index] = id
	}
	// Kept as SQL: DISTINCT join between scheduler_event and event_delivery.
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT
		e.scheduler_id, e.event_id, e.scheduler_run_id, e.type, e.level,
		e.message, e.linked_sandbox_id, e.created_at
		FROM scheduler_event e
		JOIN event_delivery d ON d.scheduler_id = e.scheduler_id AND d.scheduler_run_id = e.scheduler_run_id
		WHERE d.event_id IN (`+placeholders(len(eventIDs))+`) AND d.scheduler_run_id <> ''
		ORDER BY e.created_at ASC, e.event_id ASC`, args...)
	if err != nil {
		return fmt.Errorf("query traced scheduler events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var schedulerID, runID string
		var event domain.EventSchedulerEventSummary
		var createdAt any
		if err := rows.Scan(
			&schedulerID, &event.ID, &runID, &event.Type, &event.Level,
			&event.Message, &event.LinkedSandboxID, &createdAt,
		); err != nil {
			return fmt.Errorf("scan traced scheduler event: %w", err)
		}
		event.CreatedAt = storedtime.ParseStoredTime(createdAt)
		for _, index := range indexesByRun[eventRunTraceKey(schedulerID, runID)] {
			traces[index].Events = append(traces[index].Events, event)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate traced scheduler events: %w", err)
	}
	return nil
}

func eventSchedulerName(displayName, fallback string) string {
	if name := strings.TrimSpace(displayName); name != "" {
		return name
	}
	return strings.TrimSpace(fallback)
}

// eventSchedulerDisplayName reads display_name from a scheduler spec without
// asking the database to parse JSON, which keeps SQL portable across SQLite
// and PostgreSQL. Invalid or legacy specs fall back to the agent name.
func eventSchedulerDisplayName(specJSON string) string {
	var spec map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(specJSON)), &spec); err != nil {
		return ""
	}
	raw, ok := spec["display_name"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	// Match the previous json_extract(... ) CAST AS TEXT behavior for a
	// non-string scalar or structured value.
	return strings.TrimSpace(string(raw))
}

func eventRunTraceKey(schedulerID, runID string) string {
	return schedulerID + "\x00" + runID
}
