package agentcompose

import (
	"context"
	"log/slog"

	"agent-compose/pkg/sessions"
)

const stalePendingSessionLastError = "session startup interrupted before runtime reached running state"
const staleProjectRunError = "project run interrupted before reaching terminal state"

func (s *Service) reconcilePersistedSessions(ctx context.Context) error {
	return sessions.ReconcilePersistedSessions(ctx, s.store, s.startedAt, s.sessions.ReconcileSessionRuntimeState, s.reconcilePersistedProjectRuns)
}

func (s *Service) reconcilePersistedProjectRuns(ctx context.Context) error {
	if s == nil || s.configDB == nil {
		return nil
	}
	coordinator := NewRunCoordinator(s.configDB)
	for _, status := range []string{ProjectRunStatusPending, ProjectRunStatusRunning} {
		if err := s.reconcilePersistedProjectRunsWithStatus(ctx, coordinator, status); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) reconcilePersistedProjectRunsWithStatus(ctx context.Context, coordinator *RunCoordinator, status string) error {
	var staleRuns []ProjectRunRecord
	offset := 0
	for {
		runs, err := s.configDB.ListProjectRunsByOptions(ctx, ProjectRunListOptions{
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
			if !run.CreatedAt.Before(s.startedAt) {
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
			Error:    staleProjectRunError,
		}); err != nil {
			slog.Warn("failed to mark stale project run failed", "run_id", run.RunID, "error", err)
		}
	}
	return nil
}
