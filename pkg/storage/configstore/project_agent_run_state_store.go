package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/internal/projects"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// agentRunningCounts tracks the non-scheduler and scheduler runs currently in
// flight for one project agent.
type agentRunningCounts struct {
	running          uint32
	runningScheduler uint32
}

// ListProjectAgentRunStates returns the latest run plus running counts for each
// project agent. It replaces a single window-function query — which scanned and
// partitioned every run for the project — with two narrow queries: one that only
// touches the running rows, and one that resolves the latest run per agent
// through the (project_id, agent_name, created_at) index. The two queries share
// a read transaction so a run completing or starting between them cannot make
// the running counts disagree with the reported latest run.
func (s *projectStore) ListProjectAgentRunStates(ctx context.Context, projectID string) ([]domain.ProjectAgentRunState, error) {
	projectID = strings.TrimSpace(projectID)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin project agent run state transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	running, err := s.projectAgentRunningCounts(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	latest, err := s.projectAgentLatestRuns(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit project agent run state transaction: %w", err)
	}
	states := make([]domain.ProjectAgentRunState, 0, len(latest))
	for _, item := range latest {
		if counts, ok := running[item.AgentName]; ok {
			item.RunningRunCount = counts.running
			item.RunningSchedulerRunCount = counts.runningScheduler
		}
		states = append(states, item)
	}
	return states, nil
}

func (s *projectStore) projectAgentRunningCounts(ctx context.Context, tx *sql.Tx, projectID string) (map[string]agentRunningCounts, error) {
	// Kept as SQL: SUM(CASE ...) grouped by agent.
	rows, err := tx.QueryContext(ctx, `SELECT agent_name,
		SUM(CASE WHEN source = ? THEN 0 ELSE 1 END),
		SUM(CASE WHEN source = ? THEN 1 ELSE 0 END)
	FROM project_run
	WHERE project_id = ? AND status = ? AND agent_name != ''
	GROUP BY agent_name`,
		domain.ProjectRunSourceScheduler, domain.ProjectRunSourceScheduler, projectID, domain.ProjectRunStatusRunning)
	if err != nil {
		return nil, fmt.Errorf("query project agent running counts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make(map[string]agentRunningCounts)
	for rows.Next() {
		var name string
		var running, runningScheduler int64
		if err := rows.Scan(&name, &running, &runningScheduler); err != nil {
			return nil, err
		}
		result[name] = agentRunningCounts{running: uint32(running), runningScheduler: uint32(runningScheduler)}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project agent running counts: %w", err)
	}
	return result, nil
}

func (s *projectStore) projectAgentLatestRuns(ctx context.Context, tx *sql.Tx, projectID string) ([]domain.ProjectAgentRunState, error) {
	// Kept as SQL: correlated latest-per-agent subquery plus a CASE that picks
	// completed_at, started_at, or created_at.
	rows, err := tx.QueryContext(ctx, `SELECT r.agent_name, 0, 0, r.run_id, r.status, r.source,
		CASE WHEN r.completed_at != 0 THEN r.completed_at WHEN r.started_at != 0 THEN r.started_at ELSE r.created_at END
	FROM project_run r
	WHERE r.project_id = ? AND r.agent_name != ''
		AND r.run_id = (
			SELECT run_id FROM project_run
			WHERE project_id = r.project_id AND agent_name = r.agent_name
			ORDER BY created_at DESC, run_id DESC LIMIT 1
		)
	ORDER BY r.agent_name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query project agent latest runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var states []domain.ProjectAgentRunState
	for rows.Next() {
		item, err := projects.ScanProjectAgentRunState(rows.Scan)
		if err != nil {
			return nil, err
		}
		states = append(states, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project agent latest runs: %w", err)
	}
	return states, nil
}
