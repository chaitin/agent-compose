package schedulers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

const interruptedSchedulerRunError = "daemon interrupted scheduler trigger run before reaching terminal state"

type interruptedSchedulerRunStore interface {
	ListInterruptedSchedulerRuns(context.Context, time.Time) ([]domain.SchedulerRunSummary, error)
}

type interruptedSchedulerRunEventStore interface {
	ListSchedulerEventsPage(context.Context, SchedulerEventPageFilter) ([]domain.SchedulerEvent, error)
}

// InterruptedRunSandboxStopper stops one sandbox by ID. Stopping a sandbox that
// is already stopped is a no-op.
type InterruptedRunSandboxStopper interface {
	Shutdown(ctx context.Context, sandboxID string) error
}

const interruptedRunEventPageSize = 200

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
		run.Status = domain.SchedulerRunStatusFailed
		run.CompletedAt = completedAt
		run.DurationMs = max(completedAt.Sub(run.StartedAt).Milliseconds(), 0)
		run.Error = interruptedSchedulerRunError
		if err := c.deps.Store.UpdateSchedulerRun(ctx, run); err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("recover interrupted scheduler run %s/%s: %w", run.SchedulerID, run.ID, err))
			continue
		}
		if _, err := c.AddSchedulerEventRecord(ctx, SchedulerEventInput{
			SchedulerID: run.SchedulerID, RunID: run.ID, TriggerID: run.TriggerID,
			EventType: "scheduler.run.failed", Level: "error", Message: interruptedSchedulerRunError,
			Payload: map[string]any{"reason": "daemon_interrupted"},
		}); err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("record interrupted scheduler run event %s/%s: %w", run.SchedulerID, run.ID, err))
		}
		if err := c.stopInterruptedRunSandboxes(ctx, run); err != nil {
			recoveryErrors = append(recoveryErrors, err)
		}
	}
	return errors.Join(recoveryErrors...)
}

// stopInterruptedRunSandboxes stops the sandboxes run had scheduled to stop at
// its end. Sandboxes a run leaves running by design record no such event and
// are not touched.
func (c *Controller) stopInterruptedRunSandboxes(ctx context.Context, run domain.SchedulerRunSummary) error {
	if c.deps.InterruptedSandboxes == nil {
		return nil
	}
	sandboxIDs, err := c.interruptedRunScheduledStops(ctx, run)
	if err != nil {
		return fmt.Errorf("list sandboxes of interrupted scheduler run %s/%s: %w", run.SchedulerID, run.ID, err)
	}
	var stopErrors []error
	for _, sandboxID := range sandboxIDs {
		event := SchedulerEventInput{
			SchedulerID: run.SchedulerID, RunID: run.ID, TriggerID: run.TriggerID,
			EventType: "scheduler.sandbox.stopped", Level: "info", Message: "scheduler sandbox stopped after interrupted run",
			Payload: map[string]any{"sandboxId": sandboxID, "reason": "daemon_interrupted"}, LinkedSandboxID: sandboxID,
		}
		if err := c.deps.InterruptedSandboxes.Shutdown(ctx, sandboxID); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			stopErrors = append(stopErrors, fmt.Errorf("stop sandbox %s of interrupted scheduler run %s/%s: %w", sandboxID, run.SchedulerID, run.ID, err))
			event.EventType, event.Level, event.Message = "scheduler.sandbox.stop_failed", "error", err.Error()
		}
		if _, err := c.AddSchedulerEventRecord(ctx, event); err != nil {
			stopErrors = append(stopErrors, fmt.Errorf("record sandbox stop of interrupted scheduler run %s/%s: %w", run.SchedulerID, run.ID, err))
		}
	}
	return errors.Join(stopErrors...)
}

func (c *Controller) interruptedRunScheduledStops(ctx context.Context, run domain.SchedulerRunSummary) ([]string, error) {
	store, ok := c.deps.Store.(interruptedSchedulerRunEventStore)
	if !ok || store == nil {
		return nil, fmt.Errorf("scheduler event store is unavailable")
	}
	var sandboxIDs []string
	seen := map[string]struct{}{}
	for offset := 0; ; offset += interruptedRunEventPageSize {
		events, err := store.ListSchedulerEventsPage(ctx, SchedulerEventPageFilter{
			SchedulerIDs: []string{run.SchedulerID}, RunID: run.ID,
			Ascending: true, Offset: offset, Limit: interruptedRunEventPageSize,
		})
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			sandboxID := strings.TrimSpace(event.LinkedSandboxID)
			if event.Type != SandboxStopScheduledEventType || sandboxID == "" {
				continue
			}
			if _, ok := seen[sandboxID]; ok {
				continue
			}
			seen[sandboxID] = struct{}{}
			sandboxIDs = append(sandboxIDs, sandboxID)
		}
		if len(events) < interruptedRunEventPageSize {
			return sandboxIDs, nil
		}
	}
}
