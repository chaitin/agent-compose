package configstore

import (
	"maps"
	"slices"
	"strings"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
)

func projectRunFilter(options model.ProjectRunListOptions) ([]string, []any) {
	where := make([]string, 0, 9)
	args := make([]any, 0, 8)
	if projectID := strings.TrimSpace(options.ProjectID); projectID != "" {
		where = append(where, "project_id = ?")
		args = append(args, projectID)
	}
	if agentName := strings.TrimSpace(options.AgentName); agentName != "" {
		where = append(where, "agent_name = ?")
		args = append(args, agentName)
	}
	if sandboxID := strings.TrimSpace(options.SandboxID); sandboxID != "" {
		where = append(where, "sandbox_id = ?")
		args = append(args, sandboxID)
	}
	if schedulerID := strings.TrimSpace(options.SchedulerID); schedulerID != "" {
		where = append(where, "scheduler_id = ?")
		args = append(args, schedulerID)
	}
	if schedulerRunID := strings.TrimSpace(options.SchedulerRunID); schedulerRunID != "" {
		where = append(where, "scheduler_run_id = ?")
		args = append(args, schedulerRunID)
	}
	if status := strings.TrimSpace(options.Status); status != "" {
		where = append(where, "status = ?")
		args = append(args, projects.NormalizeRunStatus(status))
	}
	if source := strings.TrimSpace(options.Source); source != "" {
		where = append(where, "source = ?")
		args = append(args, runs.NormalizeSource(source))
	}
	if options.StartedFrom != nil || options.StartedTo != nil {
		where = append(where, "started_at > 0")
	}
	if options.StartedFrom != nil {
		where = append(where, "started_at >= ?")
		args = append(args, options.StartedFrom.UnixMilli())
	}
	if options.StartedTo != nil {
		where = append(where, "started_at <= ?")
		args = append(args, options.StartedTo.UnixMilli())
	}
	// Sort keys first so the generated SQL text (and therefore its prepared
	// statement cache key) is stable across calls with the same filter set.
	for _, key := range slices.Sorted(maps.Keys(options.Labels)) {
		where = append(where, "EXISTS (SELECT 1 FROM project_run_label WHERE run_id = project_run.run_id AND key = ? AND value = ?)")
		args = append(args, key, options.Labels[key])
	}
	return where, args
}
