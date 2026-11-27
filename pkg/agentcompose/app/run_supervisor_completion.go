package app

import (
	"context"
	"fmt"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
)

// checkInactiveRunCompletion distinguishes an execution that handed off its
// result from a nonterminal run that has lost both execution and completion
// ownership. Stop must not invent a cancellation result for either case.
func (s *RunSupervisor) checkInactiveRunCompletion(ctx context.Context, runID string) error {
	if s.completions != nil {
		pending, err := s.completions.Pending(ctx, runID)
		if err != nil {
			return fmt.Errorf("check completion ownership for run %s: %w", runID, err)
		}
		if pending {
			return nil
		}
	}
	if s.store == nil {
		return nil
	}
	run, err := s.store.GetProjectRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("load inactive run %s: %w", runID, err)
	}
	if runs.StatusIsTerminal(run.Status) {
		return nil
	}
	// Registration may have raced the database checks. A subsequent stop can
	// reach that executor; it is not an orphan that needs recovery.
	s.mu.Lock()
	_, active := s.active[runID]
	s.mu.Unlock()
	if active {
		return nil
	}
	return domain.ClassifyError(domain.ErrFailedPrecondition,
		fmt.Sprintf("run %s is %s but has no active execution or pending completion; recovery is required", runID, run.Status), nil)
}
