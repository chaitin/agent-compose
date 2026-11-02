package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/sqlite"
	"github.com/chaitin/agent-compose/pkg/upgradeadvisor"
)

func TestExecuteVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := execute(&stdout, &stderr, []string{"version"}); code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "agent-compose-ops") {
		t.Fatalf("version output = %q", stdout.String())
	}
}

func TestExecuteReportsMissingDataRoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := execute(&stdout, &stderr, []string{"upgrade", "status", "--data-root", filepath.Join(t.TempDir(), "missing")})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "data.db not found") {
		t.Fatalf("stderr = %q, want a data.db not found message", stderr.String())
	}
}

func TestExecuteStatusJSON(t *testing.T) {
	path := seedCLIFixture(t)
	var stdout, stderr bytes.Buffer
	code := execute(&stdout, &stderr, []string{"upgrade", "status", "--db", path, "--json", "--tz", "UTC"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	var report upgradeadvisor.StatusReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode status json: %v\n%s", err, stdout.String())
	}
	if len(report.RunningSchedulerRuns) != 1 || report.RunningSchedulerRuns[0].RunID != "run-live" {
		t.Fatalf("running scheduler runs = %+v", report.RunningSchedulerRuns)
	}
	if len(report.SandboxCounts) != 1 || report.SandboxCounts[0].Status != domain.VMStatusRunning {
		t.Fatalf("sandbox counts = %+v", report.SandboxCounts)
	}
}

func TestExecuteWindowsJSON(t *testing.T) {
	path := seedCLIFixture(t)
	var stdout, stderr bytes.Buffer
	code := execute(&stdout, &stderr, []string{
		"upgrade", "windows", "--db", path, "--json", "--tz", "UTC",
		"--days", "1", "--horizon", "2h", "--window", "10m", "--downtime", "2m", "--top", "2",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	var report upgradeadvisor.WindowsReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode windows json: %v\n%s", err, stdout.String())
	}
	if len(report.Candidates) == 0 {
		t.Fatalf("candidates = 0, want at least one")
	}
	if got := report.Candidates[0].End.Sub(report.Candidates[0].Start); got != 10*time.Minute {
		t.Fatalf("candidate window = %s, want 10m", got)
	}
}

func TestExecuteForecastRejectsInvalidTime(t *testing.T) {
	path := seedCLIFixture(t)
	var stdout, stderr bytes.Buffer
	code := execute(&stdout, &stderr, []string{"upgrade", "forecast", "--db", path, "--at", "not-a-time"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "parse --at") {
		t.Fatalf("stderr = %q, want a parse --at message", stderr.String())
	}
}

func seedCLIFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.db")
	database, err := sqlite.Open(path, time.Second)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	db := database.DB()
	ctx := context.Background()
	now := time.Now().UTC()
	nowUnix := now.Unix()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed query: %v", err)
		}
	}
	exec(`INSERT INTO project(id, name, short_id, source_path, source_json, current_revision, spec_hash, created_at, updated_at)
		VALUES('prj_cli', 'cli-project', 'prj_cli', '/tmp/prj_cli', '{}', 1, 'hash', ?, ?)`, nowUnix, nowUnix)
	exec(`INSERT INTO project_agent(id, name, short_id, project_id, agent_name, revision, provider, model, image, driver, scheduler_enabled, spec_json, created_at, updated_at)
		VALUES('agt_cli', 'worker', 'agt_cli', 'prj_cli', 'worker', 1, 'codex', '', '', '', 1, '{}', ?, ?)`, nowUnix, nowUnix)
	exec(`INSERT INTO project_scheduler(id, short_id, project_id, scheduler_id, agent_name, revision, enabled, trigger_count, spec_json, last_error, created_at, updated_at)
		VALUES('sch_cli', 'sch_cli', 'prj_cli', 'sch_cli', 'worker', 1, 1, 1, '{"display_name":"cli scheduler"}', '', ?, ?)`, nowUnix, nowUnix)
	exec(`INSERT INTO scheduler_trigger(scheduler_id, trigger_id, kind, topic, interval_ms, enabled, auto_id, spec_json, next_fire_at, last_fired_at)
		VALUES('sch_cli', 'hourly', 'interval', '', 3600000, 1, 0, '{}', ?, 0)`, now.Add(time.Hour).UnixMilli())
	exec(`INSERT INTO scheduler_run(scheduler_id, run_id, trigger_id, trigger_kind, trigger_source, status, started_at, completed_at, duration_ms, error, result_json, payload_json, source_script_sha256, artifacts_dir)
		VALUES('sch_cli', 'run-live', 'hourly', 'interval', 'interval:3600000', 'running', ?, 0, 0, '', '', '', '', '')`, now.Add(-5*time.Minute).UnixMilli())
	exec(`INSERT INTO sandboxes(id, short_id, title, vm_status, driver, created_at, updated_at)
		VALUES('sbx_cli', '', 'cli', 'RUNNING', 'microsandbox', ?, ?)`, nowUnix, nowUnix)
	return path
}
