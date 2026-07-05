package agentcompose

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

const stalePendingSessionLastError = "session startup interrupted before runtime reached running state"
const staleProjectRunError = "project run interrupted before reaching terminal state"
const orphanedRunningCellError = "cell execution interrupted by daemon restart"

func (s *Service) reconcilePersistedSessions(ctx context.Context) error {
	result, err := s.store.ListSessions(ctx, SessionListOptions{Limit: 1 << 30})
	if err != nil {
		return err
	}
	for _, session := range result.Sessions {
		reconciled, err := s.reconcilePendingSessionState(ctx, session)
		if err != nil {
			slog.Warn("failed to reconcile pending session state", "session_id", session.Summary.ID, "error", err)
			continue
		}
		if _, err := s.reconcileSessionRuntimeState(ctx, reconciled); err != nil {
			slog.Warn("failed to reconcile session runtime state", "session_id", session.Summary.ID, "error", err)
		}
		if err := s.reconcileOrphanedRunningCells(ctx, session); err != nil {
			slog.Warn("failed to reconcile orphaned running cells", "session_id", session.Summary.ID, "error", err)
		}
	}
	if err := s.reconcilePersistedProjectRuns(ctx); err != nil {
		slog.Warn("failed to reconcile persisted project runs", "error", err)
	}
	return nil
}

func (s *Service) reconcilePendingSessionState(ctx context.Context, session *Session) (*Session, error) {
	if session == nil || session.Summary.VMStatus != VMStatusPending {
		return session, nil
	}
	if !session.Summary.CreatedAt.Before(s.startedAt) {
		return session, nil
	}
	vmState, err := s.store.GetVMState(session.Summary.ID)
	if err != nil {
		return nil, err
	}
	if !vmState.StartedAt.IsZero() {
		return session, nil
	}
	now := time.Now().UTC()
	vmState.StoppedAt = now
	vmState.BoxID = ""
	if strings.TrimSpace(vmState.LastError) == "" {
		vmState.LastError = stalePendingSessionLastError
	}
	if err := s.store.SaveVMState(session.Summary.ID, vmState); err != nil {
		return nil, err
	}
	session.Summary.VMStatus = VMStatusFailed
	if err := s.store.UpdateSession(ctx, session); err != nil {
		return nil, err
	}
	_ = s.store.AddEvent(ctx, session.Summary.ID, SessionEvent{
		ID:        uuid.NewString(),
		Type:      "session.startup_interrupted",
		Level:     "warn",
		Message:   "session marked failed after a previous startup was interrupted before the VM became ready",
		CreatedAt: now,
	})
	return s.store.GetSession(ctx, session.Summary.ID)
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

// reconcileOrphanedRunningCells converges cells that were marked as Running
// when the daemon previously stopped. These orphaned cells will otherwise
// remain in Running state indefinitely, presenting a false view to users
// and schedulers.
func (s *Service) reconcileOrphanedRunningCells(ctx context.Context, session *Session) error {
	if session == nil {
		return nil
	}
	cells, err := s.store.ListCells(ctx, session.Summary.ID)
	if err != nil {
		return err
	}
	var converged int
	now := time.Now().UTC()
	for i := range cells {
		if !cells[i].Running {
			continue
		}
		// Only converge cells created before this daemon instance started.
		// Cells created after startedAt belong to the current daemon lifecycle.
		if !cells[i].CreatedAt.Before(s.startedAt) {
			continue
		}
		cells[i].Running = false
		cells[i].Success = false
		cells[i].ExitCode = 1
		converged++
		slog.Info("converged orphaned running cell",
			"session_id", session.Summary.ID,
			"cell_id", cells[i].ID,
			"cell_type", cells[i].Type,
			"cell_source", truncateCellSource(cells[i].Source, 80),
		)
		_ = s.store.AddEvent(ctx, session.Summary.ID, SessionEvent{
			ID:        uuid.NewString(),
			Type:      "cell.execution_interrupted",
			Level:     "warn",
			Message:   orphanedRunningCellError,
			CreatedAt: now,
		})
	}
	if converged == 0 {
		return nil
	}
	if err := s.store.saveCells(session.Summary.ID, cells); err != nil {
		return err
	}
	slog.Info("converged orphaned running cells", "session_id", session.Summary.ID, "count", converged)
	return nil
}

// truncateCellSource returns a truncated version of source for logging.
func truncateCellSource(source string, maxLen int) string {
	if len(source) <= maxLen {
		return source
	}
	return source[:maxLen] + "..."
}
