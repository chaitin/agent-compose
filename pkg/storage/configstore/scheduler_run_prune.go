package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/idset"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

func (s *schedulerStore) ListSchedulerRunsForPrune(ctx context.Context, filter schedulers.SchedulerRunPruneFilter) ([]domain.SchedulerRunSummary, error) {
	schedulerIDs := idset.Normalize(filter.SchedulerIDs)
	if len(schedulerIDs) == 0 {
		return []domain.SchedulerRunSummary{}, nil
	}
	placeholders := make([]string, len(schedulerIDs))
	args := make([]any, 0, len(schedulerIDs)+len(filter.Statuses)+4)
	for index, schedulerID := range schedulerIDs {
		placeholders[index] = "?"
		args = append(args, schedulerID)
	}
	query := schedulers.SelectSchedulerRunSQL() + ` WHERE scheduler_id IN (` + strings.Join(placeholders, ",") + `) AND trigger_id <> ''`
	if triggerID := strings.TrimSpace(filter.TriggerID); triggerID != "" {
		query += ` AND trigger_id = ?`
		args = append(args, triggerID)
	}
	if len(filter.Statuses) > 0 {
		statusPlaceholders := make([]string, len(filter.Statuses))
		for index, status := range filter.Statuses {
			statusPlaceholders[index] = "?"
			args = append(args, strings.TrimSpace(status))
		}
		query += ` AND status IN (` + strings.Join(statusPlaceholders, ",") + `)`
	}
	if filter.OlderThan > 0 {
		cutoff := filter.Now.Add(-filter.OlderThan).UnixMilli()
		query += ` AND ((completed_at > 0 AND completed_at <= ?) OR (completed_at = 0 AND started_at <= ?))`
		args = append(args, cutoff, cutoff)
	}
	query += ` ORDER BY started_at ASC, scheduler_id ASC, run_id ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query scheduler runs for prune: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]domain.SchedulerRunSummary, 0)
	for rows.Next() {
		run, scanErr := schedulers.ScanSchedulerRun(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scheduler runs for prune: %w", err)
	}
	return result, nil
}

func (s *schedulerStore) CountSchedulerRunPruneData(ctx context.Context, keys []schedulers.SchedulerRunKey) (schedulers.SchedulerRunPruneDatabaseStats, error) {
	keys = normalizedSchedulerRunKeys(keys)
	var stats schedulers.SchedulerRunPruneDatabaseStats
	for _, key := range keys {
		eligible, err := schedulerRunPruneKeyIsEligible(ctx, s.db, key)
		if err != nil {
			return schedulers.SchedulerRunPruneDatabaseStats{}, err
		}
		if !eligible {
			continue
		}
		var schedulerEvents, deliveries, sandboxLinks, runs int64
		err = s.db.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM scheduler_event WHERE scheduler_id = ? AND scheduler_run_id = ?),
			(SELECT COUNT(*) FROM event_delivery WHERE scheduler_id = ? AND scheduler_run_id = ?),
			(SELECT COUNT(*) FROM event_sandbox_link WHERE scheduler_id = ? AND scheduler_run_id = ?),
			(SELECT COUNT(*) FROM scheduler_run WHERE scheduler_id = ? AND run_id = ? AND trigger_id <> '')`,
			key.SchedulerID, key.RunID,
			key.SchedulerID, key.RunID,
			key.SchedulerID, key.RunID,
			key.SchedulerID, key.RunID,
		).Scan(&schedulerEvents, &deliveries, &sandboxLinks, &runs)
		if err != nil {
			return schedulers.SchedulerRunPruneDatabaseStats{}, fmt.Errorf("count scheduler run prune data %s/%s: %w", key.SchedulerID, key.RunID, err)
		}
		stats.SchedulerEvents += uint64(schedulerEvents)
		stats.EventDeliveries += uint64(deliveries)
		stats.EventSandboxLinks += uint64(sandboxLinks)
		stats.Runs += uint64(runs)
	}
	return stats, nil
}

func (s *schedulerStore) DeleteSchedulerRunPruneData(ctx context.Context, keys []schedulers.SchedulerRunKey) (schedulers.SchedulerRunPruneDatabaseResult, error) {
	keys = normalizedSchedulerRunKeys(keys)
	if len(keys) == 0 {
		return schedulers.SchedulerRunPruneDatabaseResult{}, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return schedulers.SchedulerRunPruneDatabaseResult{}, fmt.Errorf("begin scheduler run prune: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var result schedulers.SchedulerRunPruneDatabaseResult
	for _, key := range keys {
		removed, err := deleteSchedulerRunPruneRows(ctx, tx, key, &result.Stats)
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

func deleteSchedulerRunPruneRows(ctx context.Context, tx *sql.Tx, key schedulers.SchedulerRunKey, stats *schedulers.SchedulerRunPruneDatabaseStats) (bool, error) {
	eligible, err := schedulerRunPruneKeyIsEligible(ctx, tx, key)
	if err != nil {
		return false, err
	}
	if !eligible {
		return false, nil
	}
	steps := []struct {
		name  string
		query string
		add   func(uint64)
	}{
		{name: "event sandbox links", query: `DELETE FROM event_sandbox_link WHERE scheduler_id = ? AND scheduler_run_id = ?`, add: func(count uint64) { stats.EventSandboxLinks += count }},
		{name: "event deliveries", query: `DELETE FROM event_delivery WHERE scheduler_id = ? AND scheduler_run_id = ?`, add: func(count uint64) { stats.EventDeliveries += count }},
		{name: "scheduler events", query: `DELETE FROM scheduler_event WHERE scheduler_id = ? AND scheduler_run_id = ?`, add: func(count uint64) { stats.SchedulerEvents += count }},
		{name: "scheduler run", query: `DELETE FROM scheduler_run WHERE scheduler_id = ? AND run_id = ? AND trigger_id <> ''`, add: func(count uint64) { stats.Runs += count }},
	}
	for _, step := range steps {
		result, err := tx.ExecContext(ctx, step.query, key.SchedulerID, key.RunID)
		if err != nil {
			return false, fmt.Errorf("delete scheduler run %s %s/%s: %w", step.name, key.SchedulerID, key.RunID, err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("count deleted scheduler run %s %s/%s: %w", step.name, key.SchedulerID, key.RunID, err)
		}
		step.add(uint64(rows))
	}
	return true, nil
}

type schedulerRunPruneQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func schedulerRunPruneKeyIsEligible(ctx context.Context, queryer schedulerRunPruneQueryRower, key schedulers.SchedulerRunKey) (bool, error) {
	var eligible bool
	err := queryer.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM scheduler_run
		WHERE scheduler_id = ? AND run_id = ? AND trigger_id <> ''
		AND status IN (?, ?, ?, ?)
	)`,
		key.SchedulerID, key.RunID,
		domain.SchedulerRunStatusSucceeded,
		domain.SchedulerRunStatusFailed,
		domain.SchedulerRunStatusCanceled,
		domain.SchedulerRunStatusSkipped,
	).Scan(&eligible)
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
