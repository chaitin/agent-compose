package configstore

import (
	"context"
	"fmt"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

func (s *schedulerStore) ListInterruptedSchedulerRuns(ctx context.Context, startedBefore time.Time) ([]domain.SchedulerRunSummary, error) {
	rows, err := s.db.QueryContext(ctx, schedulers.SelectSchedulerRunSQL()+`
		WHERE status = ? AND started_at < ?
		ORDER BY started_at ASC, scheduler_id ASC, run_id ASC`,
		domain.SchedulerRunStatusRunning, startedBefore.UTC().UnixMilli(),
	)
	if err != nil {
		return nil, fmt.Errorf("query interrupted scheduler runs: %w", err)
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
		return nil, fmt.Errorf("iterate interrupted scheduler runs: %w", err)
	}
	return result, nil
}
