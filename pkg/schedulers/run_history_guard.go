package schedulers

import (
	"context"

	"github.com/chaitin/agent-compose/pkg/apierror"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// WithIdleScheduler excludes new entries and the interval between terminal
// persistence and the last delivery/artifact write of an existing execution.
func (c *Controller) WithIdleScheduler(ctx context.Context, schedulerID string, action func(context.Context) error) error {
	c.mu.Lock()
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return apierror.NotApplied(err)
	}
	if c.running[schedulerID] > 0 || c.historyRemoving[schedulerID] {
		c.mu.Unlock()
		return apierror.NotApplied(domain.ClassifyError(domain.ErrFailedPrecondition, "scheduler still has active execution, completion work, or history removal", nil))
	}
	if c.historyRemoving == nil {
		c.historyRemoving = make(map[string]bool)
	}
	c.historyRemoving[schedulerID] = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.historyRemoving, schedulerID)
		c.mu.Unlock()
	}()
	// Filesystem and SQLite work must not hold the controller-wide mutex:
	// unrelated schedulers can still enter, leave, and refresh their state.
	return action(ctx)
}
