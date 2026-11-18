package schedulers

import (
	"context"
	"strings"

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
	if c.running[schedulerID] > 0 || c.skipRecording[schedulerID] > 0 || c.historyRemoving[schedulerID] {
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

// HistoryRemoving reports whether exact history removal currently holds the
// scheduler, so refused entries are not recorded as concurrency skips.
func (c *Controller) HistoryRemoving(schedulerID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.historyRemoving[strings.TrimSpace(schedulerID)]
}

// BeginSkipRecord registers the recording of a skipped run, which writes rows
// and artifacts outside the running count. Removal refuses while it is held.
func (c *Controller) BeginSkipRecord(schedulerID string) (func(), bool) {
	schedulerID = strings.TrimSpace(schedulerID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.historyRemoving[schedulerID] {
		return nil, false
	}
	if c.skipRecording == nil {
		c.skipRecording = make(map[string]int)
	}
	c.skipRecording[schedulerID]++
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.skipRecording[schedulerID] <= 1 {
			delete(c.skipRecording, schedulerID)
			return
		}
		c.skipRecording[schedulerID]--
	}, true
}
