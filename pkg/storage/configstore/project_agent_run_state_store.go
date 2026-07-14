package configstore

import (
	"context"
	"fmt"
	"strings"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
)

func (s *projectStore) ListProjectAgentRunStates(ctx context.Context, projectID string) ([]domain.ProjectAgentRunState, error) {
	rows, err := s.db.QueryContext(ctx, `WITH ranked AS (
		SELECT agent_name, run_id, status, source, started_at, completed_at, created_at,
			ROW_NUMBER() OVER (PARTITION BY agent_name ORDER BY created_at DESC, run_id DESC) AS position,
			SUM(CASE WHEN status = ? AND source = ? THEN 1 ELSE 0 END) OVER (PARTITION BY agent_name) AS running_scheduler_count,
			SUM(CASE WHEN status = ? AND source != ? THEN 1 ELSE 0 END) OVER (PARTITION BY agent_name) AS running_count
		FROM project_run WHERE project_id = ? AND agent_name != ''
	)
	SELECT agent_name, running_count, running_scheduler_count, run_id, status, source,
		CASE WHEN completed_at != 0 THEN completed_at WHEN started_at != 0 THEN started_at ELSE created_at END
	FROM ranked WHERE position = 1 ORDER BY agent_name`, domain.ProjectRunStatusRunning, domain.ProjectRunSourceScheduler, domain.ProjectRunStatusRunning, domain.ProjectRunSourceScheduler, strings.TrimSpace(projectID))
	if err != nil {
		return nil, fmt.Errorf("query project agent run states: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var states []domain.ProjectAgentRunState
	for rows.Next() {
		state, scanErr := projects.ScanProjectAgentRunState(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project agent run states: %w", err)
	}
	return states, nil
}
