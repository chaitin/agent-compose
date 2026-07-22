package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenReportsPingFailure(t *testing.T) {
	database, err := Open(t.TempDir(), 0)
	if database != nil {
		_ = database.Close()
		t.Fatal("Open returned a database for a directory path")
	}
	if err == nil || !strings.Contains(err.Error(), "ping SQLite database") {
		t.Fatalf("Open error = %v, want ping failure", err)
	}
}

func TestMigrationBaseline(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	if err := applyMigrations(ctx, db, embeddedMigrations); err != nil {
		t.Fatalf("applyMigrations: %v", err)
	}

	tables := []string{
		"schema_migrations", "global_env", "workspace_config", "agent_definition",
		"llm_provider", "llm_model", "llm_provider_model", "llm_facade_token",
		"capability_gateway", "volumes", "project_volumes", "loader", "loader_trigger",
		"loader_run", "loader_event", "loader_state", "loader_binding", "project",
		"project_revision", "project_agent", "project_scheduler", "project_run",
		"project_run_event", "event", "webhook_source", "event_delivery", "event_sandbox_link",
		"sandbox_projection_meta", "sandboxes",
	}
	for _, table := range tables {
		var count int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("query table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s count = %d, want 1", table, count)
		}
	}
	var tableCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&tableCount); err != nil {
		t.Fatalf("count application tables: %v", err)
	}
	if tableCount != len(tables) {
		t.Fatalf("application table count = %d, want %d", tableCount, len(tables))
	}
	for _, index := range []string{
		"idx_agent_definition_deleted_enabled", "idx_agent_definition_managed_project", "idx_agent_definition_workspace",
		"idx_event_correlation", "idx_event_delivery_loader_run", "idx_event_delivery_run", "idx_event_delivery_status",
		"idx_event_dispatch", "idx_event_dispatch_attempt", "idx_event_idempotency", "idx_event_parent",
		"idx_event_sandbox_link_loader_run", "idx_event_sandbox_link_run", "idx_event_sandbox_link_sandbox",
		"idx_event_topic_sequence", "idx_llm_facade_token_sandbox", "idx_loader_event_created",
		"idx_loader_event_run_created", "idx_loader_managed_project", "idx_loader_run_prune",
		"idx_loader_run_started", "idx_loader_run_status_started", "idx_loader_run_trigger_started",
		"idx_loader_trigger_schedule", "idx_project_agent_id", "idx_project_agent_managed_agent",
		"idx_project_name", "idx_project_revision_hash", "idx_project_run_agent",
		"idx_project_run_event_sequence", "idx_project_run_project_status", "idx_project_run_sandbox",
		"idx_project_run_scheduler", "idx_project_scheduler_agent", "idx_project_scheduler_id",
		"idx_project_scheduler_managed_loader", "idx_project_short_id", "idx_project_source_path",
		"idx_project_volumes_volume", "idx_sandboxes_type_updated", "idx_sandboxes_updated",
		"idx_sandboxes_vm_status_updated", "idx_volumes_driver", "idx_volumes_project",
		"idx_webhook_source_enabled_topic",
	} {
		assertSQLiteIndexExists(t, db, index)
	}
	assertSQLiteIndexUnique(t, db, "idx_event_idempotency", true)
	var idempotencyIndexSQL string
	if err := db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_event_idempotency'`).Scan(&idempotencyIndexSQL); err != nil {
		t.Fatalf("query partial idempotency index: %v", err)
	}
	if !strings.Contains(idempotencyIndexSQL, "WHERE idempotency_key != ''") {
		t.Fatalf("idempotency index is not partial: %s", idempotencyIndexSQL)
	}

	available, err := loadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	appliedAt := make(map[int64]int64, len(available))
	rows, err := db.QueryContext(ctx,
		`SELECT version, name, checksum, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query migration history: %v", err)
	}
	migrationIndex := 0
	for rows.Next() {
		var version, timestamp int64
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum, &timestamp); err != nil {
			_ = rows.Close()
			t.Fatalf("scan migration history: %v", err)
		}
		if migrationIndex >= len(available) {
			_ = rows.Close()
			t.Fatalf("unexpected migration history row (%d, %q)", version, name)
		}
		expected := available[migrationIndex]
		if version != expected.version || name != expected.name || checksum != expected.checksum {
			_ = rows.Close()
			t.Fatalf("migration history row %d = (%d, %q, %q), want (%d, %q, %q)",
				migrationIndex, version, name, checksum, expected.version, expected.name, expected.checksum)
		}
		appliedAt[version] = timestamp
		migrationIndex++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatalf("iterate migration history: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close migration history: %v", err)
	}
	if migrationIndex != len(available) {
		t.Fatalf("migration history row count = %d, want %d", migrationIndex, len(available))
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO project_revision(project_id, revision, spec_hash, spec_json) VALUES('missing', 1, 'hash', '{}')`); err == nil {
		t.Fatal("foreign key enforcement is disabled")
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO global_env(name, value, secret, updated_at) VALUES('PRESERVED', 'before', 0, 1234)`); err != nil {
		t.Fatalf("insert data before second initialization: %v", err)
	}
	if err := applyMigrations(ctx, db, embeddedMigrations); err != nil {
		t.Fatalf("second applyMigrations: %v", err)
	}
	var historyCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&historyCount); err != nil {
		t.Fatalf("count migration history: %v", err)
	}
	if historyCount != len(available) {
		t.Fatalf("migration history count = %d, want %d", historyCount, len(available))
	}
	var value string
	var updatedAt int64
	if err := db.QueryRowContext(ctx,
		`SELECT value, updated_at FROM global_env WHERE name = 'PRESERVED'`).Scan(&value, &updatedAt); err != nil {
		t.Fatalf("query data after second initialization: %v", err)
	}
	if value != "before" || updatedAt != 1234 {
		t.Fatalf("data after second initialization = (%q, %d), want (%q, %d)", value, updatedAt, "before", 1234)
	}
	for _, item := range available {
		var currentAppliedAt int64
		if err := db.QueryRowContext(ctx,
			`SELECT applied_at FROM schema_migrations WHERE version = ?`, item.version).Scan(&currentAppliedAt); err != nil {
			t.Fatalf("query migration %d time after second initialization: %v", item.version, err)
		}
		if currentAppliedAt != appliedAt[item.version] {
			t.Fatalf("migration %d applied_at changed from %d to %d", item.version, appliedAt[item.version], currentAppliedAt)
		}
	}
}

func TestLoaderBindingConfigHashMigration(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	available, err := loadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(available) < 2 || available[1].version != 2 {
		t.Fatalf("available migrations = %#v, want version 2", available)
	}
	if err := applyMigrationSet(ctx, db, available[:1]); err != nil {
		t.Fatalf("apply baseline migration: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO loader_binding(loader_id, trigger_id, sandbox_id, created_at, updated_at)
		VALUES('loader-1', 'trigger-1', 'sandbox-1', 1, 2)`); err != nil {
		t.Fatalf("insert pre-migration binding: %v", err)
	}

	if err := applyMigrations(ctx, db, embeddedMigrations); err != nil {
		t.Fatalf("apply config hash migration: %v", err)
	}
	var sandboxID, configHash string
	if err := db.QueryRowContext(ctx, `SELECT sandbox_id, sandbox_config_hash FROM loader_binding WHERE loader_id = 'loader-1' AND trigger_id = 'trigger-1'`).Scan(&sandboxID, &configHash); err != nil {
		t.Fatalf("query migrated binding: %v", err)
	}
	if sandboxID != "sandbox-1" || configHash != "" {
		t.Fatalf("migrated binding = (%q, %q), want (%q, %q)", sandboxID, configHash, "sandbox-1", "")
	}
	var versionCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 2`).Scan(&versionCount); err != nil {
		t.Fatalf("query migration history: %v", err)
	}
	if versionCount != 1 {
		t.Fatalf("version 2 history count = %d, want 1", versionCount)
	}
	if err := applyMigrations(ctx, db, embeddedMigrations); err != nil {
		t.Fatalf("reapply migrations: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 2`).Scan(&versionCount); err != nil {
		t.Fatalf("query migration history after reapply: %v", err)
	}
	if versionCount != 1 {
		t.Fatalf("version 2 history count after reapply = %d, want 1", versionCount)
	}
}

func TestBaselineIncludesPreviouslyOmittedSchema(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	if err := applyMigrations(ctx, db, embeddedMigrations); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	indexDefinitions := []struct {
		name       string
		columns    []string
		descending []bool
	}{
		{name: "idx_sandboxes_updated", columns: []string{"updated_at", "id"}, descending: []bool{true, true}},
		{name: "idx_sandboxes_vm_status_updated", columns: []string{"vm_status_search", "updated_at", "id"}, descending: []bool{false, true, true}},
		{name: "idx_sandboxes_type_updated", columns: []string{"sandbox_type", "updated_at", "id"}, descending: []bool{false, true, true}},
		{name: "idx_loader_run_trigger_started", columns: []string{"loader_id", "trigger_id", "started_at", "run_id"}, descending: []bool{false, false, true, true}},
		{name: "idx_loader_run_status_started", columns: []string{"loader_id", "status", "started_at", "run_id"}, descending: []bool{false, false, true, true}},
		{name: "idx_loader_run_prune", columns: []string{"loader_id", "status", "completed_at", "started_at", "run_id"}, descending: []bool{false, false, false, false, false}},
		{name: "idx_loader_event_run_created", columns: []string{"loader_id", "run_id", "created_at", "event_id"}, descending: []bool{false, false, true, true}},
		{name: "idx_event_sandbox_link_loader_run", columns: []string{"loader_id", "run_id"}, descending: []bool{false, false}},
		{name: "idx_event_delivery_loader_run", columns: []string{"loader_id", "run_id"}, descending: []bool{false, false}},
	}
	for _, definition := range indexDefinitions {
		assertSQLiteIndexColumns(t, db, definition.name, definition.columns, definition.descending)
	}
}
func assertSQLiteIndexColumns(t *testing.T, db *sql.DB, indexName string, columns []string, descending []bool) {
	t.Helper()
	if len(columns) != len(descending) {
		t.Fatalf("index %s expectation has %d columns and %d sort directions", indexName, len(columns), len(descending))
	}
	rows, err := db.QueryContext(context.Background(),
		`SELECT name, "desc" FROM pragma_index_xinfo(?) WHERE key = 1 ORDER BY seqno`, indexName)
	if err != nil {
		t.Fatalf("query index %s columns: %v", indexName, err)
	}
	defer func() { _ = rows.Close() }()
	position := 0
	for rows.Next() {
		var name string
		var desc int
		if err := rows.Scan(&name, &desc); err != nil {
			t.Fatalf("scan index %s column: %v", indexName, err)
		}
		if position >= len(columns) {
			t.Fatalf("index %s has unexpected column %q", indexName, name)
		}
		if name != columns[position] || (desc != 0) != descending[position] {
			t.Fatalf("index %s column %d = (%q, desc=%v), want (%q, desc=%v)",
				indexName, position, name, desc != 0, columns[position], descending[position])
		}
		position++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate index %s columns: %v", indexName, err)
	}
	if position != len(columns) {
		t.Fatalf("index %s column count = %d, want %d", indexName, position, len(columns))
	}
}

func TestLoadMigrationsValidation(t *testing.T) {
	tests := []struct {
		name    string
		files   fstest.MapFS
		wantErr string
	}{
		{
			name:    "no SQL files",
			files:   fstest.MapFS{"migrations/README.md": {Data: []byte("docs")}},
			wantErr: "no embedded",
		},
		{
			name:    "invalid filename",
			files:   fstest.MapFS{"migrations/1_bad.sql": {Data: []byte("SELECT 1")}},
			wantErr: "invalid SQLite migration filename",
		},
		{
			name:    "empty SQL",
			files:   fstest.MapFS{"migrations/000001_empty.sql": {Data: []byte(" \n")}},
			wantErr: "is empty",
		},
		{
			name: "duplicate version",
			files: fstest.MapFS{
				"migrations/000001_one.sql": {Data: []byte("SELECT 1")},
				"migrations/000001_two.sql": {Data: []byte("SELECT 2")},
			},
			wantErr: "duplicate SQLite migration version",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := loadMigrations(test.files)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("loadMigrations error = %v, want containing %q", err, test.wantErr)
			}
		})
	}

	items, err := loadMigrations(fstest.MapFS{
		"migrations/000002_second.sql": {Data: []byte("SELECT 2")},
		"migrations/000001_first.sql":  {Data: []byte("SELECT 1")},
	})
	if err != nil {
		t.Fatalf("load sorted migrations: %v", err)
	}
	if items[0].version != 1 || items[1].version != 2 {
		t.Fatalf("migration order = %d, %d", items[0].version, items[1].version)
	}
}

func TestValidateAppliedMigrations(t *testing.T) {
	available := []migration{
		{version: 1, name: "000001_first", checksum: "one"},
		{version: 3, name: "000003_third", checksum: "three"},
	}
	tests := []struct {
		name    string
		applied []appliedMigration
		wantErr string
	}{
		{name: "valid prefix", applied: []appliedMigration{{version: 1, name: "000001_first", checksum: "one"}}},
		{name: "newer database", applied: []appliedMigration{{version: 1}, {version: 3}, {version: 4}}, wantErr: "newer than this binary"},
		{name: "non-prefix", applied: []appliedMigration{{version: 3, name: "000003_third", checksum: "three"}}, wantErr: "not an embedded prefix"},
		{name: "renamed", applied: []appliedMigration{{version: 1, name: "renamed", checksum: "one"}}, wantErr: "name mismatch"},
		{name: "modified", applied: []appliedMigration{{version: 1, name: "000001_first", checksum: "changed"}}, wantErr: "checksum mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateAppliedMigrations(available, test.applied)
			if test.wantErr == "" && err != nil {
				t.Fatalf("validateAppliedMigrations: %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestMigrationFailureRollsBackPendingBatch(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	available, err := loadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatalf("load baseline migration: %v", err)
	}
	first := available[0]
	if err := applyMigrationSet(ctx, db, []migration{first}); err != nil {
		t.Fatalf("apply first migration: %v", err)
	}
	second := migration{
		version: first.version + 1, name: "000002_broken",
		statement: `CREATE TABLE pending_table(id INTEGER); THIS IS NOT SQL;`, checksum: "two",
	}
	if err := applyMigrationSet(ctx, db, []migration{first, second}); err == nil {
		t.Fatal("broken migration returned nil error")
	}

	var pendingCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'pending_table'`).Scan(&pendingCount); err != nil {
		t.Fatalf("query pending table: %v", err)
	}
	if pendingCount != 0 {
		t.Fatalf("pending table count = %d, want 0", pendingCount)
	}
	var historyCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&historyCount); err != nil {
		t.Fatalf("count migration history: %v", err)
	}
	if historyCount != 1 {
		t.Fatalf("migration history count = %d, want 1", historyCount)
	}
	var name, checksum string
	if err := db.QueryRowContext(ctx,
		`SELECT name, checksum FROM schema_migrations WHERE version = 1`).Scan(&name, &checksum); err != nil {
		t.Fatalf("query prior migration after rollback: %v", err)
	}
	if name != first.name || checksum != first.checksum {
		t.Fatalf("prior migration after rollback = (%q, %q), want (%q, %q)", name, checksum, first.name, first.checksum)
	}
}

func TestMigrationHonorsCanceledContext(t *testing.T) {
	db := newMemoryDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := applyMigrationSet(ctx, db, []migration{{
		version: 1, name: "000001_first", statement: `CREATE TABLE canceled(id INTEGER)`, checksum: "one",
	}})
	if err == nil {
		t.Fatal("canceled migration returned nil error")
	}
}

func TestSQLiteDSNConfiguresProductionConnection(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "data.db")
	db, err := sql.Open("sqlite", sqliteDSN(dbPath, 5*time.Second))
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })

	var journalMode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("query journal mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal mode = %q, want wal", journalMode)
	}
	var foreignKeys, busyTimeout int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("query foreign keys: %v", err)
	}
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy timeout: %v", err)
	}
	if foreignKeys != 1 || busyTimeout != 5000 {
		t.Fatalf("connection PRAGMAs = (foreign_keys=%d, busy_timeout=%d), want (1, 5000)", foreignKeys, busyTimeout)
	}
}

func TestMigratesEveryLegacyAddedColumn(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	available, err := loadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if _, err := db.ExecContext(ctx, available[0].statement); err != nil {
		t.Fatalf("create unversioned baseline fixture: %v", err)
	}

	for _, index := range []string{
		"idx_agent_definition_managed_project", "idx_loader_managed_project",
		"idx_project_short_id", "idx_project_agent_id", "idx_project_scheduler_id",
		"idx_project_run_sandbox", "idx_event_dispatch_attempt", "idx_event_parent",
	} {
		if _, err := db.ExecContext(ctx, `DROP INDEX `+index); err != nil {
			t.Fatalf("drop fixture index %s: %v", index, err)
		}
	}

	legacyColumns := map[string][]string{
		"agent_definition":  {"capset_ids", "volumes_json", "skills", "managed_project_id", "managed_project_revision", "managed_agent_name"},
		"llm_provider":      {"use_generic_responses_text_parts"},
		"loader":            {"agent_id", "capset_ids", "volumes_json", "managed_project_id", "managed_project_revision", "managed_agent_name", "managed_scheduler_id"},
		"project":           {"short_id"},
		"project_agent":     {"id", "name", "short_id"},
		"project_scheduler": {"id", "short_id"},
		"project_run":       {"sandbox_id"},
		"event":             {"replay_of_event_id", "claim_id", "claim_until", "attempt_count", "next_attempt_at", "last_error", "dead_letter_at"},
		"webhook_source":    {"token_header"},
	}
	for table, columns := range legacyColumns {
		for _, column := range columns {
			if _, err := db.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %q DROP COLUMN %q`, table, column)); err != nil {
				t.Fatalf("drop fixture column %s.%s: %v", table, column, err)
			}
		}
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO agent_definition(id, name, created_at, updated_at) VALUES('agent-1', 'preserved', 1, 2)`); err != nil {
		t.Fatalf("insert legacy agent: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO loader(id, name, script) VALUES('loader-1', 'preserved', 'return 1')`); err != nil {
		t.Fatalf("insert legacy loader: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO project(id, name) VALUES('project-1', 'preserved')`); err != nil {
		t.Fatalf("insert legacy project: %v", err)
	}

	if err := applyMigrations(ctx, db, embeddedMigrations); err != nil {
		t.Fatalf("migrate legacy columns: %v", err)
	}
	for table, columns := range legacyColumns {
		types, err := sqliteTableColumnTypes(ctx, db, table)
		if err != nil {
			t.Fatalf("inspect migrated table %s: %v", table, err)
		}
		for _, column := range columns {
			if _, ok := types[column]; !ok {
				t.Fatalf("migrated table %s missing column %s", table, column)
			}
		}
	}
	for _, index := range []string{
		"idx_agent_definition_managed_project", "idx_loader_managed_project",
		"idx_project_short_id", "idx_project_agent_id", "idx_project_scheduler_id",
		"idx_project_run_sandbox", "idx_event_dispatch_attempt", "idx_event_parent",
		"idx_loader_run_trigger_started", "idx_loader_run_status_started", "idx_loader_run_prune",
		"idx_loader_event_run_created", "idx_event_sandbox_link_loader_run", "idx_event_delivery_loader_run",
		"idx_sandboxes_updated", "idx_sandboxes_vm_status_updated", "idx_sandboxes_type_updated",
	} {
		assertSQLiteIndexExists(t, db, index)
	}
	for _, table := range []string{"sandbox_projection_meta", "sandboxes"} {
		var count int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("query migrated table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("migrated table %s count = %d, want 1", table, count)
		}
	}
	for table, id := range map[string]string{"agent_definition": "agent-1", "loader": "loader-1", "project": "project-1"} {
		var name string
		if err := db.QueryRowContext(ctx, `SELECT name FROM `+table+` WHERE id = ?`, id).Scan(&name); err != nil {
			t.Fatalf("query preserved %s: %v", table, err)
		}
		if name != "preserved" {
			t.Fatalf("preserved %s name = %q", table, name)
		}
	}
}

func TestMigratesLegacyTimestampTables(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	statements := []string{
		`CREATE TABLE global_env(name TEXT PRIMARY KEY, value TEXT NOT NULL, secret INTEGER NOT NULL, updated_at TEXT NOT NULL)`,
		`INSERT INTO global_env VALUES('A', 'one', 1, '2026-06-02T09:00:00Z')`,
		`CREATE TABLE workspace_config(id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL, config_json TEXT NOT NULL, comment TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`INSERT INTO workspace_config VALUES('ws-1', 'Workspace', 'file', '{}', 'legacy', '2026-06-02T09:00:00Z', '2026-06-02T09:01:00Z')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare legacy timestamp fixture: %v", err)
		}
	}
	if err := applyMigrations(ctx, db, embeddedMigrations); err != nil {
		t.Fatalf("migrate legacy timestamp tables: %v", err)
	}
	for table, columns := range map[string][]string{
		"global_env":       {"updated_at"},
		"workspace_config": {"created_at", "updated_at"},
	} {
		types, err := sqliteTableColumnTypes(ctx, db, table)
		if err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		for _, column := range columns {
			if !isIntegerColumnType(types[column]) {
				t.Fatalf("%s.%s type = %q, want integer", table, column, types[column])
			}
		}
	}
	var globalName string
	if err := db.QueryRowContext(ctx, `SELECT name FROM global_env WHERE name = 'A'`).Scan(&globalName); err != nil || globalName != "A" {
		t.Fatalf("migrated global env name = %q, err=%v", globalName, err)
	}
	var workspaceName string
	if err := db.QueryRowContext(ctx, `SELECT name FROM workspace_config WHERE id = 'ws-1'`).Scan(&workspaceName); err != nil || workspaceName != "Workspace" {
		t.Fatalf("migrated workspace name = %q, err=%v", workspaceName, err)
	}
}

func TestMigratesLegacyLoaderTimestampsOnce(t *testing.T) {
	ctx := context.Background()
	db := newMemoryDB(t)
	available, err := loadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if _, err := db.ExecContext(ctx, available[0].statement); err != nil {
		t.Fatalf("create unversioned baseline fixture: %v", err)
	}
	const seconds int64 = 1_720_000_000
	statements := []string{
		`INSERT INTO loader(id, name, script) VALUES('loader-1', 'legacy', 'return 1')`,
		`INSERT INTO loader_trigger(loader_id, trigger_id, kind, next_fire_at, last_fired_at)
			VALUES('loader-1', 'trigger-1', 'interval', 1720000000, 1720000001)`,
		`INSERT INTO loader_run(loader_id, run_id, started_at, completed_at)
			VALUES('loader-1', 'run-1', 1720000002, 1720000003)`,
		`INSERT INTO loader_event(loader_id, event_id, type, created_at)
			VALUES('loader-1', 'event-1', 'legacy', 1720000004)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare legacy loader timestamp fixture: %v", err)
		}
	}

	for pass := 1; pass <= 2; pass++ {
		if err := applyMigrations(ctx, db, embeddedMigrations); err != nil {
			t.Fatalf("applyMigrations pass %d: %v", pass, err)
		}
	}
	var nextFireAt, lastFiredAt, startedAt, completedAt, eventCreatedAt int64
	if err := db.QueryRowContext(ctx,
		`SELECT next_fire_at, last_fired_at FROM loader_trigger WHERE loader_id = 'loader-1' AND trigger_id = 'trigger-1'`).
		Scan(&nextFireAt, &lastFiredAt); err != nil {
		t.Fatalf("query migrated loader trigger timestamps: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT started_at, completed_at FROM loader_run WHERE loader_id = 'loader-1' AND run_id = 'run-1'`).
		Scan(&startedAt, &completedAt); err != nil {
		t.Fatalf("query migrated loader run timestamps: %v", err)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT created_at FROM loader_event WHERE loader_id = 'loader-1' AND event_id = 'event-1'`).
		Scan(&eventCreatedAt); err != nil {
		t.Fatalf("query migrated loader event timestamp: %v", err)
	}
	want := []int64{seconds * 1000, (seconds + 1) * 1000, (seconds + 2) * 1000, (seconds + 3) * 1000, (seconds + 4) * 1000}
	got := []int64{nextFireAt, lastFiredAt, startedAt, completedAt, eventCreatedAt}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("migrated loader timestamp %d = %d, want %d", index, got[index], want[index])
		}
	}
}

func TestApplyMigrationsRequiresBaselineVersion(t *testing.T) {
	db := newMemoryDB(t)
	err := applyMigrations(context.Background(), db, fstest.MapFS{
		"migrations/000002_second.sql": {Data: []byte("SELECT 1")},
	})
	if err == nil || !strings.Contains(err.Error(), "must be version 000001") {
		t.Fatalf("applyMigrations error = %v", err)
	}
}

func TestMigrationFSReadError(t *testing.T) {
	_, err := loadMigrations(failingMigrationFS{})
	if err == nil || !strings.Contains(err.Error(), "read embedded SQLite migrations") {
		t.Fatalf("loadMigrations error = %v", err)
	}
}

func assertSQLiteIndexExists(t *testing.T, db *sql.DB, indexName string) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, indexName).Scan(&count); err != nil {
		t.Fatalf("query SQLite index %s: %v", indexName, err)
	}
	if count != 1 {
		t.Fatalf("SQLite index %s count = %d, want 1", indexName, count)
	}
}

func assertSQLiteIndexUnique(t *testing.T, db *sql.DB, indexName string, want bool) {
	t.Helper()
	var unique int
	if err := db.QueryRowContext(context.Background(), `
		SELECT il."unique"
		FROM sqlite_master AS sm, pragma_index_list(sm.tbl_name) AS il
		WHERE sm.type = 'index' AND sm.name = ? AND il.name = sm.name
	`, indexName).Scan(&unique); err != nil {
		t.Fatalf("query SQLite index %s uniqueness: %v", indexName, err)
	}
	if (unique != 0) != want {
		t.Fatalf("SQLite index %s unique = %v, want %v", indexName, unique != 0, want)
	}
}

func newMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(":memory:", defaultBusyTimeout))
	if err != nil {
		t.Fatalf("open in-memory SQLite: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type failingMigrationFS struct{}

func (failingMigrationFS) Open(string) (fs.File, error) {
	return nil, fmt.Errorf("read failed")
}
