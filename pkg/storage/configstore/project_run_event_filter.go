package configstore

import (
	"context"
	"database/sql"
	"fmt"
)

// maxEventScopeEvents bounds the event scope walk (descendants plus
// correlation siblings) to the same limit GetEventTrace applies.
const maxEventScopeEvents = 1000

// eventRunScope resolves an event id to the set of scheduler run ids recorded
// against the event scope: the event itself, its descendant events, and events
// sharing its correlation id — the same scope GetEventTrace presents. An
// unknown event id returns a NotFound resource error. The scope walk caps at
// maxEventScopeEvents like GetEventTrace; truncated reports that the cap was
// hit, so runs recorded only against events beyond it are missing from the
// filter.
func eventRunScope(ctx context.Context, db *sql.DB, eventID string) (schedulerRunIDs []string, truncated bool, err error) {
	root, err := eventSummaryByID(ctx, db, eventID)
	if err != nil {
		return nil, false, err
	}
	scope, descendantsTruncated, err := listEventDescendantIDs(ctx, db, root.ID, maxEventScopeEvents)
	if err != nil {
		return nil, false, err
	}
	scope, correlationTruncated, err := mergeCorrelationEventIDs(ctx, db, root, scope, maxEventScopeEvents)
	if err != nil {
		return nil, false, err
	}
	schedulerRunIDs, err = eventScopeSchedulerRunIDs(ctx, db, scope)
	return schedulerRunIDs, descendantsTruncated || correlationTruncated, err
}

// eventScopeSchedulerRunIDs collects the distinct non-empty scheduler run ids
// recorded in event_delivery rows for the given event ids.
func eventScopeSchedulerRunIDs(ctx context.Context, db *sql.DB, eventIDs []string) ([]string, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	args := make([]any, len(eventIDs))
	for index, id := range eventIDs {
		args[index] = id
	}
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT scheduler_run_id FROM event_delivery
		WHERE scheduler_run_id <> '' AND event_id IN (`+placeholders(len(eventIDs))+`)`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("query event delivery scheduler runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	schedulerRunIDs := make([]string, 0, len(eventIDs))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan event delivery scheduler run: %w", err)
		}
		schedulerRunIDs = append(schedulerRunIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate event delivery scheduler runs: %w", err)
	}
	return schedulerRunIDs, nil
}
