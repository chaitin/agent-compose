package agentcompose

import (
	"context"
	"fmt"
	"strings"

	projectdomain "agent-compose/internal/agentcompose/project"
)

type ProjectSessionRelationFilter = projectdomain.ProjectSessionRelationFilter

type ProjectSessionStatus struct {
	Run            ProjectRunRecord `json:"run"`
	Session        *Session         `json:"session,omitempty"`
	SessionMissing bool             `json:"session_missing,omitempty"`
}

func (s *ConfigStore) ListProjectSessionRuns(ctx context.Context, filter ProjectSessionRelationFilter) ([]ProjectRunRecord, error) {
	return s.projectStore().ListProjectSessionRuns(ctx, filter)
}

func (s *ConfigStore) ListProjectRunsForSession(ctx context.Context, sessionID string) ([]ProjectRunRecord, error) {
	return s.projectStore().ListProjectRunsForSession(ctx, sessionID)
}

func ListProjectSessionStatuses(ctx context.Context, configDB *ConfigStore, store *Store, filter ProjectSessionRelationFilter) ([]ProjectSessionStatus, error) {
	if configDB == nil {
		return nil, fmt.Errorf("config store is required")
	}
	if store == nil {
		return nil, fmt.Errorf("session store is required")
	}
	runs, err := configDB.ListProjectSessionRuns(ctx, filter)
	if err != nil {
		return nil, err
	}
	items := make([]ProjectSessionStatus, 0, len(runs))
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
		item := ProjectSessionStatus{Run: run}
		session, err := store.GetSession(ctx, sessionID)
		if err != nil {
			item.SessionMissing = true
		} else {
			item.Session = session
		}
		items = append(items, item)
	}
	return items, nil
}

func normalizeProjectRunStatusFilter(statuses []string) []string {
	return projectdomain.NormalizeProjectRunStatusFilter(statuses)
}
