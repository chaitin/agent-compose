package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigStoreMigrationAndTimeParsingWorkflows(t *testing.T) {
	testConfigStoreMigrationAndTimeParsingWorkflows(t)
}

func testConfigStoreMigrationAndTimeParsingWorkflows(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &ConfigStore{db: db}

	if _, err := store.tableColumnTypes(ctx, " "); err == nil {
		t.Fatalf("empty table name returned nil error")
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE global_env (
		name TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		secret INTEGER NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy global env: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO global_env(name, value, secret, updated_at)
		VALUES ('A', 'one', 1, '2026-06-02T09:00:00Z')`); err != nil {
		t.Fatalf("insert legacy global env: %v", err)
	}
	if err := store.rebuildGlobalEnvTable(ctx); err != nil {
		t.Fatalf("rebuildGlobalEnvTable returned error: %v", err)
	}
	columns, err := store.tableColumnTypes(ctx, "global_env")
	if err != nil {
		t.Fatalf("tableColumnTypes returned error: %v", err)
	}
	if !isIntegerColumnType(columns["updated_at"]) {
		t.Fatalf("updated_at column type = %q, want integer", columns["updated_at"])
	}
	items, err := store.ListGlobalEnv(ctx)
	if err != nil {
		t.Fatalf("ListGlobalEnv returned error: %v", err)
	}
	if len(items) != 1 || items[0].Name != "A" || !items[0].Secret {
		t.Fatalf("global env items = %#v", items)
	}

	if _, err := db.ExecContext(ctx, `CREATE TABLE workspace_config (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		type TEXT NOT NULL,
		config_json TEXT NOT NULL,
		comment TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy workspace config: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO workspace_config(id, name, type, config_json, comment, created_at, updated_at)
		VALUES ('ws-1', 'Workspace', 'file', '{}', 'legacy', '2026-06-02T09:00:00.000Z', '2026-06-02T09:01:00Z')`); err != nil {
		t.Fatalf("insert legacy workspace config: %v", err)
	}
	if err := store.rebuildWorkspaceConfigTable(ctx); err != nil {
		t.Fatalf("rebuildWorkspaceConfigTable returned error: %v", err)
	}
	workspace, err := store.GetWorkspaceConfig(ctx, "ws-1")
	if err != nil {
		t.Fatalf("GetWorkspaceConfig returned error: %v", err)
	}
	if workspace.Name != "Workspace" || workspace.Type != "file" || workspace.CreatedAt.IsZero() || workspace.UpdatedAt.IsZero() {
		t.Fatalf("workspace = %#v", workspace)
	}

	if !parseStoredLoaderTriggerTime(int(1000)).Equal(time.Unix(1000, 0).UTC()) {
		t.Fatalf("parseStoredLoaderTriggerTime int failed")
	}
	if !parseStoredLoaderTriggerTime(float64(1000)).Equal(time.Unix(1000, 0).UTC()) {
		t.Fatalf("parseStoredLoaderTriggerTime float failed")
	}
	if !parseStoredLoaderTriggerTime([]byte("1000")).Equal(time.Unix(1000, 0).UTC()) {
		t.Fatalf("parseStoredLoaderTriggerTime bytes failed")
	}
	if parseStoredLoaderTriggerTime(" ").IsZero() == false || parseStoredLoaderTriggerTime(struct{}{}).IsZero() == false {
		t.Fatalf("parseStoredLoaderTriggerTime empty/default failed")
	}
	if !parseStoredTime("2026-06-02T09:00:00.000Z").Equal(time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("parseStoredTime custom layout failed")
	}
	if !strings.Contains(normalizeSQLiteTimestampExpr("updated_at"), "updated_at") {
		t.Fatalf("normalizeSQLiteTimestampExpr missing column name")
	}
	if boolToInt(true) != 1 || boolToInt(false) != 0 {
		t.Fatalf("boolToInt returned unexpected values")
	}
}

func TestConfigStoreProjectSchemaMigration(t *testing.T) {
	testConfigStoreProjectSchemaMigration(t)
}

func testConfigStoreProjectSchemaMigration(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &ConfigStore{db: db}

	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("initSchema on empty db returned error: %v", err)
	}
	assertProjectSchema(t, store)
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("second initSchema on empty db returned error: %v", err)
	}
	assertProjectSchema(t, store)
}

func TestConfigStoreProjectSchemaMigrationPreservesExistingData(t *testing.T) {
	testConfigStoreProjectSchemaMigrationPreservesExistingData(t)
}

func TestConfigStoreProjectSchemaMigrationAddsBundleHashBeforeIndexes(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &ConfigStore{db: db}
	statements := []string{
		`CREATE TABLE project (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			source_path TEXT NOT NULL DEFAULT '',
			source_json TEXT NOT NULL DEFAULT '{}',
			current_revision INTEGER NOT NULL DEFAULT 0,
			spec_hash TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0,
			removed_at INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE TABLE project_revision (
			project_id TEXT NOT NULL,
			revision INTEGER NOT NULL,
			spec_hash TEXT NOT NULL,
			spec_json TEXT NOT NULL,
			created_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(project_id, revision)
		);`,
		`CREATE UNIQUE INDEX idx_project_revision_hash ON project_revision(project_id, spec_hash);`,
		`INSERT INTO project(id, name, source_path, source_json, current_revision, spec_hash, created_at, updated_at, removed_at)
			VALUES('project-legacy', 'legacy', '/tmp/agent-compose.yml', '{}', 1, 'sha256:legacy', 1, 1, 0);`,
		`INSERT INTO project_revision(project_id, revision, spec_hash, spec_json, created_at)
			VALUES('project-legacy', 1, 'sha256:legacy', '{"name":"legacy"}', 1);`,
	}
	for _, stmt := range statements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("prepare legacy project schema: %v", err)
		}
	}
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("initSchema returned error: %v", err)
	}
	assertTableColumns(t, store, "project", "bundle_hash")
	assertTableColumns(t, store, "project_revision", "bundle_hash")
	assertSQLiteIndexExists(t, db, "idx_project_revision_hash_bundle")
	revision, err := store.GetProjectRevision(ctx, "project-legacy", 1)
	if err != nil {
		t.Fatalf("GetProjectRevision returned error: %v", err)
	}
	if revision.BundleHash != "" || revision.SpecHash != "sha256:legacy" {
		t.Fatalf("revision after migration = %#v", revision)
	}
}

func testConfigStoreProjectSchemaMigrationPreservesExistingData(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	configDB := &ConfigStore{db: db}

	for _, ensure := range []func(context.Context) error{
		configDB.ensureGlobalEnvSchema,
		configDB.ensureCapabilityGatewaySchema,
		configDB.ensureWorkspaceConfigSchema,
		configDB.ensureLoaderSchema,
		configDB.ensureAgentDefinitionSchema,
		configDB.ensureEventSchema,
	} {
		if err := ensure(ctx); err != nil {
			t.Fatalf("prepare existing schema returned error: %v", err)
		}
	}

	agent, err := configDB.CreateAgentDefinition(ctx, AgentDefinition{
		ID:           "agent-existing",
		Name:         "Existing Agent",
		Provider:     "codex",
		Model:        "gpt-test",
		Driver:       driverpkg.RuntimeDriverBoxlite,
		GuestImage:   "guest:latest",
		SystemPrompt: "keep me",
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	loader, err := configDB.CreateLoader(ctx, Loader{
		Summary: LoaderSummary{
			ID:           "loader-existing",
			Name:         "Existing Loader",
			Runtime:      LoaderRuntimeScheduler,
			Enabled:      true,
			DefaultAgent: "codex",
		},
		Script: `{"steps":[]}`,
	})
	if err != nil {
		t.Fatalf("CreateLoader returned error: %v", err)
	}
	if _, err := configDB.db.ExecContext(ctx, `INSERT INTO loader_run(
		loader_id, run_id, trigger_id, trigger_kind, trigger_source, status, started_at
	) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		loader.Summary.ID, "run-existing", "manual", LoaderTriggerKindEvent, "legacy", LoaderRunStatusRunning, time.Now().UTC().UnixMilli()); err != nil {
		t.Fatalf("insert existing loader run: %v", err)
	}

	root := t.TempDir()
	sessionStore := &Store{config: &appconfig.Config{
		SessionRoot:          filepath.Join(root, "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "guest:latest",
		JupyterProxyBasePath: "/agent-compose/session",
		JupyterGuestPort:     8888,
	}}
	session, err := sessionStore.CreateSession(ctx, "Legacy Session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", SessionTypeManual, nil, nil, []SessionTag{{Name: "legacy", Value: "true"}})
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}

	if err := configDB.initSchema(ctx); err != nil {
		t.Fatalf("initSchema on existing db returned error: %v", err)
	}
	assertProjectSchema(t, configDB)

	loadedAgent, err := configDB.GetAgentDefinition(ctx, agent.ID)
	if err != nil {
		t.Fatalf("GetAgentDefinition after migration returned error: %v", err)
	}
	if loadedAgent.Name != agent.Name || loadedAgent.Provider != agent.Provider || loadedAgent.Model != agent.Model {
		t.Fatalf("loaded agent after migration = %#v, want %#v", loadedAgent, agent)
	}

	loadedLoader, err := configDB.GetLoader(ctx, loader.Summary.ID)
	if err != nil {
		t.Fatalf("GetLoader after migration returned error: %v", err)
	}
	if loadedLoader.Summary.Name != loader.Summary.Name || loadedLoader.Summary.RunCount != 1 {
		t.Fatalf("loaded loader after migration = %#v", loadedLoader)
	}
	run, err := configDB.GetLoaderRun(ctx, loader.Summary.ID, "run-existing")
	if err != nil {
		t.Fatalf("GetLoaderRun after migration returned error: %v", err)
	}
	if run.Status != LoaderRunStatusRunning || run.TriggerSource != "legacy" {
		t.Fatalf("loader run after migration = %#v", run)
	}

	loadedSession, err := sessionStore.GetSession(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("GetSession after config migration returned error: %v", err)
	}
	if loadedSession.Summary.Title != "Legacy Session" || len(loadedSession.Summary.Tags) != 1 {
		t.Fatalf("loaded session after config migration = %#v", loadedSession)
	}
}

func assertProjectSchema(t *testing.T, store *ConfigStore) {
	t.Helper()
	for table, columns := range map[string][]string{
		"project":          {"id", "name", "source_path", "source_json", "current_revision", "spec_hash", "bundle_hash", "created_at", "updated_at", "removed_at"},
		"project_revision": {"project_id", "revision", "spec_hash", "bundle_hash", "spec_json", "created_at"},
		"project_agent":    {"project_id", "agent_name", "managed_agent_id", "revision", "provider", "model", "image", "driver", "scheduler_enabled", "spec_json", "created_at", "updated_at"},
		"project_service": {"project_id", "service_name", "revision", "runtime", "entry", "input_schema_ref", "output_schema_ref", "error_schema_ref", "timeout_ms", "spec_json",
			"created_at", "updated_at"},
		"project_scheduler": {"project_id", "scheduler_id", "agent_name", "target_type", "target_name", "managed_loader_id", "revision", "enabled", "trigger_count", "spec_json",
			"created_at", "updated_at"},
		"project_run": {"run_id", "project_id", "project_name", "project_revision", "agent_name", "managed_agent_id", "source", "scheduler_id", "trigger_id", "status",
			"session_id", "exit_code", "error", "prompt", "output", "result_json", "logs_path", "artifacts_dir", "cleanup_error", "driver", "image_ref", "started_at",
			"completed_at", "duration_ms", "created_at", "updated_at"},
		"agent_definition": {"managed_project_id", "managed_project_revision", "managed_agent_name"},
		"loader":           {"managed_project_id", "managed_project_revision", "managed_agent_name", "managed_scheduler_id"},
	} {
		assertTableColumns(t, store, table, columns...)
	}
	for _, index := range []string{
		"idx_project_name",
		"idx_project_source_path",
		"idx_project_revision_hash_bundle",
		"idx_project_agent_managed_agent",
		"idx_project_scheduler_agent",
		"idx_project_scheduler_target",
		"idx_project_scheduler_managed_loader",
		"idx_project_run_project_status",
		"idx_project_run_agent",
		"idx_project_run_session",
		"idx_project_run_scheduler",
		"idx_agent_definition_managed_project",
		"idx_loader_managed_project",
	} {
		assertSQLiteIndexExists(t, store.db, index)
	}
}

func assertTableColumns(t *testing.T, store *ConfigStore, table string, columns ...string) {
	t.Helper()
	columnTypes, err := store.tableColumnTypes(context.Background(), table)
	if err != nil {
		t.Fatalf("tableColumnTypes(%s) returned error: %v", table, err)
	}
	if len(columnTypes) == 0 {
		t.Fatalf("table %s does not exist or has no columns", table)
	}
	for _, column := range columns {
		if _, ok := columnTypes[column]; !ok {
			t.Fatalf("table %s missing column %s; columns=%v", table, column, columnTypes)
		}
	}
}

func assertSQLiteIndexExists(t *testing.T, db *sql.DB, indexName string) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, indexName).Scan(&count); err != nil {
		t.Fatalf("query sqlite index %s: %v", indexName, err)
	}
	if count != 1 {
		t.Fatalf("sqlite index %s count = %d, want 1", indexName, count)
	}
}
