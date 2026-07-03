package runs

import (
	"context"
	"fmt"
	"strings"

	domain "agent-compose/pkg/model"
)

type ProjectSessionRunStore interface {
	ListProjectSessionRuns(context.Context, domain.ProjectSessionRelationFilter) ([]domain.ProjectRunRecord, error)
}

type SessionStore interface {
	GetSession(context.Context, string) (*domain.Session, error)
}

func ListProjectSessionStatuses(ctx context.Context, runStore ProjectSessionRunStore, sessionStore SessionStore, filter domain.ProjectSessionRelationFilter) ([]domain.ProjectSessionStatus, error) {
	if runStore == nil {
		return nil, fmt.Errorf("config store is required")
	}
	if sessionStore == nil {
		return nil, fmt.Errorf("session store is required")
	}
	runs, err := runStore.ListProjectSessionRuns(ctx, filter)
	if err != nil {
		return nil, err
	}
	items := make([]domain.ProjectSessionStatus, 0, len(runs))
	seenSessions := make(map[string]struct{}, len(runs))
	for _, run := range runs {
		sessionID := strings.TrimSpace(run.SessionID)
		if sessionID == "" {
			continue
		}
		if _, ok := seenSessions[sessionID]; ok {
			continue
		}
		seenSessions[sessionID] = struct{}{}
		item := domain.ProjectSessionStatus{Run: run}
		session, err := sessionStore.GetSession(ctx, sessionID)
		if err != nil {
			item.SessionMissing = true
		} else {
			item.Session = session
		}
		items = append(items, item)
	}
	return items, nil
}
