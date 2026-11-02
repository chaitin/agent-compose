package upgradeadvisor

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/storage/sqlite"
)

const (
	fixtureProjectID    = "prj_test"
	fixtureAgentID      = "agt_test"
	fixtureSchedulerID  = "sch_test"
	fixtureSchedulerIDB = "sch_other"
)

func openFixtureDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.db")
	database, err := sqlite.Open(path, time.Second)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database.DB()
}

func seedProject(t *testing.T, db *sql.DB, projectID, agentID, schedulerID, agent string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().Unix()
	if _, err := db.ExecContext(ctx, `INSERT INTO project(id, name, short_id, source_path, source_json, current_revision, spec_hash, created_at, updated_at)
		VALUES(?, ?, ?, ?, '{}', 1, 'hash', ?, ?)`,
		projectID, "project-"+projectID, projectID, "/tmp/"+projectID, now, now); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO project_agent(id, name, short_id, project_id, agent_name, revision, provider, model, image, driver, scheduler_enabled, spec_json, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, 1, 'codex', '', '', '', 1, '{}', ?, ?)`,
		agentID, agent, agentID, projectID, agent, now, now); err != nil {
		t.Fatalf("insert project agent: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO project_scheduler(id, short_id, project_id, scheduler_id, agent_name, revision, enabled, trigger_count, spec_json, last_error, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, 1, 1, 1, '{"display_name":"fixture scheduler"}', '', ?, ?)`,
		schedulerID, schedulerID, projectID, schedulerID, agent, now, now); err != nil {
		t.Fatalf("insert project scheduler: %v", err)
	}
}

func setSchedulerEnabled(t *testing.T, db *sql.DB, schedulerID string, enabled bool) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `UPDATE project_scheduler SET enabled = ? WHERE id = ?`, enabled, schedulerID); err != nil {
		t.Fatalf("set scheduler enabled: %v", err)
	}
}

func insertTrigger(t *testing.T, db *sql.DB, schedulerID, triggerID, kind, specJSON string, intervalMS int64, nextFireAt time.Time) {
	t.Helper()
	nextFireMillis := int64(0)
	if !nextFireAt.IsZero() {
		nextFireMillis = nextFireAt.UnixMilli()
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO scheduler_trigger(scheduler_id, trigger_id, kind, topic, interval_ms, enabled, auto_id, spec_json, next_fire_at, last_fired_at)
		VALUES(?, ?, ?, '', ?, 1, 0, ?, ?, 0)`,
		schedulerID, triggerID, kind, intervalMS, specJSON, nextFireMillis); err != nil {
		t.Fatalf("insert scheduler trigger: %v", err)
	}
}

func insertSchedulerRun(t *testing.T, db *sql.DB, schedulerID, runID, triggerID, kind, source, status string, startedAt time.Time, duration time.Duration) {
	t.Helper()
	completedAt := int64(0)
	if duration > 0 {
		completedAt = startedAt.Add(duration).UnixMilli()
	}
	if _, err := db.ExecContext(context.Background(), `INSERT INTO scheduler_run(scheduler_id, run_id, trigger_id, trigger_kind, trigger_source, status, started_at, completed_at, duration_ms, error, result_json, payload_json, source_script_sha256, artifacts_dir)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '', '', '')`,
		schedulerID, runID, triggerID, kind, source, status, startedAt.UnixMilli(), completedAt, duration.Milliseconds()); err != nil {
		t.Fatalf("insert scheduler run: %v", err)
	}
}

func insertProjectRun(t *testing.T, db *sql.DB, runID, status string, startedAt time.Time) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO project_run(run_id, project_id, project_name, project_revision, agent_name, agent_id, source, scheduler_id, trigger_id, status, sandbox_id, driver, image_ref, started_at, completed_at, duration_ms, created_at, updated_at)
		VALUES(?, ?, '', 1, 'agent', ?, 'scheduler', ?, '', ?, '', '', '', ?, 0, 0, ?, ?)`,
		runID, fixtureProjectID, fixtureAgentID, fixtureSchedulerID, status, startedAt.UnixMilli(), startedAt.Unix(), startedAt.Unix()); err != nil {
		t.Fatalf("insert project run: %v", err)
	}
}

func insertSandbox(t *testing.T, db *sql.DB, sandboxID, status string, createdAt time.Time) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO sandboxes(id, short_id, title, vm_status, driver, created_at, updated_at)
		VALUES(?, '', 'fixture', ?, 'microsandbox', ?, ?)`,
		sandboxID, status, createdAt.Unix(), createdAt.Unix()); err != nil {
		t.Fatalf("insert sandbox: %v", err)
	}
}

func cronSpec(t *testing.T, expr, timezone string) string {
	t.Helper()
	spec, err := schedulers.SchedulerCronSpecJSON(expr, timezone)
	if err != nil {
		t.Fatalf("build cron spec: %v", err)
	}
	return spec
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse fixture time %q: %v", value, err)
	}
	return parsed
}

func mustAdvisor(t *testing.T, db *sql.DB, options Options) *Advisor {
	t.Helper()
	advisor, err := New(db, options)
	if err != nil {
		t.Fatalf("create advisor: %v", err)
	}
	return advisor
}
