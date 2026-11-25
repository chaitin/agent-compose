package configstore

import (
	"context"
	"encoding/json"
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

// SaveProjectRunExecutionCheckpoint keeps execution recovery independent of log
// storage after a log fault. Only the recovery field and sticky health bit change.
func (s *projectStore) SaveProjectRunExecutionCheckpoint(ctx context.Context, runID, checkpoint string) error {
	if !json.Valid([]byte(checkpoint)) {
		return fmt.Errorf("invalid run execution checkpoint")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE project_run SET log_incomplete=1, execution_checkpoint_json=? WHERE run_id=?`, checkpoint, runID)
	if err != nil {
		return fmt.Errorf("save run execution checkpoint: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect run recovery update: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}
