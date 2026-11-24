package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/project"
	"github.com/chaitin/agent-compose/internal/projects"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func (s *projectStore) GetProjectByName(ctx context.Context, name string, includeRemoved bool) (ProjectRecord, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ProjectRecord{}, fmt.Errorf("project name is required")
	}
	query := s.client.Project.Query().Where(project.NameEQ(name))
	if !includeRemoved {
		query.Where(project.RemovedAtEQ(0))
	}
	entity, err := query.Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return ProjectRecord{}, domain.ResourceError(domain.ErrNotFound, "project", name, fmt.Sprintf("project with name %s not found", name), scanNotFound("project"))
		}
		return ProjectRecord{}, err
	}
	return projectFromEntity(entity), nil
}

func (s *projectStore) GetProjectBySourcePath(ctx context.Context, sourcePath string, includeRemoved bool) (ProjectRecord, error) {
	sourcePath = projects.NormalizeProjectSourcePath(sourcePath)
	if sourcePath == "" {
		return ProjectRecord{}, fmt.Errorf("project source path is required")
	}
	query := s.client.Project.Query().Where(project.SourcePathEQ(sourcePath))
	if !includeRemoved {
		query.Where(project.RemovedAtEQ(0))
	}
	entities, err := query.
		Order(
			project.ByUpdatedAt(entsql.OrderDesc()),
			project.ByCreatedAt(entsql.OrderDesc()),
			project.ByID(),
		).
		Limit(2).
		All(ctx)
	if err != nil {
		return ProjectRecord{}, fmt.Errorf("query project by source path %s: %w", sourcePath, err)
	}
	items := make([]ProjectRecord, 0, 2)
	for _, entity := range entities {
		items = append(items, projectFromEntity(entity))
	}
	if len(items) == 0 {
		return ProjectRecord{}, domain.ResourceError(domain.ErrNotFound, "project", sourcePath, fmt.Sprintf("project with source path %s not found", sourcePath), sql.ErrNoRows)
	}
	if len(items) > 1 {
		return ProjectRecord{}, domain.ClassifyError(domain.ErrAmbiguous, fmt.Sprintf("project source path %s is ambiguous; use project_id", sourcePath), nil)
	}
	return items[0], nil
}
