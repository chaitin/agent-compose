package agentcompose

import (
	"context"

	"agent-compose/pkg/projects"
	"agent-compose/pkg/sessions"
)

const stalePendingSessionLastError = "session startup interrupted before runtime reached running state"

func (s *Service) reconcilePersistedSessions(ctx context.Context) error {
	return sessions.ReconcilePersistedSessions(ctx, s.store, s.startedAt, s.sessions.ReconcileSessionRuntimeState, s.reconcilePersistedProjectRuns)
}

func (s *Service) reconcilePersistedProjectRuns(ctx context.Context) error {
	if s == nil || s.configDB == nil {
		return nil
	}
	return projects.ReconcilePersistedRuns(ctx, s.configDB, s.startedAt)
}
