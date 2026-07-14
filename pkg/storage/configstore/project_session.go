package configstore

import (
	"context"
	"fmt"
	"strings"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
)

func (s *projectStore) ListProjectSandboxRuns(ctx context.Context, filter domain.ProjectSandboxRelationFilter) ([]ProjectRunRecord, error) {
	query := projects.SelectProjectRunSQL() + ` WHERE sandbox_id != ''`
	args := make([]any, 0, 4+len(filter.Statuses))
	if projectID := strings.TrimSpace(filter.ProjectID); projectID != "" {
		query += ` AND project_id = ?`
		args = append(args, projectID)
	}
	if agentName := strings.TrimSpace(filter.AgentName); agentName != "" {
		query += ` AND agent_name = ?`
		args = append(args, agentName)
	}
	if sandboxID := strings.TrimSpace(filter.SandboxID); sandboxID != "" {
		query += ` AND sandbox_id = ?`
		args = append(args, sandboxID)
	}
	statuses := projects.NormalizeRunStatusFilter(filter.Statuses)
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
		return nil, fmt.Errorf("query project sandbox runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var items []ProjectRunRecord
	for rows.Next() {
		item, err := projects.ScanProjectRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project sandbox runs: %w", err)
	}
	return items, nil
}

func (s *projectStore) ListLatestProjectRunsForSandboxes(ctx context.Context, sandboxIDs []string) (map[string]ProjectRunRecord, error) {
	ids := normalizedNonEmptyStrings(sandboxIDs)
	if len(ids) == 0 {
		return map[string]ProjectRunRecord{}, nil
	}
	query := projects.SelectProjectRunSQL() + ` WHERE sandbox_id IN (` + placeholders(len(ids)) + `) ORDER BY updated_at DESC, created_at DESC, run_id DESC`
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query latest project runs for sandboxes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make(map[string]ProjectRunRecord, len(ids))
	for rows.Next() {
		item, err := projects.ScanProjectRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		if _, exists := result[item.SandboxID]; !exists {
			result[item.SandboxID] = item
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate latest project runs for sandboxes: %w", err)
	}
	return result, nil
}

func (s *projectStore) ListProjectRunsForSandbox(ctx context.Context, sandboxID string) ([]ProjectRunRecord, error) {
	sandboxID = strings.TrimSpace(sandboxID)
	if sandboxID == "" {
		return nil, fmt.Errorf("sandbox id is required")
	}
	return s.ListProjectSandboxRuns(ctx, domain.ProjectSandboxRelationFilter{SandboxID: sandboxID})
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

func normalizedNonEmptyStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
