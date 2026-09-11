package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/internal/projects"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// GetProjectSchedulerByID returns an active project scheduler by its globally unique native ID.
func (s *projectStore) GetProjectSchedulerByID(ctx context.Context, schedulerID string) (domain.ProjectSchedulerRecord, error) {
	schedulerID = strings.TrimSpace(schedulerID)
	row := s.db.QueryRowContext(ctx, `SELECT s.id, s.short_id, s.project_id, s.id, s.agent_name, s.revision, s.enabled, s.trigger_count, s.spec_json, s.created_at, s.updated_at
		FROM project_scheduler s
		JOIN project p ON p.id = s.project_id
		WHERE s.id = ? AND p.removed_at = 0 AND s.revision = p.current_revision`, schedulerID)
	item, err := projects.ScanProjectScheduler(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ProjectSchedulerRecord{}, domain.ResourceError(domain.ErrNotFound, "project scheduler", schedulerID, fmt.Sprintf("project scheduler %s not found", schedulerID), err)
		}
		return domain.ProjectSchedulerRecord{}, fmt.Errorf("get project scheduler %s: %w", schedulerID, err)
	}
	return item, nil
}
