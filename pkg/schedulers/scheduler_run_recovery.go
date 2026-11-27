package schedulers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
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
		if err := c.closeInterruptedSteps(ctx, run); err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("close interrupted steps of scheduler run %s/%s: %w", run.SchedulerID, run.ID, err))
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

const interruptedSchedulerStepError = "daemon interrupted scheduler step before it finished"

// interruptedStepStore lists the events of a scheduler run, so recovery can
// find steps that started and never finished.
type interruptedStepStore interface {
	ListSchedulerEventsPage(context.Context, SchedulerEventPageFilter) ([]domain.SchedulerEvent, error)
}

// stepLogPaths locates the log of a scheduler run's step.
type stepLogPaths interface {
	StepLogPath(schedulerID, runID, cellID string) string
}

// closeInterruptedSteps fails the shell/exec steps an interrupted run left
// started. Steps do not survive a restart: the script that waits for them is
// gone. Each gets the failed event it never had, and its log says why it ends.
func (c *Controller) closeInterruptedSteps(ctx context.Context, run domain.SchedulerRunSummary) error {
	store, ok := c.deps.Store.(interruptedStepStore)
	if !ok {
		return nil
	}
	started := map[string]domain.SchedulerEvent{}
	var order []string
	const page = 500
	for offset := 0; ; offset += page {
		events, err := store.ListSchedulerEventsPage(ctx, SchedulerEventPageFilter{SchedulerIDs: []string{run.SchedulerID}, RunID: run.ID, Ascending: true, Limit: page, Offset: offset})
		if err != nil {
			return err
		}
		for _, event := range events {
			cellID := strings.TrimSpace(event.LinkedCellID)
			if cellID == "" {
				continue
			}
			switch event.Type {
			case "scheduler.command.started":
				if _, seen := started[cellID]; !seen {
					order = append(order, cellID)
				}
				started[cellID] = event
			case "scheduler.command.completed", "scheduler.command.failed":
				delete(started, cellID)
			}
		}
		if len(events) < page {
			break
		}
	}
	var stepErrors []error
	for _, cellID := range order {
		event, open := started[cellID]
		if !open {
			continue
		}
		if paths, ok := c.deps.Artifacts.(stepLogPaths); ok {
			if err := appendStepLogError(paths.StepLogPath(run.SchedulerID, run.ID, cellID), interruptedSchedulerStepError); err != nil {
				stepErrors = append(stepErrors, fmt.Errorf("step %s log: %w", cellID, err))
			}
		}
		if _, err := c.AddSchedulerEventRecord(ctx, SchedulerEventInput{
			SchedulerID: run.SchedulerID, RunID: run.ID, TriggerID: run.TriggerID,
			EventType: "scheduler.command.failed", Level: "error", Message: interruptedSchedulerStepError,
			Payload:         map[string]any{"reason": "daemon_interrupted", "cellId": cellID, "sandboxId": event.LinkedSandboxID},
			LinkedSandboxID: event.LinkedSandboxID, LinkedCellID: cellID,
		}); err != nil {
			stepErrors = append(stepErrors, fmt.Errorf("step %s: %w", cellID, err))
		}
	}
	return errors.Join(stepErrors...)
}

func appendStepLogError(path, message string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	event, err := json.Marshal(map[string]string{"kind": "error", "severity": "error", "message": message})
	if err != nil {
		return err
	}
	w, err := runlog.Open(path, runlog.Options{})
	if err != nil {
		return err
	}
	return errors.Join(w.AppendAgentEvent(event), w.Close())
}
