package schedulers

import (
	"context"
	"errors"
	"fmt"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

const interruptedSchedulerRunError = "daemon interrupted scheduler trigger run before reaching terminal state"

type interruptedSchedulerRunStore interface {
	ListInterruptedSchedulerRuns(context.Context, time.Time) ([]domain.SchedulerRunSummary, error)
}

func (c *Controller) RecoverInterruptedRuns(ctx context.Context, startedAt time.Time) error {
	store, ok := c.deps.Store.(interruptedSchedulerRunStore)
	if !ok || store == nil {
		return fmt.Errorf("scheduler run recovery store is unavailable")
	}
	runs, err := store.ListInterruptedSchedulerRuns(ctx, startedAt.UTC())
	if err != nil {
		return err
	}
	completedAt := c.now()
	var recoveryErrors []error
	for _, run := range runs {
		previousStatus := run.Status
		run.Status = domain.SchedulerRunStatusFailed
		run.CompletedAt = completedAt
		run.DurationMs = max(completedAt.Sub(run.StartedAt).Milliseconds(), 0)
		run.Error = interruptedSchedulerRunError
		if err := c.deps.Store.UpdateSchedulerRun(ctx, run); err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("recover interrupted scheduler run %s/%s: %w", run.SchedulerID, run.ID, err))
			continue
		}
		if c.deps.Observer != nil {
			projectID, agentName := c.schedulerIdentity(ctx, run.SchedulerID)
			c.deps.Observer.SchedulerRunStatusChanged(ctx, previousStatus, run, projectID, agentName)
		}
		if _, err := c.AddSchedulerEventRecord(ctx, SchedulerEventInput{
			SchedulerID: run.SchedulerID, RunID: run.ID, TriggerID: run.TriggerID,
			EventType: "scheduler.run.failed", Level: "error", Message: interruptedSchedulerRunError,
			Payload: map[string]any{"reason": "daemon_interrupted"},
		}); err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("record interrupted scheduler run event %s/%s: %w", run.SchedulerID, run.ID, err))
		}
	}
	return errors.Join(recoveryErrors...)
}
