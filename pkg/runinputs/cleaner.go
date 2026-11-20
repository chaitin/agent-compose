package runinputs

import (
	"context"
	"time"

	"github.com/chaitin/agent-compose/pkg/cleanup"
)

// Cleaner adapts the store to the daemon cleanup runner. Expiry is the store's
// own TTL, so the runner's cutoff is ignored.
type Cleaner struct {
	Store *Store
}

// Name identifies the cleaner in cleanup logs.
func (Cleaner) Name() string { return "run input uploads" }

// Clean removes expired uploads and stale residue.
func (c Cleaner) Clean(context.Context, time.Time) (cleanup.Result, error) {
	removed := c.Store.Collect()
	return cleanup.Result{Matched: removed, Removed: removed}, nil
}
