package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
)

func (s *loaderStore) ListLoaderRunsForPrune(ctx context.Context, filter loaders.SchedulerRunPruneFilter) ([]domain.LoaderRunSummary, error) {
	loaderIDs := normalizedLoaderRunPageIDs(filter.LoaderIDs)
	if len(loaderIDs) == 0 {
		return []domain.LoaderRunSummary{}, nil
	}
	placeholders := make([]string, len(loaderIDs))
	args := make([]any, 0, len(loaderIDs)+len(filter.Statuses)+4)
	for index, loaderID := range loaderIDs {
		placeholders[index] = "?"
		args = append(args, loaderID)
	}
	query := loaders.SelectLoaderRunSQL() + ` WHERE loader_id IN (` + strings.Join(placeholders, ",") + `) AND trigger_id <> ''`
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
	query += ` ORDER BY started_at ASC, loader_id ASC, run_id ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query loader runs for prune: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]domain.LoaderRunSummary, 0)
	for rows.Next() {
		run, scanErr := loaders.ScanLoaderRun(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate loader runs for prune: %w", err)
	}
	return result, nil
}

func (s *loaderStore) CountLoaderRunPruneData(ctx context.Context, keys []loaders.LoaderRunKey) (loaders.SchedulerRunPruneDatabaseStats, error) {
	keys = normalizedLoaderRunKeys(keys)
	var stats loaders.SchedulerRunPruneDatabaseStats
	for _, key := range keys {
		eligible, err := schedulerRunPruneKeyIsEligible(ctx, s.db, key)
		if err != nil {
			return loaders.SchedulerRunPruneDatabaseStats{}, err
		}
		if !eligible {
			continue
		}
		var loaderEvents, deliveries, sandboxLinks, runs int64
		err = s.db.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM loader_event WHERE loader_id = ? AND run_id = ?),
			(SELECT COUNT(*) FROM event_delivery WHERE loader_id = ? AND run_id = ?),
			(SELECT COUNT(*) FROM event_sandbox_link WHERE loader_id = ? AND run_id = ?),
			(SELECT COUNT(*) FROM loader_run WHERE loader_id = ? AND run_id = ? AND trigger_id <> '')`,
			key.LoaderID, key.RunID,
			key.LoaderID, key.RunID,
			key.LoaderID, key.RunID,
			key.LoaderID, key.RunID,
		).Scan(&loaderEvents, &deliveries, &sandboxLinks, &runs)
		if err != nil {
			return loaders.SchedulerRunPruneDatabaseStats{}, fmt.Errorf("count scheduler run prune data %s/%s: %w", key.LoaderID, key.RunID, err)
		}
		stats.LoaderEvents += uint64(loaderEvents)
		stats.EventDeliveries += uint64(deliveries)
		stats.EventSandboxLinks += uint64(sandboxLinks)
		stats.Runs += uint64(runs)
	}
	return stats, nil
}

func (s *loaderStore) DeleteLoaderRunPruneData(ctx context.Context, keys []loaders.LoaderRunKey) (loaders.SchedulerRunPruneDatabaseResult, error) {
	keys = normalizedLoaderRunKeys(keys)
	if len(keys) == 0 {
		return loaders.SchedulerRunPruneDatabaseResult{}, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return loaders.SchedulerRunPruneDatabaseResult{}, fmt.Errorf("begin scheduler run prune: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var result loaders.SchedulerRunPruneDatabaseResult
	for _, key := range keys {
		removed, err := deleteLoaderRunPruneRows(ctx, tx, key, &result.Stats)
		if err != nil {
			return loaders.SchedulerRunPruneDatabaseResult{}, err
		}
		if removed {
			result.RemovedKeys = append(result.RemovedKeys, key)
		}
	}
	if err := tx.Commit(); err != nil {
		return loaders.SchedulerRunPruneDatabaseResult{}, fmt.Errorf("commit scheduler run prune: %w", err)
	}
	return result, nil
}

func deleteLoaderRunPruneRows(ctx context.Context, tx *sql.Tx, key loaders.LoaderRunKey, stats *loaders.SchedulerRunPruneDatabaseStats) (bool, error) {
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
		{name: "event sandbox links", query: `DELETE FROM event_sandbox_link WHERE loader_id = ? AND run_id = ?`, add: func(count uint64) { stats.EventSandboxLinks += count }},
		{name: "event deliveries", query: `DELETE FROM event_delivery WHERE loader_id = ? AND run_id = ?`, add: func(count uint64) { stats.EventDeliveries += count }},
		{name: "loader events", query: `DELETE FROM loader_event WHERE loader_id = ? AND run_id = ?`, add: func(count uint64) { stats.LoaderEvents += count }},
		{name: "loader run", query: `DELETE FROM loader_run WHERE loader_id = ? AND run_id = ? AND trigger_id <> ''`, add: func(count uint64) { stats.Runs += count }},
	}
	for _, step := range steps {
		result, err := tx.ExecContext(ctx, step.query, key.LoaderID, key.RunID)
		if err != nil {
			return false, fmt.Errorf("delete scheduler run %s %s/%s: %w", step.name, key.LoaderID, key.RunID, err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("count deleted scheduler run %s %s/%s: %w", step.name, key.LoaderID, key.RunID, err)
		}
		step.add(uint64(rows))
	}
	return true, nil
}

type schedulerRunPruneQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func schedulerRunPruneKeyIsEligible(ctx context.Context, queryer schedulerRunPruneQueryRower, key loaders.LoaderRunKey) (bool, error) {
	var eligible int
	err := queryer.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM loader_run
		WHERE loader_id = ? AND run_id = ? AND trigger_id <> ''
		AND status IN (?, ?, ?, ?)
	)`,
		key.LoaderID, key.RunID,
		domain.LoaderRunStatusSucceeded,
		domain.LoaderRunStatusFailed,
		domain.LoaderRunStatusCanceled,
		domain.LoaderRunStatusSkipped,
	).Scan(&eligible)
	if err != nil {
		return false, fmt.Errorf("recheck scheduler run prune candidate %s/%s: %w", key.LoaderID, key.RunID, err)
	}
	return eligible != 0, nil
}

func normalizedLoaderRunKeys(keys []loaders.LoaderRunKey) []loaders.LoaderRunKey {
	seen := make(map[loaders.LoaderRunKey]struct{}, len(keys))
	result := make([]loaders.LoaderRunKey, 0, len(keys))
	for _, key := range keys {
		key.LoaderID = strings.TrimSpace(key.LoaderID)
		key.RunID = strings.TrimSpace(key.RunID)
		if key.LoaderID == "" || key.RunID == "" {
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
