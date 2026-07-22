package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
)

func (s *loaderStore) GetLoaderRunForLoaders(ctx context.Context, loaderIDs []string, runID string) (domain.LoaderRunSummary, error) {
	loaderIDs = normalizedLoaderRunPageIDs(loaderIDs)
	runID = strings.TrimSpace(runID)
	if len(loaderIDs) == 0 {
		return domain.LoaderRunSummary{}, loaderRunPageNotFound(runID, nil)
	}
	refClause, refArgs, ok := resourceIDClause("run_id", runID)
	if !ok {
		refClause = `run_id = ?`
		refArgs = []any{runID}
	}
	placeholders := make([]string, len(loaderIDs))
	args := make([]any, 0, len(loaderIDs)+len(refArgs))
	args = append(args, refArgs...)
	for index, loaderID := range loaderIDs {
		placeholders[index] = "?"
		args = append(args, loaderID)
	}
	rows, err := s.db.QueryContext(ctx, loaders.SelectLoaderRunSQL()+` WHERE `+refClause+` AND loader_id IN (`+strings.Join(placeholders, ",")+`) AND trigger_id <> '' ORDER BY loader_id ASC, run_id ASC LIMIT 2`, args...)
	if err != nil {
		return domain.LoaderRunSummary{}, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]domain.LoaderRunSummary, 0, 2)
	for rows.Next() {
		item, scanErr := loaders.ScanLoaderRun(rows.Scan)
		if scanErr != nil {
			return domain.LoaderRunSummary{}, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.LoaderRunSummary{}, err
	}
	if len(items) == 0 {
		return domain.LoaderRunSummary{}, loaderRunPageNotFound(runID, sql.ErrNoRows)
	}
	if len(items) > 1 {
		return domain.LoaderRunSummary{}, domain.ResourceError(domain.ErrAmbiguous, "scheduler run", runID, fmt.Sprintf("scheduler run reference %s is ambiguous", runID), nil)
	}
	return items[0], nil
}

func loaderRunPageNotFound(runID string, cause error) error {
	return domain.ResourceError(domain.ErrNotFound, "scheduler run", runID, fmt.Sprintf("scheduler run %s not found", runID), cause)
}

func (s *loaderStore) ListLoaderRunsPage(ctx context.Context, filter loaders.LoaderRunPageFilter) ([]domain.LoaderRunSummary, error) {
	loaderIDs := normalizedLoaderRunPageIDs(filter.LoaderIDs)
	if len(loaderIDs) == 0 {
		return []domain.LoaderRunSummary{}, nil
	}
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	placeholders := make([]string, len(loaderIDs))
	args := make([]any, 0, len(loaderIDs)+7)
	for index, loaderID := range loaderIDs {
		placeholders[index] = "?"
		args = append(args, loaderID)
	}
	query := loaders.SelectLoaderRunSQL() + ` WHERE loader_id IN (` + strings.Join(placeholders, ",") + `)`
	if filter.RequireTrigger {
		query += ` AND trigger_id <> ''`
	}
	if triggerID := strings.TrimSpace(filter.TriggerID); triggerID != "" {
		query += ` AND trigger_id = ?`
		args = append(args, triggerID)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	if !filter.BeforeStartedAt.IsZero() {
		query += ` AND (started_at < ? OR (started_at = ? AND (loader_id < ? OR (loader_id = ? AND run_id < ?))))`
		beforeMillis := filter.BeforeStartedAt.UTC().UnixMilli()
		args = append(args, beforeMillis, beforeMillis, strings.TrimSpace(filter.BeforeLoaderID), strings.TrimSpace(filter.BeforeLoaderID), strings.TrimSpace(filter.BeforeRunID))
	}
	query += ` ORDER BY started_at DESC, loader_id DESC, run_id DESC LIMIT ?`
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query loader run page: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]domain.LoaderRunSummary, 0)
	for rows.Next() {
		item, err := loaders.ScanLoaderRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate loader run page: %w", err)
	}
	return items, nil
}

func (s *loaderStore) ListLoaderRunSandboxIDs(ctx context.Context, keys []loaders.LoaderRunKey) (map[loaders.LoaderRunKey][]string, error) {
	result := make(map[loaders.LoaderRunKey][]string)
	clauses := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys)*2)
	seenKeys := make(map[loaders.LoaderRunKey]struct{}, len(keys))
	for _, key := range keys {
		key.LoaderID = strings.TrimSpace(key.LoaderID)
		key.RunID = strings.TrimSpace(key.RunID)
		if key.LoaderID == "" || key.RunID == "" {
			continue
		}
		if _, ok := seenKeys[key]; ok {
			continue
		}
		seenKeys[key] = struct{}{}
		clauses = append(clauses, `(loader_id = ? AND run_id = ?)`)
		args = append(args, key.LoaderID, key.RunID)
	}
	if len(clauses) == 0 {
		return result, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT loader_id, run_id, linked_sandbox_id FROM loader_event WHERE linked_sandbox_id <> '' AND (`+strings.Join(clauses, ` OR `)+`) ORDER BY loader_id ASC, run_id ASC, linked_sandbox_id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query loader run sandbox ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	seen := make(map[loaders.LoaderRunKey]map[string]struct{})
	for rows.Next() {
		var key loaders.LoaderRunKey
		var sandboxID string
		if err := rows.Scan(&key.LoaderID, &key.RunID, &sandboxID); err != nil {
			return nil, fmt.Errorf("scan loader run sandbox id: %w", err)
		}
		if seen[key] == nil {
			seen[key] = make(map[string]struct{})
		}
		if _, ok := seen[key][sandboxID]; ok {
			continue
		}
		seen[key][sandboxID] = struct{}{}
		result[key] = append(result[key], sandboxID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate loader run sandbox ids: %w", err)
	}
	return result, nil
}

func normalizedLoaderRunPageIDs(loaderIDs []string) []string {
	seen := make(map[string]struct{}, len(loaderIDs))
	result := make([]string, 0, len(loaderIDs))
	for _, loaderID := range loaderIDs {
		loaderID = strings.TrimSpace(loaderID)
		if loaderID == "" {
			continue
		}
		if _, ok := seen[loaderID]; ok {
			continue
		}
		seen[loaderID] = struct{}{}
		result = append(result, loaderID)
	}
	return result
}
