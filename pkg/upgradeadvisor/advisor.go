// Package upgradeadvisor analyzes a live agent-compose data directory and
// recommends windows for restarting the daemon without interrupting running
// scheduler work.
//
// The package is deliberately read-only. It never opens the database for
// writing, never runs migrations, and never talks to the daemon process; it
// only queries the SQLite snapshot the daemon already persists so an operator
// can inspect activity while the daemon keeps running.
package upgradeadvisor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

// ErrNotAgentComposeDatabase reports that a database does not contain the
// tables an agent-compose data directory must have.
var ErrNotAgentComposeDatabase = errors.New("not an agent-compose database")

const (
	// DefaultDays is how much run history statistics look at.
	DefaultDays = 14
	// DefaultBin is the activity histogram resolution.
	DefaultBin = 5 * time.Minute
	// DefaultHorizon is how far into the future windows are recommended.
	DefaultHorizon = 48 * time.Hour
	// DefaultWindow is the minimum candidate upgrade window.
	DefaultWindow = 5 * time.Minute
	// DefaultDowntime is the estimated daemon restart cost.
	DefaultDowntime = 90 * time.Second
	// DefaultTop is how many candidate windows are returned.
	DefaultTop = 5
	// DefaultLimit is how many upcoming fires are returned.
	DefaultLimit = 50
	// DefaultStuckAfter is when a running scheduler run is flagged as long.
	DefaultStuckAfter = time.Hour
	// DefaultPendingSandboxAfter is when a creating sandbox is flagged as stuck.
	DefaultPendingSandboxAfter = 30 * time.Minute
	// maxFiresPerTrigger bounds one trigger's forecast loop so a one-second
	// interval cannot exhaust memory. It is intentionally far above a realistic
	// horizon (48h at 1s is 172,800 fires).
	maxFiresPerTrigger = 500_000
	// maxFiresTotal bounds the merged forecast across all triggers.
	maxFiresTotal = 200_000
)

// Options controls one analysis run. Zero values select the documented
// defaults; Now and Location are injectable so tests are deterministic.
type Options struct {
	Days                int
	Bin                 time.Duration
	Horizon             time.Duration
	Window              time.Duration
	Downtime            time.Duration
	Top                 int
	Limit               int
	AssumePause         bool
	StuckAfter          time.Duration
	PendingSandboxAfter time.Duration
	Now                 time.Time
	Location            *time.Location
}

func (o Options) normalized() (Options, error) {
	if o.Days <= 0 {
		o.Days = DefaultDays
	}
	if o.Bin <= 0 {
		o.Bin = DefaultBin
	}
	if o.Bin < time.Minute {
		o.Bin = time.Minute
	}
	if o.Bin%time.Minute != 0 || (24*60)%int(o.Bin/time.Minute) != 0 {
		return Options{}, fmt.Errorf("--bin %s must be a whole number of minutes that divides a day", o.Bin)
	}
	if o.Horizon <= 0 {
		o.Horizon = DefaultHorizon
	}
	if o.Window <= 0 {
		o.Window = DefaultWindow
	}
	if o.Downtime < 0 {
		o.Downtime = 0
	}
	if o.Downtime == 0 {
		o.Downtime = DefaultDowntime
	}
	if o.Top <= 0 {
		o.Top = DefaultTop
	}
	if o.Limit <= 0 {
		o.Limit = DefaultLimit
	}
	if o.StuckAfter <= 0 {
		o.StuckAfter = DefaultStuckAfter
	}
	if o.PendingSandboxAfter <= 0 {
		o.PendingSandboxAfter = DefaultPendingSandboxAfter
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	o.Now = o.Now.UTC()
	if o.Location == nil {
		o.Location = time.Local
	}
	return o, nil
}

// Advisor answers questions about a daemon's persisted scheduler state.
type Advisor struct {
	db   *sql.DB
	opts Options
}

// New creates an Advisor bound to a read-only database handle. The caller owns
// db and must keep it open for the lifetime of the Advisor.
func New(db *sql.DB, options Options) (*Advisor, error) {
	if db == nil {
		return nil, fmt.Errorf("upgrade advisor database is required")
	}
	options, err := options.normalized()
	if err != nil {
		return nil, err
	}
	return &Advisor{db: db, opts: options}, nil
}

// percentile returns the nearest-rank percentile for an ascending slice.
func percentileMillis(sorted []int64, fraction float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if fraction <= 0 {
		return sorted[0]
	}
	if fraction >= 1 {
		return sorted[len(sorted)-1]
	}
	index := int(float64(len(sorted))*fraction+0.999999) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func sortedMillis(values []int64) []int64 {
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted
}

func unixMillis(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}

// runDurationMillis derives a duration from a run row, preferring the recorded
// duration and falling back to completed-started for rows written before the
// duration column was filled.
func runDurationMillis(startedAt, completedAt time.Time, durationMS int64) int64 {
	if durationMS > 0 {
		return durationMS
	}
	if startedAt.IsZero() || completedAt.IsZero() || completedAt.Before(startedAt) {
		return 0
	}
	return completedAt.Sub(startedAt).Milliseconds()
}

func (a *Advisor) load(ctx context.Context) (*dataset, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return loadDataset(ctx, a.db, a.opts)
}
