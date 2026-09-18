package sandboxstore

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

type sandboxIndexObservation struct {
	db            *sql.DB
	before        sql.DBStats
	started       time.Time
	gateWait      time.Duration
	metadataRead  time.Duration
	projectLookup time.Duration
	write         time.Duration
}

func newSandboxIndexObservation(db *sql.DB) *sandboxIndexObservation {
	return &sandboxIndexObservation{db: db, before: db.Stats(), started: time.Now()}
}

func (o *sandboxIndexObservation) finish(ctx context.Context, id string, operation sandboxIndexOperation, err error) {
	if err == nil {
		return
	}
	after := o.db.Stats()
	// Pool deltas include concurrent users during this attempt; they are not
	// presented as the duration of this individual SQL query.
	slog.WarnContext(ctx, "sandbox listing cache synchronization failed",
		"sandbox_id", id, "operation", operation, "error", err,
		"elapsed", time.Since(o.started), "gate_wait", o.gateWait,
		"metadata_read", o.metadataRead, "project_lookup", o.projectLookup, "write", o.write,
		"pool_max_open", after.MaxOpenConnections, "pool_in_use", after.InUse, "pool_idle", after.Idle,
		"pool_wait_count_delta", after.WaitCount-o.before.WaitCount,
		"pool_wait_duration_delta", after.WaitDuration-o.before.WaitDuration)
}
