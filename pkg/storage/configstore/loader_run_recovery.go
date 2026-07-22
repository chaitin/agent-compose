package configstore

import (
	"context"
	"fmt"
	"time"

	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
)

func (s *loaderStore) ListInterruptedLoaderRuns(ctx context.Context, startedBefore time.Time) ([]domain.LoaderRunSummary, error) {
	rows, err := s.db.QueryContext(ctx, loaders.SelectLoaderRunSQL()+`
		WHERE trigger_id <> '' AND status = ? AND started_at < ?
		ORDER BY started_at ASC, loader_id ASC, run_id ASC`,
		domain.LoaderRunStatusRunning, startedBefore.UTC().UnixMilli(),
	)
	if err != nil {
		return nil, fmt.Errorf("query interrupted scheduler runs: %w", err)
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
		return nil, fmt.Errorf("iterate interrupted scheduler runs: %w", err)
	}
	return result, nil
}
