package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// UnfinishedProjectRunForSandbox returns a pending or running project run bound
// to the sandbox other than excludeRunID, if there is one.
func (s *projectStore) UnfinishedProjectRunForSandbox(ctx context.Context, sandboxID, excludeRunID string) (string, bool, error) {
	var runID string
	err := s.db.QueryRowContext(ctx, `SELECT run_id FROM project_run WHERE sandbox_id = ? AND run_id <> ? AND status IN (?, ?) LIMIT 1`,
		strings.TrimSpace(sandboxID), strings.TrimSpace(excludeRunID), domain.ProjectRunStatusPending, domain.ProjectRunStatusRunning).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("find unfinished project run for sandbox %s: %w", sandboxID, err)
	}
	return runID, true, nil
}
