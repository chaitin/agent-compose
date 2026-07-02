package projects

import (
	"context"
	"log/slog"
	"time"
)

const StaleProjectRunError = "project run interrupted before reaching terminal state"

func ReconcilePersistedRuns(ctx context.Context, store *ConfigStore, startedAt time.Time) error {
	if store == nil {
		return nil
	}
	coordinator := NewRunCoordinator(store)
	for _, status := range []string{ProjectRunStatusPending, ProjectRunStatusRunning} {
		if err := reconcilePersistedRunsWithStatus(ctx, store, coordinator, startedAt, status); err != nil {
			return err
		}
	}
	return nil
}

func reconcilePersistedRunsWithStatus(ctx context.Context, store *ConfigStore, coordinator *RunCoordinator, startedAt time.Time, status string) error {
	var staleRuns []ProjectRunRecord
	offset := 0
	for {
		runs, err := store.ListProjectRunsByOptions(ctx, ProjectRunListOptions{
			Status: status,
			Limit:  200,
			Offset: offset,
		})
		if err != nil {
			return err
		}
		if len(runs) == 0 {
			break
		}
		for _, run := range runs {
			if !run.CreatedAt.Before(startedAt) {
				continue
			}
			staleRuns = append(staleRuns, run)
		}
		offset += len(runs)
	}
	for _, run := range staleRuns {
		if _, err := coordinator.MarkFailed(ctx, ProjectRunTransitionRequest{
			RunID:    run.RunID,
			ExitCode: firstNonZeroInt(run.ExitCode, 1),
			Error:    StaleProjectRunError,
		}); err != nil {
			slog.Warn("failed to mark stale project run failed", "run_id", run.RunID, "error", err)
		}
	}
	return nil
}
