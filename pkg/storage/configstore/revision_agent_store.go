package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/internal/projects"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storedtime"
)

func (s *coreStore) loadRevisionAgentDefinition(ctx context.Context, agentID string) (domain.AgentDefinition, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return domain.AgentDefinition{}, fmt.Errorf("agent id is required")
	}
	// Kept as SQL: joins project_agent, project, and project_revision as one
	// consistent read, which needs a multi-table join.
	row := s.db.QueryRowContext(ctx, `SELECT
		p.id, p.name, p.short_id, p.source_path, p.source_json,
		p.current_revision, p.spec_hash, p.created_at, p.updated_at, p.removed_at,
		a.agent_name, a.revision, r.spec_hash, r.spec_json, r.created_at
		FROM project_agent a
		JOIN project p ON p.id = a.project_id
		JOIN project_revision r ON r.project_id = a.project_id AND r.revision = a.revision
		WHERE a.id = ?`, agentID)
	var project domain.ProjectRecord
	var revision domain.ProjectRevisionRecord
	var agentName string
	var projectCreated, projectUpdated, projectRemoved, revisionCreated any
	if err := row.Scan(
		&project.ID, &project.Name, &project.ShortID, &project.SourcePath,
		&project.SourceJSON, &project.CurrentRevision, &project.SpecHash,
		&projectCreated, &projectUpdated, &projectRemoved, &agentName,
		&revision.Revision, &revision.SpecHash, &revision.SpecJSON, &revisionCreated,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.AgentDefinition{}, domain.ResourceError(domain.ErrNotFound, "agent", agentID, fmt.Sprintf("agent %s not found", agentID), err)
		}
		return domain.AgentDefinition{}, fmt.Errorf("load revision agent %s: %w", agentID, err)
	}
	project.CreatedAt = storedtime.ParseStoredTime(projectCreated)
	project.UpdatedAt = storedtime.ParseStoredTime(projectUpdated)
	project.RemovedAt = storedtime.ParseStoredTime(projectRemoved)
	revision.ProjectID = project.ID
	revision.CreatedAt = storedtime.ParseStoredTime(revisionCreated)
	return projects.AgentDefinitionFromRevision(project, revision, agentName)
}
