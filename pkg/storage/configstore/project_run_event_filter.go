package configstore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/eventdelivery"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// eventRunScope resolves an event id to the set of scheduler run ids recorded
// against the event scope: the event itself, its descendant events, and events
// sharing its correlation id — the same scope GetEventTrace presents. An
// unknown event id returns a NotFound resource error. The scope walk caps at
// MaxEventScopeEvents like GetEventTrace; truncated reports that the cap was
// hit, so runs recorded only against events beyond it are missing from the
// filter.
func eventRunScope(ctx context.Context, client *dao.Client, db *sql.DB, eventID string) (schedulerRunIDs []string, truncated bool, err error) {
	_, scope, truncated, err := eventScopeIDs(ctx, client, db, eventID, domain.MaxEventScopeEvents)
	if err != nil {
		return nil, false, err
	}
	schedulerRunIDs, err = eventScopeSchedulerRunIDs(ctx, client, scope)
	return schedulerRunIDs, truncated, err
}

// eventScopeSchedulerRunIDs collects the distinct non-empty scheduler run ids
// recorded in event_delivery rows for the given event ids.
func eventScopeSchedulerRunIDs(ctx context.Context, client *dao.Client, eventIDs []string) ([]string, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	entities, err := client.EventDelivery.Query().
		Select(eventdelivery.FieldSchedulerRunID).
		Where(
			eventdelivery.SchedulerRunIDNEQ(""),
			eventdelivery.IDIn(eventIDs...),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query event delivery scheduler runs: %w", err)
	}
	// The released query was SELECT DISTINCT; the delivery primary key spans
	// event/scheduler/trigger, so one run id can repeat across rows.
	seen := make(map[string]struct{}, len(entities))
	schedulerRunIDs := make([]string, 0, len(entities))
	for _, entity := range entities {
		if _, ok := seen[entity.SchedulerRunID]; ok {
			continue
		}
		seen[entity.SchedulerRunID] = struct{}{}
		schedulerRunIDs = append(schedulerRunIDs, entity.SchedulerRunID)
	}
	return schedulerRunIDs, nil
}
