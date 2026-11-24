package configstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/eventdelivery"
	"github.com/chaitin/agent-compose/ent/dao/eventsandboxlink"
	"github.com/chaitin/agent-compose/ent/dao/schedulerevent"
	"github.com/chaitin/agent-compose/ent/dao/schedulerrun"
	"github.com/chaitin/agent-compose/pkg/idset"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

func (s *schedulerStore) ListSchedulerRunsForPrune(ctx context.Context, filter schedulers.SchedulerRunPruneFilter) ([]domain.SchedulerRunSummary, error) {
	schedulerIDs := idset.Normalize(filter.SchedulerIDs)
	if len(schedulerIDs) == 0 {
		return []domain.SchedulerRunSummary{}, nil
	}
	query := s.client.SchedulerRun.Query().
		Where(
			schedulerrun.SchedulerIDIn(schedulerIDs...),
			schedulerrun.TriggerIDNEQ(""),
		)
	if triggerID := strings.TrimSpace(filter.TriggerID); triggerID != "" {
		query = query.Where(schedulerrun.TriggerIDEQ(triggerID))
	}
	if len(filter.Statuses) > 0 {
		statuses := make([]string, len(filter.Statuses))
		for index, status := range filter.Statuses {
			statuses[index] = strings.TrimSpace(status)
		}
		query = query.Where(schedulerrun.StatusIn(statuses...))
	}
	if filter.OlderThan > 0 {
		cutoff := filter.Now.Add(-filter.OlderThan).UnixMilli()
		query = query.Where(schedulerrun.Or(
			schedulerrun.And(schedulerrun.CompletedAtGT(0), schedulerrun.CompletedAtLTE(cutoff)),
			schedulerrun.And(schedulerrun.CompletedAtEQ(0), schedulerrun.StartedAtLTE(cutoff)),
		))
	}
	entities, err := query.
		Order(schedulerrun.ByStartedAt(), schedulerrun.BySchedulerID(), schedulerrun.ByID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query scheduler runs for prune: %w", err)
	}
	result := make([]domain.SchedulerRunSummary, 0, len(entities))
	for _, entity := range entities {
		result = append(result, schedulerRunFromEntity(entity))
	}
	return result, nil
}

// CountSchedulerRunPruneData reports the rows a prune would remove. The four
// counts are read one after another from the shared client; the released
// statement bundled them into one round trip, and the preview is advisory (the
// delete recomputes every count under its transaction).
func (s *schedulerStore) CountSchedulerRunPruneData(ctx context.Context, keys []schedulers.SchedulerRunKey) (schedulers.SchedulerRunPruneDatabaseStats, error) {
	keys = normalizedSchedulerRunKeys(keys)
	var stats schedulers.SchedulerRunPruneDatabaseStats
	for _, key := range keys {
		eligible, err := schedulerRunPruneKeyIsEligible(ctx, s.client, key)
		if err != nil {
			return schedulers.SchedulerRunPruneDatabaseStats{}, err
		}
		if !eligible {
			continue
		}
		counts, err := pruneDataCounts(ctx, s.client, key)
		if err != nil {
			return schedulers.SchedulerRunPruneDatabaseStats{}, fmt.Errorf("count scheduler run prune data %s/%s: %w", key.SchedulerID, key.RunID, err)
		}
		stats.SchedulerEvents += counts.schedulerEvents
		stats.EventDeliveries += counts.deliveries
		stats.EventSandboxLinks += counts.sandboxLinks
		stats.Runs += counts.runs
	}
	return stats, nil
}

type pruneDataCount struct {
	schedulerEvents uint64
	deliveries      uint64
	sandboxLinks    uint64
	runs            uint64
}

func pruneDataCounts(ctx context.Context, client *dao.Client, key schedulers.SchedulerRunKey) (pruneDataCount, error) {
	schedulerEvents, err := client.SchedulerEvent.Query().
		Where(schedulerevent.IDEQ(key.SchedulerID), schedulerevent.SchedulerRunIDEQ(key.RunID)).
		Count(ctx)
	if err != nil {
		return pruneDataCount{}, err
	}
	deliveries, err := client.EventDelivery.Query().
		Where(eventdelivery.SchedulerIDEQ(key.SchedulerID), eventdelivery.SchedulerRunIDEQ(key.RunID)).
		Count(ctx)
	if err != nil {
		return pruneDataCount{}, err
	}
	sandboxLinks, err := client.EventSandboxLink.Query().
		Where(eventsandboxlink.SchedulerIDEQ(key.SchedulerID), eventsandboxlink.SchedulerRunIDEQ(key.RunID)).
		Count(ctx)
	if err != nil {
		return pruneDataCount{}, err
	}
	runs, err := client.SchedulerRun.Query().
		Where(
			schedulerrun.IDEQ(key.RunID),
			schedulerrun.SchedulerIDEQ(key.SchedulerID),
			schedulerrun.TriggerIDNEQ(""),
		).
		Count(ctx)
	if err != nil {
		return pruneDataCount{}, err
	}
	return pruneDataCount{
		schedulerEvents: uint64(schedulerEvents),
		deliveries:      uint64(deliveries),
		sandboxLinks:    uint64(sandboxLinks),
		runs:            uint64(runs),
	}, nil
}

func (s *schedulerStore) DeleteSchedulerRunPruneData(ctx context.Context, keys []schedulers.SchedulerRunKey) (schedulers.SchedulerRunPruneDatabaseResult, error) {
	keys = normalizedSchedulerRunKeys(keys)
	if len(keys) == 0 {
		return schedulers.SchedulerRunPruneDatabaseResult{}, nil
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return schedulers.SchedulerRunPruneDatabaseResult{}, fmt.Errorf("begin scheduler run prune: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	var result schedulers.SchedulerRunPruneDatabaseResult
	for _, key := range keys {
		removed, err := deleteSchedulerRunPruneRows(ctx, client, key, &result.Stats)
		if err != nil {
			return schedulers.SchedulerRunPruneDatabaseResult{}, err
		}
		if removed {
			result.RemovedKeys = append(result.RemovedKeys, key)
		}
	}
	if err := tx.Commit(); err != nil {
		return schedulers.SchedulerRunPruneDatabaseResult{}, fmt.Errorf("commit scheduler run prune: %w", err)
	}
	return result, nil
}

func deleteSchedulerRunPruneRows(ctx context.Context, client *dao.Client, key schedulers.SchedulerRunKey, stats *schedulers.SchedulerRunPruneDatabaseStats) (bool, error) {
	eligible, err := schedulerRunPruneKeyIsEligible(ctx, client, key)
	if err != nil {
		return false, err
	}
	if !eligible {
		return false, nil
	}
	steps := []struct {
		name string
		exec func() (int, error)
		add  func(uint64)
	}{
		{name: "event sandbox links", exec: func() (int, error) {
			return client.EventSandboxLink.Delete().
				Where(eventsandboxlink.SchedulerIDEQ(key.SchedulerID), eventsandboxlink.SchedulerRunIDEQ(key.RunID)).
				Exec(ctx)
		}, add: func(count uint64) { stats.EventSandboxLinks += count }},
		{name: "event deliveries", exec: func() (int, error) {
			return client.EventDelivery.Delete().
				Where(eventdelivery.SchedulerIDEQ(key.SchedulerID), eventdelivery.SchedulerRunIDEQ(key.RunID)).
				Exec(ctx)
		}, add: func(count uint64) { stats.EventDeliveries += count }},
		{name: "scheduler events", exec: func() (int, error) {
			return client.SchedulerEvent.Delete().
				Where(schedulerevent.IDEQ(key.SchedulerID), schedulerevent.SchedulerRunIDEQ(key.RunID)).
				Exec(ctx)
		}, add: func(count uint64) { stats.SchedulerEvents += count }},
		{name: "scheduler run", exec: func() (int, error) {
			return client.SchedulerRun.Delete().
				Where(
					schedulerrun.SchedulerIDEQ(key.SchedulerID),
					schedulerrun.IDEQ(key.RunID),
					schedulerrun.TriggerIDNEQ(""),
				).
				Exec(ctx)
		}, add: func(count uint64) { stats.Runs += count }},
	}
	for _, step := range steps {
		rows, err := step.exec()
		if err != nil {
			return false, fmt.Errorf("delete scheduler run %s %s/%s: %w", step.name, key.SchedulerID, key.RunID, err)
		}
		step.add(uint64(rows))
	}
	return true, nil
}

func schedulerRunPruneKeyIsEligible(ctx context.Context, client *dao.Client, key schedulers.SchedulerRunKey) (bool, error) {
	eligible, err := client.SchedulerRun.Query().
		Where(
			schedulerrun.SchedulerIDEQ(key.SchedulerID),
			schedulerrun.IDEQ(key.RunID),
			schedulerrun.TriggerIDNEQ(""),
			schedulerrun.StatusIn(
				domain.SchedulerRunStatusSucceeded,
				domain.SchedulerRunStatusFailed,
				domain.SchedulerRunStatusCanceled,
				domain.SchedulerRunStatusSkipped,
			),
		).
		Exist(ctx)
	if err != nil {
		return false, fmt.Errorf("recheck scheduler run prune candidate %s/%s: %w", key.SchedulerID, key.RunID, err)
	}
	return eligible, nil
}

func normalizedSchedulerRunKeys(keys []schedulers.SchedulerRunKey) []schedulers.SchedulerRunKey {
	seen := make(map[schedulers.SchedulerRunKey]struct{}, len(keys))
	result := make([]schedulers.SchedulerRunKey, 0, len(keys))
	for _, key := range keys {
		key.SchedulerID = strings.TrimSpace(key.SchedulerID)
		key.RunID = strings.TrimSpace(key.RunID)
		if key.SchedulerID == "" || key.RunID == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	return result
}
