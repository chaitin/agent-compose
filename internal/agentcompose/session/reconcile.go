package session

import (
	"context"
	"strings"
	"time"
)

const (
	StalePendingSessionLastError = "session startup interrupted before runtime reached running state"
	StartupInterruptedEventType  = "session.startup_interrupted"
	StartupInterruptedMessage    = "session marked failed after a previous startup was interrupted before the VM became ready"
	RuntimeLostEventType         = "session.runtime_lost"
	RuntimeLostMessage           = "session marked stopped after microsandbox runtime became unreachable"
	StaleProjectRunError         = "project run interrupted before reaching terminal state"
)

type PendingStateStore interface {
	GetVMState(string) (VMState, error)
	SaveVMState(string, VMState) error
	UpdateSession(context.Context, *Session) error
	AddEvent(context.Context, string, Event) error
	GetSession(context.Context, string) (*Session, error)
}

type Clock func() time.Time
type IDGenerator func() string

func ReconcilePendingState(ctx context.Context, store PendingStateStore, session *Session, startedAt time.Time, clock Clock, newID IDGenerator) (*Session, error) {
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
	if clock != nil {
		now = clock().UTC()
	}
	vmState.StoppedAt = now
	vmState.BoxID = ""
	if strings.TrimSpace(vmState.LastError) == "" {
		vmState.LastError = StalePendingSessionLastError
	}
	if err := store.SaveVMState(session.Summary.ID, vmState); err != nil {
		return nil, err
	}
	session.Summary.VMStatus = VMStatusFailed
	if err := store.UpdateSession(ctx, session); err != nil {
		return nil, err
	}
	eventID := ""
	if newID != nil {
		eventID = newID()
	}
	_ = store.AddEvent(ctx, session.Summary.ID, Event{
		ID:        eventID,
		Type:      StartupInterruptedEventType,
		Level:     "warn",
		Message:   StartupInterruptedMessage,
		CreatedAt: now,
	})
	return store.GetSession(ctx, session.Summary.ID)
}

func MarkRuntimeLost(ctx context.Context, store PendingStateStore, session *Session, vmState VMState, now time.Time, eventID string) (*Session, error) {
	if session == nil {
		return session, nil
	}
	vmState.StoppedAt = now.UTC()
	vmState.LastError = ""
	vmState.BoxID = ""
	if err := store.SaveVMState(session.Summary.ID, vmState); err != nil {
		return nil, err
	}
	session.Summary.VMStatus = VMStatusStopped
	if err := store.UpdateSession(ctx, session); err != nil {
		return nil, err
	}
	_ = store.AddEvent(ctx, session.Summary.ID, Event{
		ID:        eventID,
		Type:      RuntimeLostEventType,
		Level:     "warn",
		Message:   RuntimeLostMessage,
		CreatedAt: now.UTC(),
	})
	return store.GetSession(ctx, session.Summary.ID)
}
