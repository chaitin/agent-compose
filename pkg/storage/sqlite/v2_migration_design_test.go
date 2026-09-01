package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestV2MigrationDesignPreservesManagedHistory(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	chain := loadV2MigrationDesignChain(t)
	if err := applyMigrationSet(ctx, db, chain[:4]); err != nil {
		t.Fatalf("apply v4 prefix: %v", err)
	}
	seedManagedV4Fixture(t, db)

	if err := applyMigrationSet(ctx, db, chain); err != nil {
		t.Fatalf("apply v2 migration design: %v", err)
	}
	if err := applyMigrationSet(ctx, db, chain); err != nil {
		t.Fatalf("reapply v2 migration design: %v", err)
	}

	assertSQLiteTablesAbsent(t, db,
		"agent_definition", "loader", "loader_trigger", "loader_run",
		"loader_event", "loader_state", "loader_binding", "event_session_link",
	)
	assertSQLiteTablesPresent(t, db,
		"project_agent", "project_scheduler", "project_run", "project_run_event",
		"scheduler_trigger", "scheduler_run", "scheduler_event", "scheduler_state",
		"scheduler_sandbox_binding", "event_delivery", "event_sandbox_link",
	)

	var agentID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM project_agent WHERE project_id = 'project-1' AND agent_name = 'worker'`).Scan(&agentID); err != nil {
		t.Fatalf("query migrated project agent: %v", err)
	}
	if agentID != "agent-1" {
		t.Fatalf("migrated agent id = %q, want agent-1", agentID)
	}
	var runAgentID string
	var schedulerRunID sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT agent_id, scheduler_run_id FROM project_run WHERE run_id = 'project-run-1'`).Scan(&runAgentID, &schedulerRunID); err != nil {
		t.Fatalf("query migrated project run: %v", err)
	}
	if runAgentID != "agent-1" || schedulerRunID.Valid {
		t.Fatalf("migrated project run link = (%q, %#v), want agent-1 and NULL", runAgentID, schedulerRunID)
	}

	var lastError string
	if err := db.QueryRowContext(ctx, `SELECT last_error FROM project_scheduler WHERE id = 'scheduler-1'`).Scan(&lastError); err != nil {
		t.Fatalf("query migrated scheduler: %v", err)
	}
	if lastError != "previous failure" {
		t.Fatalf("migrated scheduler last_error = %q", lastError)
	}
	for table := range map[string]struct{}{
		"scheduler_trigger": {}, "scheduler_run": {}, "scheduler_event": {},
		"scheduler_state": {}, "scheduler_sandbox_binding": {},
	} {
		var schedulerID string
		if err := db.QueryRowContext(ctx, `SELECT scheduler_id FROM `+table+` LIMIT 1`).Scan(&schedulerID); err != nil {
			t.Fatalf("query %s identity: %v", table, err)
		}
		if schedulerID != "scheduler-1" {
			t.Fatalf("%s scheduler_id = %q, want scheduler-1", table, schedulerID)
		}
	}

	var topic, source, payload string
	if err := db.QueryRowContext(ctx, `SELECT topic, source, payload_json FROM event WHERE id = 'topic-event-1'`).Scan(&topic, &source, &payload); err != nil {
		t.Fatalf("query historical event: %v", err)
	}
	if topic != "loader.completed" || source != "loader" || payload != `{"type":"loader.completed"}` {
		t.Fatalf("historical event changed: topic=%q source=%q payload=%q", topic, source, payload)
	}
	var deliveryScheduler, deliveryRun string
	if err := db.QueryRowContext(ctx, `SELECT scheduler_id, scheduler_run_id FROM event_delivery WHERE event_id = 'topic-event-1'`).Scan(&deliveryScheduler, &deliveryRun); err != nil {
		t.Fatalf("query migrated event delivery: %v", err)
	}
	if deliveryScheduler != "scheduler-1" || deliveryRun != "scheduler-run-1" {
		t.Fatalf("delivery scheduler link = (%q, %q)", deliveryScheduler, deliveryRun)
	}
	var linkCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_sandbox_link WHERE event_id = 'topic-event-1' AND scheduler_id = 'scheduler-1'`).Scan(&linkCount); err != nil {
		t.Fatalf("count migrated event sandbox links: %v", err)
	}
	if linkCount != 2 {
		t.Fatalf("migrated event sandbox link count = %d, want 2", linkCount)
	}

	assertNoForeignKeyViolations(t, db)
	var historyCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&historyCount); err != nil {
		t.Fatalf("count v2 migration history: %v", err)
	}
	if historyCount != len(chain) {
		t.Fatalf("v2 migration history count = %d, want %d", historyCount, len(chain))
	}
}

func TestV2MigrationDesignRejectsStandaloneAgentWithoutModification(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	chain := loadV2MigrationDesignChain(t)
	if err := applyMigrationSet(ctx, db, chain[:4]); err != nil {
		t.Fatalf("apply v4 prefix: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO agent_definition(id, name, created_at, updated_at) VALUES('standalone-agent', 'standalone', 1, 1)`); err != nil {
		t.Fatalf("seed standalone agent: %v", err)
	}

	err := applyMigrationSet(ctx, db, chain)
	if err == nil || !strings.Contains(err.Error(), "agent-compose-migrate") {
		t.Fatalf("migration error = %v, want storage migrator hint", err)
	}
	assertV4RollbackState(t, db, "agent_definition", "standalone-agent")
}

func TestV2MigrationDesignRejectsStandaloneSchedulerAndRollsBackAgentMigration(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	chain := loadV2MigrationDesignChain(t)
	if err := applyMigrationSet(ctx, db, chain[:4]); err != nil {
		t.Fatalf("apply v4 prefix: %v", err)
	}
	seedManagedV4Fixture(t, db)
	if _, err := db.ExecContext(ctx, `INSERT INTO loader(id, name, script) VALUES('standalone-loader', 'standalone', 'return 1')`); err != nil {
		t.Fatalf("seed standalone scheduler: %v", err)
	}

	err := applyMigrationSet(ctx, db, chain)
	if err == nil || !strings.Contains(err.Error(), "agent-compose-migrate") {
		t.Fatalf("migration error = %v, want storage migrator hint", err)
	}
	assertV4RollbackState(t, db, "loader", "standalone-loader")
}

func TestV2MigrationDesignRejectsAgentProjectionRevisionMismatchWithoutModification(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	chain := loadV2MigrationDesignChain(t)
	if err := applyMigrationSet(ctx, db, chain[:4]); err != nil {
		t.Fatalf("apply v4 prefix: %v", err)
	}
	seedManagedV4Fixture(t, db)
	if _, err := db.ExecContext(ctx, `UPDATE agent_definition SET managed_project_revision = 2 WHERE id = 'agent-1'`); err != nil {
		t.Fatalf("seed projection revision mismatch: %v", err)
	}

	err := applyMigrationSet(ctx, db, chain)
	if err == nil || !strings.Contains(err.Error(), "agent-compose-migrate") {
		t.Fatalf("migration error = %v, want storage migrator hint", err)
	}
	assertV4RollbackState(t, db, "agent_definition", "agent-1")
	var revision int64
	if err := db.QueryRow(`SELECT managed_project_revision FROM agent_definition WHERE id = 'agent-1'`).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("projection revision after rollback = %d, err=%v", revision, err)
	}
}

func TestV2MigrationDesignRejectsLegacySchedulerArtifactPathWithoutModification(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	chain := loadV2MigrationDesignChain(t)
	if err := applyMigrationSet(ctx, db, chain[:4]); err != nil {
		t.Fatalf("apply v4 prefix: %v", err)
	}
	seedManagedV4Fixture(t, db)
	legacyPath := `/data/loaders/loader-1/runs/scheduler-run-1`
	if _, err := db.ExecContext(ctx, `UPDATE loader_run SET artifacts_dir = ? WHERE run_id = 'scheduler-run-1'`, legacyPath); err != nil {
		t.Fatalf("seed legacy scheduler artifact path: %v", err)
	}

	err := applyMigrationSet(ctx, db, chain)
	if err == nil || !strings.Contains(err.Error(), "agent-compose-migrate") {
		t.Fatalf("migration error = %v, want storage migrator hint", err)
	}
	assertV4RollbackState(t, db, "loader", "loader-1")
	var artifactsDir string
	if err := db.QueryRow(`SELECT artifacts_dir FROM loader_run WHERE run_id = 'scheduler-run-1'`).Scan(&artifactsDir); err != nil || artifactsDir != legacyPath {
		t.Fatalf("artifact path after rollback = %q, err=%v", artifactsDir, err)
	}
}

func TestV2MigrationDesignBuildsCleanDatabaseThroughHistoricalChain(t *testing.T) {
	db := newMemoryDB(t)
	if err := applyMigrationSet(context.Background(), db, loadV2MigrationDesignChain(t)); err != nil {
		t.Fatalf("apply clean v2 migration chain: %v", err)
	}
	assertSQLiteTablesAbsent(t, db, "agent_definition", "loader", "loader_run")
	assertSQLiteTablesPresent(t, db, "project_agent", "project_scheduler", "scheduler_run")
	assertNoForeignKeyViolations(t, db)
	if _, err := db.Exec(`INSERT INTO project(id,name,created_at,updated_at) VALUES('identity-project','identity',1,1)`); err != nil {
		t.Fatalf("seed identity constraint project: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO project_agent(id,project_id,agent_name,created_at,updated_at) VALUES('','identity-project','empty',1,1)`); err == nil {
		t.Fatal("final project_agent schema accepted an empty native id")
	}
	if _, err := db.Exec(`INSERT INTO project_agent(id,project_id,agent_name,created_at,updated_at) VALUES('agent-valid','identity-project','worker',1,1)`); err != nil {
		t.Fatalf("seed identity constraint agent: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO project_scheduler(id,project_id,scheduler_id,agent_name,created_at,updated_at) VALUES('','identity-project','scheduler','worker',1,1)`); err == nil {
		t.Fatal("final project_scheduler schema accepted an empty native id")
	}
}

func TestV2MigrationDesignUpgradesEveryHistoricalPrefix(t *testing.T) {
	chain := loadV2MigrationDesignChain(t)
	for prefix := 1; prefix <= 4; prefix++ {
		t.Run(chain[prefix-1].name, func(t *testing.T) {
			db := newMemoryDB(t)
			if err := applyMigrationSet(context.Background(), db, chain[:prefix]); err != nil {
				t.Fatalf("apply historical prefix: %v", err)
			}
			if err := applyMigrationSet(context.Background(), db, chain); err != nil {
				t.Fatalf("upgrade historical prefix: %v", err)
			}
			assertSQLiteTablesPresent(t, db, "project_agent", "project_scheduler", "scheduler_run")
			assertSQLiteTablesAbsent(t, db, "agent_definition", "loader_run")
		})
	}
}

func TestV2MigrationDesignAppliesEachNewVersionIndependently(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	chain := loadV2MigrationDesignChain(t)
	if err := applyMigrationSet(ctx, db, chain[:4]); err != nil {
		t.Fatalf("apply v4 prefix: %v", err)
	}
	seedManagedV4Fixture(t, db)

	if err := applyMigrationSet(ctx, db, chain[:5]); err != nil {
		t.Fatalf("apply migration 5: %v", err)
	}
	assertSQLiteTablesPresent(t, db, "project_agent", "loader")
	assertSQLiteTablesAbsent(t, db, "agent_definition")
	var agentID string
	if err := db.QueryRow(`SELECT agent_id FROM project_run WHERE run_id = 'project-run-1'`).Scan(&agentID); err != nil || agentID != "agent-1" {
		t.Fatalf("migration 5 agent link = %q, err=%v", agentID, err)
	}

	if err := applyMigrationSet(ctx, db, chain[:6]); err != nil {
		t.Fatalf("apply migration 6: %v", err)
	}
	assertSQLiteTablesPresent(t, db, "scheduler_trigger", "scheduler_run", "event_session_link")
	assertSQLiteTablesAbsent(t, db, "loader", "loader_run")

	if err := applyMigrationSet(ctx, db, chain[:7]); err != nil {
		t.Fatalf("apply migration 7: %v", err)
	}
	assertSQLiteTablesPresent(t, db, "event_delivery", "event_sandbox_link")
	assertSQLiteTablesAbsent(t, db, "event_session_link")
	for migrationCount := 8; migrationCount <= len(chain); migrationCount++ {
		if err := applyMigrationSet(ctx, db, chain[:migrationCount]); err != nil {
			t.Fatalf("apply migration %d: %v", migrationCount, err)
		}
	}
	assertNoForeignKeyViolations(t, db)
}

func TestProjectRunCompletionMigrationPreservesV9Runs(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	chain := loadV2MigrationDesignChain(t)
	if err := applyMigrationSet(ctx, db, chain[:9]); err != nil {
		t.Fatalf("apply v9 prefix: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO project(id,name) VALUES('project-1','project')`,
		`INSERT INTO project_agent(id,project_id,agent_name,created_at,updated_at) VALUES('agent-1','project-1','worker',1,1)`,
		`INSERT INTO project_run(run_id,project_id,agent_id,status,sandbox_id,result_json,created_at,updated_at) VALUES('run-1','project-1','agent-1','running','sandbox-1','{}',1,1)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed v9 run with %q: %v", statement, err)
		}
	}
	if err := applyMigrationSet(ctx, db, chain[:10]); err != nil {
		t.Fatalf("apply v10 migration: %v", err)
	}
	var policy string
	var created int
	if err := db.QueryRowContext(ctx, `SELECT cleanup_policy, sandbox_created FROM project_run WHERE run_id='run-1'`).Scan(&policy, &created); err != nil {
		t.Fatal(err)
	}
	if policy != "stop_on_completion" || created != 0 {
		t.Fatalf("migrated run policy/created = %q/%d", policy, created)
	}
	assertSQLiteTablesPresent(t, db, "project_run_completion")
}

func TestV2MigrationDesignDeduplicatesEquivalentLegacyEventLinks(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	chain := loadV2MigrationDesignChain(t)
	if err := applyMigrationSet(ctx, db, chain[:4]); err != nil {
		t.Fatalf("apply v4 prefix: %v", err)
	}
	seedManagedV4Fixture(t, db)
	if _, err := db.ExecContext(ctx, `INSERT INTO event_session_link(
		event_id, session_id, relation, loader_id, run_id, trigger_id, loader_event_id, created_at
	) SELECT event_id, sandbox_id, relation, loader_id, run_id, trigger_id, loader_event_id, created_at
	  FROM event_sandbox_link WHERE event_id = 'topic-event-1' AND sandbox_id = 'sandbox-1'`); err != nil {
		t.Fatalf("seed equivalent legacy event link: %v", err)
	}

	if err := applyMigrationSet(ctx, db, chain); err != nil {
		t.Fatalf("migrate equivalent legacy event links: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_sandbox_link
		WHERE event_id='topic-event-1' AND sandbox_id='sandbox-1' AND relation='used' AND scheduler_run_id='scheduler-run-1'`).Scan(&count); err != nil {
		t.Fatalf("count deduplicated event link: %v", err)
	}
	if count != 1 {
		t.Fatalf("deduplicated event link count = %d, want 1", count)
	}
}

func TestV2MigrationDesignRejectsConflictingLegacyEventLinks(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	chain := loadV2MigrationDesignChain(t)
	if err := applyMigrationSet(ctx, db, chain[:4]); err != nil {
		t.Fatalf("apply v4 prefix: %v", err)
	}
	seedManagedV4Fixture(t, db)
	if _, err := db.ExecContext(ctx, `INSERT INTO event_session_link(
		event_id, session_id, relation, loader_id, run_id, trigger_id, loader_event_id, created_at
	) SELECT event_id, sandbox_id, relation, loader_id, run_id, 'conflicting-trigger', loader_event_id, created_at
	  FROM event_sandbox_link WHERE event_id = 'topic-event-1' AND sandbox_id = 'sandbox-1'`); err != nil {
		t.Fatalf("seed conflicting legacy event link: %v", err)
	}
	if err := applyMigrationSet(ctx, db, chain[:6]); err != nil {
		t.Fatalf("apply v6 prefix: %v", err)
	}

	err := applyMigrationSet(ctx, db, chain)
	if err == nil || !strings.Contains(err.Error(), "conflicting sandbox and session records") {
		t.Fatalf("migration error = %v, want conflicting event link guard", err)
	}
	var historyCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&historyCount); err != nil {
		t.Fatalf("count migration history after rollback: %v", err)
	}
	if historyCount != 6 {
		t.Fatalf("migration history count after rollback = %d, want 6", historyCount)
	}
	assertSQLiteTablesPresent(t, db, "event_sandbox_link", "event_session_link")
	var sandboxTrigger, sessionTrigger string
	if err := db.QueryRowContext(ctx, `SELECT trigger_id FROM event_sandbox_link
		WHERE event_id='topic-event-1' AND sandbox_id='sandbox-1' AND relation='used' AND run_id='scheduler-run-1'`).Scan(&sandboxTrigger); err != nil {
		t.Fatalf("read sandbox link after conflict rollback: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT trigger_id FROM event_session_link
		WHERE event_id='topic-event-1' AND session_id='sandbox-1' AND relation='used' AND run_id='scheduler-run-1'`).Scan(&sessionTrigger); err != nil {
		t.Fatalf("read session link after conflict rollback: %v", err)
	}
	if sandboxTrigger != "trigger-1" || sessionTrigger != "conflicting-trigger" {
		t.Fatalf("legacy links after conflict rollback = sandbox %q/session %q, want both source payloads preserved", sandboxTrigger, sessionTrigger)
	}
}

func TestHistoricalMigrationChecksumsRemainImmutable(t *testing.T) {
	want := []string{
		"6d2a07e2df01c38a57989accc3eb265cc3238ae3322f5dd540383235e59e27a9",
		"fa328b0bd1be3620d4a92b94bd39d6cb4a3a6d454ce1de643e82598f9c028a49",
		"5bdbd3258245ce7fc025121625408b35b444a425ffcb89aed0b8b7a846969183",
		"3d5c2ab028a6f7e1c461f0af3fc6d898807b60159f1625b8939987d6ce7b91cb",
	}
	chain := loadV2MigrationDesignChain(t)
	for index, checksum := range want {
		if chain[index].checksum != checksum {
			t.Fatalf("historical migration %d checksum = %s, want %s", index+1, chain[index].checksum, checksum)
		}
	}
}

func TestCronLocalTimezoneMigrationOnlyUpdatesDeclarativeSchedulers(t *testing.T) {
	for _, tt := range []struct {
		name          string
		schedulerSpec string
		wantTimezone  string
		wantNextFire  int64
	}{
		{name: "declarative", schedulerSpec: `{"enabled":true,"triggers":[{"kind":"cron","cron":"0 9 * * *"}]}`, wantTimezone: "Local", wantNextFire: 0},
		{name: "script", schedulerSpec: `{"enabled":true,"script":"scheduler.cron('0 9 * * *', run, { timezone: 'UTC' })"}`, wantTimezone: "UTC", wantNextFire: 2000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			db := newMemoryDB(t)
			chain := loadV2MigrationDesignChain(t)
			if err := applyMigrationSet(ctx, db, chain[:4]); err != nil {
				t.Fatalf("apply v4 prefix: %v", err)
			}
			seedManagedV4Fixture(t, db)
			if err := applyMigrationSet(ctx, db, chain[:7]); err != nil {
				t.Fatalf("apply v7 prefix: %v", err)
			}
			if _, err := db.ExecContext(ctx, `UPDATE project_scheduler SET spec_json = ? WHERE id = 'scheduler-1'`, tt.schedulerSpec); err != nil {
				t.Fatalf("set scheduler spec: %v", err)
			}
			if _, err := db.ExecContext(ctx, `UPDATE scheduler_trigger SET spec_json = '{"kind":"cron","expr":"0 9 * * *","timezone":"UTC"}', next_fire_at = 2000 WHERE scheduler_id = 'scheduler-1'`); err != nil {
				t.Fatalf("set cron trigger: %v", err)
			}
			if err := applyMigrationSet(ctx, db, chain[:8]); err != nil {
				t.Fatalf("apply local timezone migration: %v", err)
			}
			var specJSON string
			var nextFire int64
			if err := db.QueryRowContext(ctx, `SELECT spec_json, next_fire_at FROM scheduler_trigger WHERE scheduler_id = 'scheduler-1'`).Scan(&specJSON, &nextFire); err != nil {
				t.Fatalf("read migrated cron trigger: %v", err)
			}
			if !strings.Contains(specJSON, `"timezone":"`+tt.wantTimezone+`"`) || nextFire != tt.wantNextFire {
				t.Fatalf("migrated cron trigger = %s/%d, want timezone %s and next fire %d", specJSON, nextFire, tt.wantTimezone, tt.wantNextFire)
			}
		})
	}
}

func loadV2MigrationDesignChain(t *testing.T) []migration {
	t.Helper()
	chain, err := loadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if len(chain) != 14 {
		t.Fatalf("migration count = %d, want 14", len(chain))
	}
	return chain
}

func seedManagedV4Fixture(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	statements := []string{
		`INSERT INTO project(id, name, short_id, current_revision, spec_hash) VALUES('project-1', 'legacy', 'project', 1, 'hash-1')`,
		`INSERT INTO project_revision(project_id, revision, spec_hash, spec_json, created_at) VALUES('project-1', 1, 'hash-1', '{"name":"legacy","agents":[{"name":"worker","provider":"codex","image":"guest:latest","driver":{"name":"docker"},"enabled":true,"scheduler":{"enabled":true,"sandbox_policy":"sticky","concurrency_policy":"skip","script":"run()"}}]}', 1000)`,
		`INSERT INTO agent_definition(id, name, provider, guest_image, managed_project_id, managed_project_revision, managed_agent_name, created_at, updated_at) VALUES('agent-1', 'worker', 'codex', 'guest:latest', 'project-1', 1, 'worker', 1000, 1001)`,
		`INSERT INTO project_agent(id, name, short_id, project_id, agent_name, managed_agent_id, revision, provider, image, driver, scheduler_enabled, spec_json, created_at, updated_at) VALUES('agent-1', 'worker', 'agent', 'project-1', 'worker', 'agent-1', 1, 'codex', 'guest:latest', 'docker', 1, '{"name":"worker"}', 1000, 1001)`,
		`INSERT INTO loader(id, name, script, agent_id, managed_project_id, managed_project_revision, managed_agent_name, managed_scheduler_id, last_error, created_at, updated_at) VALUES('loader-1', 'worker scheduler', 'run()', 'agent-1', 'project-1', 1, 'worker', 'scheduler-1', 'previous failure', 1000, 1001)`,
		`INSERT INTO project_scheduler(id, short_id, project_id, scheduler_id, agent_name, managed_loader_id, revision, enabled, trigger_count, spec_json, created_at, updated_at) VALUES('scheduler-1', 'scheduler', 'project-1', 'scheduler-1', 'worker', 'loader-1', 1, 1, 1, '{"enabled":true,"script":"run()"}', 1000, 1001)`,
		`INSERT INTO project_run(run_id, project_id, project_name, project_revision, agent_name, managed_agent_id, source, scheduler_id, trigger_id, status, sandbox_id, result_json, created_at, updated_at) VALUES('project-run-1', 'project-1', 'legacy', 1, 'worker', 'agent-1', 'scheduler', 'scheduler-1', 'trigger-1', 'succeeded', 'sandbox-1', '{"ok":true}', 1100, 1200)`,
		`INSERT INTO project_run_event(id, run_id, seq, kind, text, success, created_at) VALUES('project-run-event-1', 'project-run-1', 1, 'agent_message', 'done', 1, 1200)`,
		`INSERT INTO loader_trigger(loader_id, trigger_id, kind, enabled, spec_json, next_fire_at, last_fired_at) VALUES('loader-1', 'trigger-1', 'cron', 1, '{"expr":"0 * * * *"}', 2000, 1900)`,
		`INSERT INTO loader_run(loader_id, run_id, trigger_id, trigger_kind, trigger_source, status, started_at, completed_at, duration_ms, result_json, payload_json, source_script_sha256, artifacts_dir) VALUES('loader-1', 'scheduler-run-1', 'trigger-1', 'cron', 'schedule', 'succeeded', 2000, 2100, 100, '{"ok":true}', '{"source":"timer"}', 'sha256', '')`,
		`INSERT INTO loader_event(loader_id, event_id, run_id, trigger_id, type, level, message, payload_json, linked_sandbox_id, linked_cell_id, linked_agent_thread_id, created_at) VALUES('loader-1', 'scheduler-event-1', 'scheduler-run-1', 'trigger-1', 'loader.completed', 'info', 'done', '{"type":"loader.completed"}', 'sandbox-1', 'cell-1', 'thread-1', 2100)`,
		`INSERT INTO loader_state(loader_id, key, value_json, updated_at) VALUES('loader-1', 'cursor', '{"offset":7}', 2100)`,
		`INSERT INTO loader_binding(loader_id, trigger_id, sandbox_id, sandbox_config_hash, created_at, updated_at) VALUES('loader-1', 'trigger-1', 'sandbox-1', 'config-hash', 2000, 2100)`,
		`INSERT INTO event(id, topic, source, correlation_id, payload_hash, payload_json, dispatch_status, created_at) VALUES('topic-event-1', 'loader.completed', 'loader', 'correlation-1', 'payload-hash', '{"type":"loader.completed"}', 'published_to_bus', 2100)`,
		`INSERT INTO event_delivery(event_id, loader_id, trigger_id, run_id, status, created_at, updated_at) VALUES('topic-event-1', 'loader-1', 'trigger-1', 'scheduler-run-1', 'run_succeeded', 2100, 2200)`,
		`INSERT INTO event_sandbox_link(event_id, sandbox_id, relation, loader_id, run_id, trigger_id, loader_event_id, created_at) VALUES('topic-event-1', 'sandbox-1', 'used', 'loader-1', 'scheduler-run-1', 'trigger-1', 'scheduler-event-1', 2100)`,
		`CREATE TABLE event_session_link(event_id TEXT NOT NULL, session_id TEXT NOT NULL, relation TEXT NOT NULL, loader_id TEXT NOT NULL DEFAULT '', run_id TEXT NOT NULL DEFAULT '', trigger_id TEXT NOT NULL DEFAULT '', loader_event_id TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, PRIMARY KEY(event_id, session_id, relation, run_id))`,
		`INSERT INTO event_session_link(event_id, session_id, relation, loader_id, run_id, trigger_id, loader_event_id, created_at) VALUES('topic-event-1', 'sandbox-legacy', 'created', 'loader-1', 'scheduler-run-1', 'trigger-1', 'scheduler-event-1', 2050)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed managed v4 fixture with %q: %v", statement, err)
		}
	}
}

func assertV4RollbackState(t *testing.T, db *sql.DB, table, id string) {
	t.Helper()
	var historyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&historyCount); err != nil {
		t.Fatalf("count rollback migration history: %v", err)
	}
	if historyCount != 4 {
		t.Fatalf("rollback migration history count = %d, want 4", historyCount)
	}
	var rowCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id = ?`, id).Scan(&rowCount); err != nil {
		t.Fatalf("query rollback fixture row: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("rollback fixture row count = %d, want 1", rowCount)
	}
	assertSQLiteTablesAbsent(t, db, "scheduler_run")
}

func assertSQLiteTablesPresent(t *testing.T, db *sql.DB, names ...string) {
	t.Helper()
	for _, name := range names {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count); err != nil {
			t.Fatalf("inspect table %s: %v", name, err)
		}
		if count != 1 {
			t.Fatalf("table %s count = %d, want 1", name, count)
		}
	}
}

func assertSQLiteTablesAbsent(t *testing.T, db *sql.DB, names ...string) {
	t.Helper()
	for _, name := range names {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count); err != nil {
			t.Fatalf("inspect absent table %s: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("table %s count = %d, want 0", name, count)
		}
	}
}

func assertNoForeignKeyViolations(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("run foreign key check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var fkID int
		if err := rows.Scan(&table, &rowID, &parent, &fkID); err != nil {
			t.Fatalf("scan foreign key violation: %v", err)
		}
		t.Fatalf("foreign key violation: table=%s row=%v parent=%s fk=%d", table, rowID, parent, fkID)
	}
	if err := rows.Err(); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("iterate foreign key check: %v", err)
	}
}
