package configstore

import (
	"context"
	"fmt"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// MarkProjectRunLogIncomplete marks an observability failure without changing
// execution status or overwriting another writer's fields.
func (s *projectStore) MarkProjectRunLogIncomplete(ctx context.Context, runID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE project_run SET log_incomplete=1 WHERE run_id=?`, runID)
	if err != nil {
		return fmt.Errorf("mark run log incomplete: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect run log health update: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}
