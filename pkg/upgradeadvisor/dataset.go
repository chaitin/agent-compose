package upgradeadvisor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

type schedulerInfo struct {
	ID          string
	ProjectID   string
	Project     string
	Agent       string
	DisplayName string
	Enabled     bool
}

type triggerInfo struct {
	SchedulerID string
	ID          string
	Kind        string
	Topic       string
	IntervalMS  int64
	Enabled     bool
	SpecJSON    string
	NextFireAt  time.Time
	LastFiredAt time.Time
}

func (t triggerInfo) source() string {
	return schedulers.SchedulerTriggerSource(t.domainTrigger())
}

func (t triggerInfo) domainTrigger() domain.SchedulerTrigger {
	return domain.SchedulerTrigger{
		SchedulerID: t.SchedulerID,
		ID:          t.ID,
		Kind:        t.Kind,
		Topic:       t.Topic,
		IntervalMs:  t.IntervalMS,
		Enabled:     t.Enabled,
		SpecJSON:    t.SpecJSON,
		NextFireAt:  t.NextFireAt,
		LastFiredAt: t.LastFiredAt,
	}
}

type runRecord struct {
	SchedulerID   string
	RunID         string
	TriggerID     string
	TriggerKind   string
	TriggerSource string
	Status        string
	Error         string
	StartedAt     time.Time
	CompletedAt   time.Time
	DurationMS    int64
}

type projectRunRecord struct {
	RunID     string
	ProjectID string
	Source    string
	Status    string
	StartedAt time.Time
}

type sandboxRow struct {
	ID        string
	VMStatus  string
	CreatedAt time.Time
}

type dataset struct {
	now              time.Time
	loc              *time.Location
	schemaVersion    int64
	schedulers       map[string]schedulerInfo
	schedulerOrder   []string
	triggers         []triggerInfo
	runs             []runRecord
	runningScheduler []runRecord
	projectRuns      []projectRunRecord
	sandboxCounts    map[string]int
	pendingSandboxes []sandboxRow
	recentFailures   []runRecord
}

func (d *dataset) scheduler(id string) (schedulerInfo, bool) {
	item, ok := d.schedulers[id]
	return item, ok
}

func loadDataset(ctx context.Context, db *sql.DB, opts Options) (*dataset, error) {
	if err := requireTables(ctx, db); err != nil {
		return nil, err
	}
	ds := &dataset{
		now:           opts.Now,
		loc:           opts.Location,
		schedulers:    map[string]schedulerInfo{},
		sandboxCounts: map[string]int{},
	}
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&ds.schemaVersion); err != nil {
		return nil, fmt.Errorf("read schema version: %w", err)
	}
	if err := loadSchedulers(ctx, db, ds); err != nil {
		return nil, err
	}
	if err := loadTriggers(ctx, db, ds); err != nil {
		return nil, err
	}
	if err := loadRuns(ctx, db, ds, opts); err != nil {
		return nil, err
	}
	if err := loadRunningSchedulerRuns(ctx, db, ds); err != nil {
		return nil, err
	}
	if err := loadProjectRuns(ctx, db, ds); err != nil {
		return nil, err
	}
	if err := loadSandboxCounts(ctx, db, ds); err != nil {
		return nil, err
	}
	if err := loadPendingSandboxes(ctx, db, ds, opts); err != nil {
		return nil, err
	}
	if err := loadRecentFailures(ctx, db, ds); err != nil {
		return nil, err
	}
	return ds, nil
}

func requireTables(ctx context.Context, db *sql.DB) error {
	required := []string{
		"schema_migrations", "project", "project_agent", "project_scheduler",
		"scheduler_trigger", "scheduler_run", "project_run", "sandboxes",
	}
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return fmt.Errorf("inspect database schema: %w", err)
	}
	defer func() { _ = rows.Close() }()
	present := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("scan database table: %w", err)
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect database schema: %w", err)
	}
	var missing []string
	for _, name := range required {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: missing tables %s", ErrNotAgentComposeDatabase, strings.Join(missing, ", "))
	}
	return nil
}

func loadSchedulers(ctx context.Context, db *sql.DB, ds *dataset) error {
	rows, err := db.QueryContext(ctx, `SELECT s.id, s.project_id, p.name, s.agent_name, s.enabled, s.spec_json
		FROM project_scheduler s
		JOIN project p ON p.id = s.project_id
		ORDER BY p.name ASC, s.agent_name ASC, s.id ASC`)
	if err != nil {
		return fmt.Errorf("list schedulers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var info schedulerInfo
		var enabled int
		var specJSON string
		if err := rows.Scan(&info.ID, &info.ProjectID, &info.Project, &info.Agent, &enabled, &specJSON); err != nil {
			return fmt.Errorf("scan scheduler: %w", err)
		}
		info.Enabled = enabled != 0
		info.DisplayName = schedulerDisplayName(specJSON)
		ds.schedulers[info.ID] = info
		ds.schedulerOrder = append(ds.schedulerOrder, info.ID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list schedulers: %w", err)
	}
	return nil
}

func schedulerDisplayName(specJSON string) string {
	var spec struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return ""
	}
	return strings.TrimSpace(spec.DisplayName)
}

func loadTriggers(ctx context.Context, db *sql.DB, ds *dataset) error {
	rows, err := db.QueryContext(ctx, `SELECT scheduler_id, trigger_id, kind, topic, interval_ms, enabled, spec_json, next_fire_at, last_fired_at
		FROM scheduler_trigger ORDER BY scheduler_id ASC, trigger_id ASC`)
	if err != nil {
		return fmt.Errorf("list scheduler triggers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var item triggerInfo
		var enabled int
		var nextFireAt, lastFiredAt int64
		if err := rows.Scan(&item.SchedulerID, &item.ID, &item.Kind, &item.Topic, &item.IntervalMS, &enabled, &item.SpecJSON, &nextFireAt, &lastFiredAt); err != nil {
			return fmt.Errorf("scan scheduler trigger: %w", err)
		}
		item.Enabled = enabled != 0
		item.NextFireAt = unixMillis(nextFireAt)
		item.LastFiredAt = unixMillis(lastFiredAt)
		ds.triggers = append(ds.triggers, item)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list scheduler triggers: %w", err)
	}
	return nil
}

func loadRuns(ctx context.Context, db *sql.DB, ds *dataset, opts Options) error {
	since := opts.Now.Add(-time.Duration(opts.Days) * 24 * time.Hour).UnixMilli()
	rows, err := db.QueryContext(ctx, `SELECT scheduler_id, run_id, trigger_id, trigger_kind, trigger_source, status,
			started_at, completed_at, duration_ms, error
		FROM scheduler_run WHERE started_at >= ? ORDER BY started_at ASC`, since)
	if err != nil {
		return fmt.Errorf("list scheduler runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return err
		}
		ds.runs = append(ds.runs, run)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list scheduler runs: %w", err)
	}
	return nil
}

func loadRunningSchedulerRuns(ctx context.Context, db *sql.DB, ds *dataset) error {
	rows, err := db.QueryContext(ctx, `SELECT scheduler_id, run_id, trigger_id, trigger_kind, trigger_source, status,
			started_at, completed_at, duration_ms, error
		FROM scheduler_run WHERE status = ? ORDER BY started_at ASC`, domain.SchedulerRunStatusRunning)
	if err != nil {
		return fmt.Errorf("list running scheduler runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return err
		}
		ds.runningScheduler = append(ds.runningScheduler, run)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list running scheduler runs: %w", err)
	}
	return nil
}

func scanRun(rows *sql.Rows) (runRecord, error) {
	var item runRecord
	var startedAt, completedAt int64
	if err := rows.Scan(&item.SchedulerID, &item.RunID, &item.TriggerID, &item.TriggerKind, &item.TriggerSource, &item.Status, &startedAt, &completedAt, &item.DurationMS, &item.Error); err != nil {
		return runRecord{}, fmt.Errorf("scan scheduler run: %w", err)
	}
	item.StartedAt = unixMillis(startedAt)
	item.CompletedAt = unixMillis(completedAt)
	return item, nil
}

func loadProjectRuns(ctx context.Context, db *sql.DB, ds *dataset) error {
	rows, err := db.QueryContext(ctx, `SELECT run_id, project_id, source, status, started_at
		FROM project_run WHERE status IN (?, ?) ORDER BY started_at ASC`, domain.ProjectRunStatusRunning, domain.ProjectRunStatusPending)
	if err != nil {
		return fmt.Errorf("list running project runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var item projectRunRecord
		var startedAt int64
		if err := rows.Scan(&item.RunID, &item.ProjectID, &item.Source, &item.Status, &startedAt); err != nil {
			return fmt.Errorf("scan project run: %w", err)
		}
		item.StartedAt = unixMillis(startedAt)
		ds.projectRuns = append(ds.projectRuns, item)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list running project runs: %w", err)
	}
	return nil
}

func loadSandboxCounts(ctx context.Context, db *sql.DB, ds *dataset) error {
	rows, err := db.QueryContext(ctx, `SELECT vm_status, COUNT(*) FROM sandboxes GROUP BY vm_status`)
	if err != nil {
		return fmt.Errorf("count sandboxes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return fmt.Errorf("scan sandbox count: %w", err)
		}
		ds.sandboxCounts[status] = count
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("count sandboxes: %w", err)
	}
	return nil
}

func loadPendingSandboxes(ctx context.Context, db *sql.DB, ds *dataset, opts Options) error {
	cutoff := opts.Now.Add(-opts.PendingSandboxAfter).UnixMilli()
	rows, err := db.QueryContext(ctx, `SELECT id, vm_status, created_at FROM sandboxes
		WHERE vm_status = ? AND created_at > 0 AND created_at < ? ORDER BY created_at ASC`,
		domain.VMStatusPending, cutoff)
	if err != nil {
		return fmt.Errorf("list pending sandboxes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var item sandboxRow
		var createdAt int64
		if err := rows.Scan(&item.ID, &item.VMStatus, &createdAt); err != nil {
			return fmt.Errorf("scan pending sandbox: %w", err)
		}
		item.CreatedAt = unixMillis(createdAt)
		ds.pendingSandboxes = append(ds.pendingSandboxes, item)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list pending sandboxes: %w", err)
	}
	return nil
}

func loadRecentFailures(ctx context.Context, db *sql.DB, ds *dataset) error {
	since := ds.now.Add(-24 * time.Hour).UnixMilli()
	rows, err := db.QueryContext(ctx, `SELECT scheduler_id, run_id, trigger_id, trigger_kind, trigger_source, status,
			started_at, completed_at, duration_ms, error
		FROM scheduler_run WHERE status = ? AND started_at >= ?
		ORDER BY started_at DESC LIMIT 20`, domain.SchedulerRunStatusFailed, since)
	if err != nil {
		return fmt.Errorf("list recent scheduler failures: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return err
		}
		ds.recentFailures = append(ds.recentFailures, run)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list recent scheduler failures: %w", err)
	}
	return nil
}
