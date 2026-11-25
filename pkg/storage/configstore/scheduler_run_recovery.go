package configstore

import (
	"context"
	"fmt"
	"time"

	"github.com/chaitin/agent-compose/ent/dao/schedulerrun"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func (s *schedulerStore) ListInterruptedSchedulerRuns(ctx context.Context, startedBefore time.Time) ([]domain.SchedulerRunSummary, error) {
	entities, err := s.client.SchedulerRun.Query().
		Where(
			schedulerrun.StatusEQ(domain.SchedulerRunStatusRunning),
			schedulerrun.StartedAtLT(startedBefore.UTC().UnixMilli()),
		).
		Order(schedulerrun.ByStartedAt(), schedulerrun.BySchedulerID(), schedulerrun.ByID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query interrupted scheduler runs: %w", err)
	}
	result := make([]domain.SchedulerRunSummary, 0, len(entities))
	for _, entity := range entities {
		result = append(result, schedulerRunFromEntity(entity))
	}
	return result, nil
}
