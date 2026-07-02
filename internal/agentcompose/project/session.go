package project

import (
	"context"
	"fmt"
	"strings"
)

type ProjectSessionRelationFilter struct {
	ProjectID string
	AgentName string
	SessionID string
	Statuses  []string
	Limit     int
}

type ProjectSessionStatus[Session any] struct {
	Run            ProjectRunRecord `json:"run"`
	Session        *Session         `json:"session,omitempty"`
	SessionMissing bool             `json:"session_missing,omitempty"`
}

func (s *Store) ListProjectSessionRuns(ctx context.Context, filter ProjectSessionRelationFilter) ([]ProjectRunRecord, error) {
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
	statuses := NormalizeProjectRunStatusFilter(filter.Statuses)
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

func (s *Store) ListProjectRunsForSession(ctx context.Context, sessionID string) ([]ProjectRunRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	return s.ListProjectSessionRuns(ctx, ProjectSessionRelationFilter{SessionID: sessionID})
}

func NormalizeProjectRunStatusFilter(statuses []string) []string {
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
