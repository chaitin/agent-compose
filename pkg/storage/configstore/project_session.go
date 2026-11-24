package configstore

import (
	"context"
	"fmt"
	"strings"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/chaitin/agent-compose/ent/dao/projectrun"
	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/idset"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func (s *projectStore) ListProjectSandboxRuns(ctx context.Context, filter domain.ProjectSandboxRelationFilter) ([]ProjectRunRecord, error) {
	query := s.client.ProjectRun.Query().Where(projectrun.SandboxIDNEQ(""))
	if projectID := strings.TrimSpace(filter.ProjectID); projectID != "" {
		query.Where(projectrun.ProjectIDEQ(projectID))
	}
	if agentName := strings.TrimSpace(filter.AgentName); agentName != "" {
		query.Where(projectrun.AgentNameEQ(agentName))
	}
	if sandboxID := strings.TrimSpace(filter.SandboxID); sandboxID != "" {
		query.Where(projectrun.SandboxIDEQ(sandboxID))
	}
	statuses := projects.NormalizeRunStatusFilter(filter.Statuses)
	if len(statuses) > 0 {
		query.Where(projectrun.StatusIn(statuses...))
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	entities, err := query.
		Order(
			projectrun.ByUpdatedAt(entsql.OrderDesc()),
			projectrun.ByCreatedAt(entsql.OrderDesc()),
			projectrun.ByID(entsql.OrderDesc()),
		).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query project sandbox runs: %w", err)
	}
	var items []ProjectRunRecord
	for _, entity := range entities {
		items = append(items, projectRunFromEntity(entity))
	}
	return items, nil
}

func (s *projectStore) ListLatestProjectRunsForSandboxes(ctx context.Context, sandboxIDs []string) (map[string]ProjectRunRecord, error) {
	ids := idset.Normalize(sandboxIDs)
	if len(ids) == 0 {
		return map[string]ProjectRunRecord{}, nil
	}
	result := make(map[string]ProjectRunRecord, len(ids))
	if err := forEachIDBatch(ids, func(batch []string) error {
		entities, err := s.client.ProjectRun.Query().
			Where(projectrun.SandboxIDIn(batch...)).
			Order(
				projectrun.ByUpdatedAt(entsql.OrderDesc()),
				projectrun.ByCreatedAt(entsql.OrderDesc()),
				projectrun.ByID(entsql.OrderDesc()),
			).
			All(ctx)
		if err != nil {
			return fmt.Errorf("query latest project runs for sandboxes: %w", err)
		}
		for _, entity := range entities {
			item := projectRunFromEntity(entity)
			if _, exists := result[item.SandboxID]; !exists {
				result[item.SandboxID] = item
			}
		}
		return nil
	}); err != nil {
		return nil, err
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
