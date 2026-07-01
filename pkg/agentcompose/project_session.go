package agentcompose

import (
	"context"
	"fmt"
	"strings"

	"agent-compose/pkg/model"
)

type ProjectSessionRelationFilter = model.ProjectSessionRelationFilter
type ProjectSessionStatus = model.ProjectSessionStatus

func (s *ConfigStore) ListProjectSessionRuns(ctx context.Context, filter ProjectSessionRelationFilter) ([]ProjectRunRecord, error) {
	query := selectProjectRunSQL() + ` WHERE session_id != ''`
	args := make([]any, 0, 4+len(filter.Statuses))
	if projectID := strings.TrimSpace(filter.ProjectID); projectID != "" {
		query += ` AND project_id = ?`
		args = append(args, projectID)
	}
	if agentName := strings.TrimSpace(filter.AgentName); agentName != "" {
		query += ` AND agent_name = ?`
		args = append(args, agentName)
	}
	if sessionID := strings.TrimSpace(filter.SessionID); sessionID != "" {
		query += ` AND session_id = ?`
		args = append(args, sessionID)
	}
	statuses := normalizeProjectRunStatusFilter(filter.Statuses)
	if len(statuses) > 0 {
		query += ` AND status IN (` + placeholders(len(statuses)) + `)`
		for _, status := range statuses {
			args = append(args, status)
		}
	}
	query += ` ORDER BY updated_at DESC, created_at DESC, run_id DESC`
	limit := filter.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	query += ` LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query project session runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var items []ProjectRunRecord
	for rows.Next() {
		item, err := scanProjectRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project session runs: %w", err)
	}
	return items, nil
}

func (s *ConfigStore) ListProjectRunsForSession(ctx context.Context, sessionID string) ([]ProjectRunRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	return s.ListProjectSessionRuns(ctx, ProjectSessionRelationFilter{SessionID: sessionID})
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
	seen := make(map[string]struct{}, len(statuses))
	normalized := make([]string, 0, len(statuses))
	for _, status := range statuses {
		status = strings.ToLower(strings.TrimSpace(status))
		if status == "" {
			continue
		}
		switch status {
		case ProjectRunStatusPending, ProjectRunStatusRunning, ProjectRunStatusSucceeded, ProjectRunStatusFailed, ProjectRunStatusCanceled:
		default:
			continue
		}
		if _, ok := seen[status]; ok {
			continue
		}
		seen[status] = struct{}{}
		normalized = append(normalized, status)
	}
	return normalized
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	values := make([]string, count)
	for i := range values {
		values[i] = "?"
	}
	return strings.Join(values, ",")
}
