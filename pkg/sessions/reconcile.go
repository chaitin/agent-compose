package sessions

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

const stalePendingSessionLastError = "session startup interrupted before runtime reached running state"

type RuntimeReconciler func(context.Context, *Session) (*Session, error)
type ProjectReconciler func(context.Context) error

func ReconcilePersistedSessions(ctx context.Context, store *Store, startedAt time.Time, reconcileRuntime RuntimeReconciler, reconcileProjects ProjectReconciler) error {
	result, err := store.ListSessions(ctx, SessionListOptions{Limit: 1 << 30})
	if err != nil {
		return err
	}
	for _, session := range result.Sessions {
		reconciled, err := ReconcilePendingSessionState(ctx, store, startedAt, session)
		if err != nil {
			slog.Warn("failed to reconcile pending session state", "session_id", session.Summary.ID, "error", err)
			continue
		}
		if reconcileRuntime == nil {
			continue
		}
		if _, err := reconcileRuntime(ctx, reconciled); err != nil {
			slog.Warn("failed to reconcile session runtime state", "session_id", session.Summary.ID, "error", err)
		}
	}
	if reconcileProjects != nil {
		if err := reconcileProjects(ctx); err != nil {
			slog.Warn("failed to reconcile persisted project runs", "error", err)
		}
	}
	return nil
}

func ReconcilePendingSessionState(ctx context.Context, store *Store, startedAt time.Time, session *Session) (*Session, error) {
	if session == nil || session.Summary.VMStatus != VMStatusPending {
		return session, nil
	}
	if !session.Summary.CreatedAt.Before(startedAt) {
		return session, nil
	}
	vmState, err := store.GetVMState(session.Summary.ID)
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
	if err := store.SaveVMState(session.Summary.ID, vmState); err != nil {
		return nil, err
	}
	session.Summary.VMStatus = VMStatusFailed
	if err := store.UpdateSession(ctx, session); err != nil {
		return nil, err
	}
	_ = store.AddEvent(ctx, session.Summary.ID, SessionEvent{
		ID:        uuid.NewString(),
		Type:      "session.startup_interrupted",
		Level:     "warn",
		Message:   "session marked failed after a previous startup was interrupted before the VM became ready",
		CreatedAt: now,
	})
	return store.GetSession(ctx, session.Summary.ID)
}
