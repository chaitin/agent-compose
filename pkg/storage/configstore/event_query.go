package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/events"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storedtime"
)

type eventListQuery struct {
	where      string
	filterArgs []any
	page       string
	pageArgs   []any
}

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

func buildEventListQuery(filter domain.TopicEventFilter) (eventListQuery, error) {
	if strings.TrimSpace(filter.Source) == "" && strings.TrimSpace(filter.Topic) == "" && strings.TrimSpace(filter.CorrelationID) == "" {
		return eventListQuery{}, fmt.Errorf("source, topic, or correlation id is required")
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	clauses := make([]string, 0, 5)
	args := make([]any, 0, 5)
	if source := strings.TrimSpace(filter.Source); source != "" {
		predicate, sourceArgs := buildEventSourcePredicate(source)
		if predicate == "" {
			return eventListQuery{}, fmt.Errorf("event source is invalid")
		}
		clauses = append(clauses, predicate)
		args = append(args, sourceArgs...)
	}
	if topic := strings.TrimSpace(filter.Topic); topic != "" {
		if err := events.ValidateTopicName(topic); err != nil {
			return eventListQuery{}, err
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
	if status := events.NormalizeDispatchStatus(filter.DispatchStatus); status != "" && strings.TrimSpace(filter.DispatchStatus) != "" {
		clauses = append(clauses, "dispatch_status = ?")
		args = append(args, status)
	}
	query := eventListQuery{
		where:      ` WHERE ` + strings.Join(clauses, " AND "),
		filterArgs: args,
		page:       ` ORDER BY sequence ASC LIMIT ?`,
		pageArgs:   []any{limit},
	}
	if filter.AfterSequence <= 0 && !filter.SequenceAsc {
		query.page = ` ORDER BY sequence DESC LIMIT ? OFFSET ?`
		query.pageArgs = []any{limit, max(filter.Offset, 0)}
	}
	return query, nil
}

func (s *eventStore) countEventList(ctx context.Context, query eventListQuery) (int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event`+query.where, query.filterArgs...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count events: %w", err)
	}
	return total, nil
}

func (s *eventStore) ListEventSummaries(ctx context.Context, filter domain.TopicEventFilter) ([]domain.EventSummary, int, error) {
	query, err := buildEventListQuery(filter)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.countEventList(ctx, query)
	if err != nil {
		return nil, 0, err
	}
	args := append(append([]any(nil), query.filterArgs...), query.pageArgs...)
	rows, err := s.db.QueryContext(ctx, selectEventSummarySQL()+query.where+query.page, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("query event summaries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]domain.EventSummary, 0)
	for rows.Next() {
		item, err := scanEventSummary(rows.Scan)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate event summaries: %w", err)
	}
	return items, total, nil
}

// eventSummaryByID loads one event by exact id. Unknown or blank ids return
// NotFound / argument errors respectively.
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
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT topic) FROM event WHERE `+predicate, sourceArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count event topics: %w", err)
	}
	queryArgs := append(append([]any(nil), sourceArgs...), limit, offset)
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
	rows, err := db.QueryContext(ctx, `WITH RECURSIVE descendants(id, sequence) AS (
		SELECT id, sequence FROM event WHERE id = ?
		UNION
		SELECT child.id, child.sequence FROM event child
		JOIN descendants parent ON child.parent_event_id = parent.id
		ORDER BY sequence ASC
		LIMIT ?
	)
	SELECT id FROM descendants ORDER BY sequence ASC`, eventID, limit+1)
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
	rows, err := s.db.QueryContext(ctx, `SELECT
		d.event_id, d.scheduler_id, d.trigger_id, d.scheduler_run_id,
		d.status, d.error, d.created_at, d.updated_at,
		COALESCE(s.id, ''), COALESCE(s.project_id, ''), COALESCE(s.agent_name, ''),
		CASE WHEN json_valid(s.spec_json) THEN CAST(COALESCE(json_extract(s.spec_json, '$.display_name'), '') AS TEXT) ELSE '' END,
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
		var schedulerID, projectID, agentName, schedulerDisplayName string
		var run domain.EventSchedulerRunSummary
		var deliveryCreatedAt, deliveryUpdatedAt, runStartedAt, runCompletedAt any
		if err := rows.Scan(
			&item.Delivery.EventID, &item.Delivery.SchedulerID, &item.Delivery.TriggerID, &item.Delivery.RunID,
			&item.Delivery.Status, &item.Delivery.Error, &deliveryCreatedAt, &deliveryUpdatedAt,
			&schedulerID, &projectID, &agentName, &schedulerDisplayName,
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
				Name:      eventSchedulerName(schedulerDisplayName, agentName),
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

func eventRunTraceKey(schedulerID, runID string) string {
	return schedulerID + "\x00" + runID
}
