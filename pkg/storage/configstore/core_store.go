package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/samber/do/v2"

	"agent-compose/pkg/capabilities"
	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
)

type (
	SessionEnvVar          = domain.SandboxEnvVar
	WorkspaceConfig        = domain.WorkspaceConfig
	Loader                 = domain.Loader
	ProjectRecord          = domain.ProjectRecord
	ProjectRevisionRecord  = domain.ProjectRevisionRecord
	ProjectAgentRecord     = domain.ProjectAgentRecord
	ProjectSchedulerRecord = domain.ProjectSchedulerRecord
	ProjectRunRecord       = domain.ProjectRunRecord
	ProjectRunEventRecord  = domain.ProjectRunEventRecord
	ProjectListOptions     = domain.ProjectListOptions
	ProjectRunListOptions  = domain.ProjectRunListOptions
	ProjectListResult      = domain.ProjectListResult
)

// coreStore owns the shared configuration domains: global env vars, workspace
// configs, and agent definitions.
type coreStore struct {
	db *sql.DB
}

// ConfigStore is the composite persistence facade over DATA_ROOT/data.db. Each
// domain lives on its own sub-store sharing the same *sql.DB; embedding
// promotes every domain method onto ConfigStore, so callers and the domain
// packages' consumer interfaces are unaffected by the internal split.
//
// ConfigStore must be constructed via NewConfigStore or FromDB, which wire the
// embedded sub-stores. The zero value (or a struct literal) leaves them nil and
// every promoted method would panic; the sub-store types are unexported to keep
// direct construction confined to this package.
type ConfigStore struct {
	db *sql.DB

	*coreStore
	*loaderStore
	*eventStore
	*projectStore
	*sandboxStore
	*llmStore
	*capabilityGatewayStore
	*volumeStore
}

func NewConfigStore(di do.Injector) (*ConfigStore, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	if err := os.MkdirAll(config.DataRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create agent-compose data root: %w", err)
	}
	dbPath := config.DbAddr
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := FromDB(db)
	if err := store.initSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func FromDB(db *sql.DB) *ConfigStore {
	return &ConfigStore{
		db:                     db,
		coreStore:              &coreStore{db: db},
		loaderStore:            &loaderStore{db: db},
		eventStore:             &eventStore{db: db},
		projectStore:           &projectStore{db: db},
		sandboxStore:           &sandboxStore{db: db},
		llmStore:               &llmStore{db: db},
		capabilityGatewayStore: &capabilityGatewayStore{db: db},
		volumeStore:            &volumeStore{db: db},
	}
}

func (s *ConfigStore) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

func (s *coreStore) InitCoreSchema(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode = WAL;`,
		`PRAGMA foreign_keys = ON;`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("init agent-compose config schema: %w", err)
		}
	}
	if err := s.ensureGlobalEnvSchema(ctx); err != nil {
		return err
	}
	if err := s.ensureWorkspaceConfigSchema(ctx); err != nil {
		return err
	}
	if err := s.ensureAgentDefinitionSchema(ctx); err != nil {
		return err
	}
	return nil
}

func (s *ConfigStore) initSchema(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("config store is required")
	}
	if err := s.migrateLegacySQLiteSchema(ctx); err != nil {
		return err
	}
	if err := s.InitCoreSchema(ctx); err != nil {
		return err
	}
	if err := s.ensureLLMSchema(ctx); err != nil {
		return err
	}
	if err := s.ensureCapabilityGatewaySchema(ctx); err != nil {
		return err
	}
	if err := s.ensureVolumeSchema(ctx); err != nil {
		return err
	}
	if err := s.ensureLoaderSchema(ctx); err != nil {
		return err
	}
	if err := s.ensureProjectSchema(ctx); err != nil {
		return err
	}
	if err := s.ensureSandboxSchema(ctx); err != nil {
		return err
	}
	if err := s.ensureEventSchema(ctx); err != nil {
		return err
	}
	if err := s.copyLegacyEventSessionLinks(ctx); err != nil {
		return err
	}
	return nil
}

func (s *ConfigStore) InitSchema(ctx context.Context) error {
	return s.initSchema(ctx)
}

func (s *ConfigStore) migrateLegacySQLiteSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("config store is required")
	}
	for _, item := range []struct {
		table   string
		legacy  string
		current string
	}{
		{table: "loader", legacy: "session_policy", current: "sandbox_policy"},
		{table: "loader_binding", legacy: "session_id", current: "sandbox_id"},
		{table: "loader_event", legacy: "linked_session_id", current: "linked_sandbox_id"},
		{table: "loader_event", legacy: "linked_agent_session_id", current: "linked_agent_thread_id"},
		{table: "llm_facade_token", legacy: "session_id", current: "sandbox_id"},
	} {
		columns, err := s.tableColumnTypes(ctx, item.table)
		if err != nil {
			return err
		}
		if _, legacyExists := columns[item.legacy]; !legacyExists {
			continue
		}
		if _, currentExists := columns[item.current]; currentExists {
			if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
				`UPDATE %q SET %q = %q WHERE %q = ''`, item.table, item.current, item.legacy, item.current,
			)); err != nil {
				return fmt.Errorf("copy legacy SQLite column %s.%s to %s: %w", item.table, item.legacy, item.current, err)
			}
			continue
		}
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
			`ALTER TABLE %q RENAME COLUMN %q TO %q`, item.table, item.legacy, item.current,
		)); err != nil {
			return fmt.Errorf("migrate legacy SQLite column %s.%s to %s: %w", item.table, item.legacy, item.current, err)
		}
	}
	return nil
}

func (s *ConfigStore) copyLegacyEventSessionLinks(ctx context.Context) error {
	exists, err := s.tableExists(ctx, "event_session_link")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO event_sandbox_link(
		event_id, sandbox_id, relation, loader_id, run_id, trigger_id, loader_event_id, created_at
	) SELECT event_id, session_id, relation, loader_id, run_id, trigger_id, loader_event_id, created_at
	FROM event_session_link`)
	if err != nil {
		return fmt.Errorf("copy legacy event_session_link rows: %w", err)
	}
	return nil
}

func (s *coreStore) ensureGlobalEnvSchema(ctx context.Context) error {
	const createStmt = `CREATE TABLE IF NOT EXISTS global_env (
		name TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		secret INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
	);`
	if _, err := s.db.ExecContext(ctx, createStmt); err != nil {
		return fmt.Errorf("create global env schema: %w", err)
	}
	columnTypes, err := s.tableColumnTypes(ctx, "global_env")
	if err != nil {
		return err
	}
	if IsIntegerColumnType(columnTypes["updated_at"]) {
		return nil
	}
	return s.rebuildGlobalEnvTable(ctx)
}

func (s *coreStore) EnsureGlobalEnvSchema(ctx context.Context) error {
	return s.ensureGlobalEnvSchema(ctx)
}

func (s *coreStore) ensureWorkspaceConfigSchema(ctx context.Context) error {
	const createStmt = `CREATE TABLE IF NOT EXISTS workspace_config (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		type TEXT NOT NULL,
		config_json TEXT NOT NULL DEFAULT '{}',
		comment TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
		updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
	);`
	if _, err := s.db.ExecContext(ctx, createStmt); err != nil {
		return fmt.Errorf("create workspace config schema: %w", err)
	}
	columnTypes, err := s.tableColumnTypes(ctx, "workspace_config")
	if err != nil {
		return err
	}
	if IsIntegerColumnType(columnTypes["created_at"]) && IsIntegerColumnType(columnTypes["updated_at"]) {
		return nil
	}
	return s.rebuildWorkspaceConfigTable(ctx)
}

func (s *coreStore) EnsureWorkspaceConfigSchema(ctx context.Context) error {
	return s.ensureWorkspaceConfigSchema(ctx)
}

func (s *coreStore) ensureAgentDefinitionSchema(ctx context.Context) error {
	const createStmt = `CREATE TABLE IF NOT EXISTS agent_definition (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		deleted_at INTEGER NOT NULL DEFAULT 0,
		provider TEXT NOT NULL DEFAULT 'codex',
		model TEXT NOT NULL DEFAULT '',
		system_prompt TEXT NOT NULL DEFAULT '',
		driver TEXT NOT NULL DEFAULT '',
		guest_image TEXT NOT NULL DEFAULT '',
		workspace_id TEXT NOT NULL DEFAULT '',
		env_json TEXT NOT NULL DEFAULT '[]',
		volumes_json TEXT NOT NULL DEFAULT '[]',
		config_json TEXT NOT NULL DEFAULT '{}',
		capset_ids TEXT NOT NULL DEFAULT '[]',
		skills TEXT NOT NULL DEFAULT '[]',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);`
	if _, err := s.db.ExecContext(ctx, createStmt); err != nil {
		return fmt.Errorf("create agent definition schema: %w", err)
	}
	if err := ensureColumn(ctx, s.db, "agent_definition", "capset_ids", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return fmt.Errorf("ensure agent definition capset_ids column: %w", err)
	}
	if err := ensureColumn(ctx, s.db, "agent_definition", "volumes_json", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return fmt.Errorf("ensure agent definition volumes_json column: %w", err)
	}
	if err := ensureColumn(ctx, s.db, "agent_definition", "skills", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return fmt.Errorf("ensure agent definition skills column: %w", err)
	}
	managedColumns := []struct {
		name       string
		definition string
	}{
		{name: "managed_project_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "managed_project_revision", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "managed_agent_name", definition: "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range managedColumns {
		if err := ensureColumn(ctx, s.db, "agent_definition", column.name, column.definition); err != nil {
			return fmt.Errorf("ensure agent definition managed column %s: %w", column.name, err)
		}
	}
	statements := []string{
		`CREATE INDEX IF NOT EXISTS idx_agent_definition_deleted_enabled ON agent_definition(deleted_at, enabled);`,
		`CREATE INDEX IF NOT EXISTS idx_agent_definition_workspace ON agent_definition(workspace_id);`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create agent definition index: %w", err)
		}
	}
	return nil
}

func (s *coreStore) EnsureAgentDefinitionSchema(ctx context.Context) error {
	return s.ensureAgentDefinitionSchema(ctx)
}

func (s *coreStore) tableColumnTypes(ctx context.Context, tableName string) (map[string]string, error) {
	return TableColumnTypes(ctx, s.db, tableName)
}

func (s *coreStore) TableColumnTypes(ctx context.Context, tableName string) (map[string]string, error) {
	return s.tableColumnTypes(ctx, tableName)
}

func (s *coreStore) tableExists(ctx context.Context, tableName string) (bool, error) {
	tableName = strings.TrimSpace(tableName)
	if tableName == "" {
		return false, fmt.Errorf("schema table name is required")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, tableName).Scan(&count); err != nil {
		return false, fmt.Errorf("query schema table %s: %w", tableName, err)
	}
	return count > 0, nil
}

func ensureColumn(ctx context.Context, db *sql.DB, table, column, definition string) error {
	columnTypes, err := TableColumnTypes(ctx, db, table)
	if err != nil {
		return err
	}
	if _, ok := columnTypes[column]; ok {
		return nil
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition)); err != nil {
		return err
	}
	return nil
}

func (s *coreStore) rebuildGlobalEnvTable(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin global env migration tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	updatedAtExpr := NormalizeSQLiteTimestampExpr("updated_at")
	statements := []string{
		`DROP TABLE IF EXISTS global_env_legacy;`,
		`ALTER TABLE global_env RENAME TO global_env_legacy;`,
		`CREATE TABLE global_env (
			name TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			secret INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
		);`,
		fmt.Sprintf(`INSERT INTO global_env(name, value, secret, updated_at)
			SELECT name, value, secret, %s FROM global_env_legacy;`, updatedAtExpr),
		`DROP TABLE global_env_legacy;`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate global env schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit global env migration tx: %w", err)
	}
	return nil
}

func (s *coreStore) RebuildGlobalEnvTable(ctx context.Context) error {
	return s.rebuildGlobalEnvTable(ctx)
}

func (s *coreStore) rebuildWorkspaceConfigTable(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin workspace config migration tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	createdAtExpr := NormalizeSQLiteTimestampExpr("created_at")
	updatedAtExpr := NormalizeSQLiteTimestampExpr("updated_at")
	statements := []string{
		`DROP TABLE IF EXISTS workspace_config_legacy;`,
		`ALTER TABLE workspace_config RENAME TO workspace_config_legacy;`,
		`CREATE TABLE workspace_config (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			config_json TEXT NOT NULL DEFAULT '{}',
			comment TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
		);`,
		fmt.Sprintf(`INSERT INTO workspace_config(id, name, type, config_json, comment, created_at, updated_at)
			SELECT id, name, type, config_json, comment, %s, %s FROM workspace_config_legacy;`, createdAtExpr, updatedAtExpr),
		`DROP TABLE workspace_config_legacy;`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate workspace config schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit workspace config migration tx: %w", err)
	}
	return nil
}

func (s *coreStore) RebuildWorkspaceConfigTable(ctx context.Context) error {
	return s.rebuildWorkspaceConfigTable(ctx)
}

func (s *coreStore) ListGlobalEnv(ctx context.Context) ([]SessionEnvVar, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, value, secret FROM global_env ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("query global env: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]SessionEnvVar, 0)
	for rows.Next() {
		var item SessionEnvVar
		var secret int
		if err := rows.Scan(&item.Name, &item.Value, &secret); err != nil {
			return nil, fmt.Errorf("scan global env: %w", err)
		}
		item.Secret = secret != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate global env: %w", err)
	}
	return items, nil
}

func (s *coreStore) ReplaceGlobalEnv(ctx context.Context, items []SessionEnvVar) ([]SessionEnvVar, error) {
	normalized := domain.NormalizeEnvItems(items)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin global env tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM global_env`); err != nil {
		return nil, fmt.Errorf("reset global env: %w", err)
	}
	for _, item := range normalized {
		if _, err := tx.ExecContext(ctx, `INSERT INTO global_env(name, value, secret, updated_at) VALUES(?, ?, ?, ?)`, item.Name, item.Value, BoolToInt(item.Secret), time.Now().UTC().Unix()); err != nil {
			return nil, fmt.Errorf("insert global env %s: %w", item.Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit global env tx: %w", err)
	}
	return normalized, nil
}

func (s *coreStore) ListWorkspaceConfigs(ctx context.Context) ([]WorkspaceConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, config_json, comment, created_at, updated_at FROM workspace_config ORDER BY name ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query workspace configs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]WorkspaceConfig, 0)
	for rows.Next() {
		item, err := ScanWorkspaceConfig(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace configs: %w", err)
	}
	return items, nil
}

func (s *coreStore) GetWorkspaceConfig(ctx context.Context, id string) (WorkspaceConfig, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, type, config_json, comment, created_at, updated_at FROM workspace_config WHERE id = ?`, strings.TrimSpace(id))
	item, err := ScanWorkspaceConfig(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			message := fmt.Sprintf("workspace config %s not found", strings.TrimSpace(id))
			return WorkspaceConfig{}, domain.ResourceError(domain.ErrNotFound, "workspace config", strings.TrimSpace(id), message, err)
		}
		return WorkspaceConfig{}, err
	}
	return item, nil
}

func (s *coreStore) CreateWorkspaceConfig(ctx context.Context, item WorkspaceConfig) (WorkspaceConfig, error) {
	normalized, err := NormalizeWorkspaceConfig(item, true)
	if err != nil {
		return WorkspaceConfig{}, err
	}
	now := time.Now().UTC()
	normalized.CreatedAt = now
	normalized.UpdatedAt = now
	if _, err := s.db.ExecContext(ctx, `INSERT INTO workspace_config(id, name, type, config_json, comment, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?)`, normalized.ID, normalized.Name, normalized.Type, normalized.ConfigJSON, normalized.Comment, normalized.CreatedAt.Unix(), normalized.UpdatedAt.Unix()); err != nil {
		return WorkspaceConfig{}, fmt.Errorf("insert workspace config %s: %w", normalized.ID, err)
	}
	return normalized, nil
}

func (s *coreStore) UpdateWorkspaceConfig(ctx context.Context, item WorkspaceConfig) (WorkspaceConfig, error) {
	normalized, err := NormalizeWorkspaceConfig(item, false)
	if err != nil {
		return WorkspaceConfig{}, err
	}
	existing, err := s.GetWorkspaceConfig(ctx, normalized.ID)
	if err != nil {
		return WorkspaceConfig{}, err
	}
	normalized.CreatedAt = existing.CreatedAt
	normalized.UpdatedAt = time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE workspace_config SET name = ?, type = ?, config_json = ?, comment = ?, updated_at = ? WHERE id = ?`, normalized.Name, normalized.Type, normalized.ConfigJSON, normalized.Comment, normalized.UpdatedAt.Unix(), normalized.ID)
	if err != nil {
		return WorkspaceConfig{}, fmt.Errorf("update workspace config %s: %w", normalized.ID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return WorkspaceConfig{}, domain.ResourceError(domain.ErrNotFound, "workspace config", normalized.ID, fmt.Sprintf("workspace config %s not found", normalized.ID), nil)
	}
	return normalized, nil
}

func (s *coreStore) DeleteWorkspaceConfig(ctx context.Context, id string) error {
	trimmedID := strings.TrimSpace(id)
	if trimmedID == "" {
		return fmt.Errorf("workspace config id is required")
	}
	if err := s.ensureWorkspaceNotReferencedByAgent(ctx, trimmedID); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM workspace_config WHERE id = ?`, trimmedID)
	if err != nil {
		return fmt.Errorf("delete workspace config %s: %w", trimmedID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.ResourceError(domain.ErrNotFound, "workspace config", trimmedID, fmt.Sprintf("workspace config %s not found", trimmedID), nil)
	}
	return nil
}

func (s *coreStore) CreateAgentDefinition(ctx context.Context, item domain.AgentDefinition) (domain.AgentDefinition, error) {
	normalized, err := domain.NormalizeAgentDefinition(item, true)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	now := time.Now().UTC()
	normalized.CreatedAt = now
	normalized.UpdatedAt = now
	normalized.DeletedAt = time.Time{}
	envJSON, err := EncodeAgentEnvJSON(normalized.EnvItems)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	volumesJSON, err := EncodeAgentVolumesJSON(normalized.Volumes)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	capsetIDsJSON, err := capabilities.EncodeCapsetIDs(normalized.CapsetIDs)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	skillsJSON, err := EncodeAgentSkillsJSON(normalized.Skills)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO agent_definition(
		id, name, description, enabled, deleted_at, provider, model, system_prompt, driver, guest_image, workspace_id, env_json, volumes_json, config_json, capset_ids, skills,
		managed_project_id, managed_project_revision, managed_agent_name, created_at, updated_at
	) VALUES(?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		normalized.ID, normalized.Name, normalized.Description, BoolToInt(normalized.Enabled), normalized.Provider, normalized.Model, normalized.SystemPrompt,
		normalized.Driver, normalized.GuestImage, normalized.WorkspaceID, envJSON, volumesJSON, normalized.ConfigJSON, capsetIDsJSON, skillsJSON,
		normalized.ManagedProjectID, normalized.ManagedProjectRevision, normalized.ManagedAgentName, normalized.CreatedAt.Unix(), normalized.UpdatedAt.Unix()); err != nil {
		return domain.AgentDefinition{}, fmt.Errorf("insert agent definition %s: %w", normalized.ID, err)
	}
	return normalized, nil
}

func (s *coreStore) UpdateAgentDefinition(ctx context.Context, item domain.AgentDefinition) (domain.AgentDefinition, error) {
	normalized, err := domain.NormalizeAgentDefinition(item, true)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	existing, err := s.GetAgentDefinition(ctx, normalized.ID)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	if normalized.ManagedProjectID == "" && normalized.ManagedAgentName == "" && normalized.ManagedProjectRevision == 0 {
		normalized.ManagedProjectID = existing.ManagedProjectID
		normalized.ManagedProjectRevision = existing.ManagedProjectRevision
		normalized.ManagedAgentName = existing.ManagedAgentName
	}
	normalized.CreatedAt = existing.CreatedAt
	normalized.UpdatedAt = time.Now().UTC()
	normalized.DeletedAt = time.Time{}
	envJSON, err := EncodeAgentEnvJSON(normalized.EnvItems)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	volumesJSON, err := EncodeAgentVolumesJSON(normalized.Volumes)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	capsetIDsJSON, err := capabilities.EncodeCapsetIDs(normalized.CapsetIDs)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	normalized.Skills = existing.Skills
	result, err := s.db.ExecContext(ctx, `UPDATE agent_definition SET
		name = ?, description = ?, enabled = ?, provider = ?, model = ?, system_prompt = ?, driver = ?, guest_image = ?, workspace_id = ?, env_json = ?,
		volumes_json = ?, config_json = ?, capset_ids = ?, managed_project_id = ?, managed_project_revision = ?, managed_agent_name = ?, updated_at = ?
		WHERE id = ? AND deleted_at = 0`,
		normalized.Name, normalized.Description, BoolToInt(normalized.Enabled), normalized.Provider, normalized.Model, normalized.SystemPrompt,
		normalized.Driver, normalized.GuestImage, normalized.WorkspaceID, envJSON, volumesJSON, normalized.ConfigJSON, capsetIDsJSON,
		normalized.ManagedProjectID, normalized.ManagedProjectRevision, normalized.ManagedAgentName, normalized.UpdatedAt.Unix(), normalized.ID)
	if err != nil {
		return domain.AgentDefinition{}, fmt.Errorf("update agent definition %s: %w", normalized.ID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.AgentDefinition{}, domain.ResourceError(domain.ErrNotFound, "agent definition", normalized.ID, fmt.Sprintf("agent definition %s not found", normalized.ID), nil)
	}
	return normalized, nil
}

func (s *coreStore) UpsertManagedAgentDefinition(ctx context.Context, item domain.AgentDefinition) (domain.AgentDefinition, error) {
	normalized, err := domain.NormalizeAgentDefinition(item, true)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	if normalized.ManagedProjectID == "" || normalized.ManagedAgentName == "" {
		return domain.AgentDefinition{}, fmt.Errorf("managed project id and managed agent name are required")
	}
	envJSON, err := EncodeAgentEnvJSON(normalized.EnvItems)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	volumesJSON, err := EncodeAgentVolumesJSON(normalized.Volumes)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	capsetIDsJSON, err := capabilities.EncodeCapsetIDs(normalized.CapsetIDs)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	skillsJSON, err := EncodeAgentSkillsJSON(normalized.Skills)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	now := time.Now().UTC()
	existing, found, err := s.getAgentDefinitionIfExists(ctx, normalized.ID, true)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	if found {
		normalized.CreatedAt = existing.CreatedAt
		normalized.UpdatedAt = now
		normalized.DeletedAt = time.Time{}
		result, err := s.db.ExecContext(ctx, `UPDATE agent_definition SET
			name = ?, description = ?, enabled = ?, deleted_at = 0, provider = ?, model = ?, system_prompt = ?, driver = ?, guest_image = ?, workspace_id = ?,
			env_json = ?, volumes_json = ?, config_json = ?, capset_ids = ?, skills = ?, managed_project_id = ?, managed_project_revision = ?, managed_agent_name = ?, updated_at = ?
			WHERE id = ?`,
			normalized.Name, normalized.Description, BoolToInt(normalized.Enabled), normalized.Provider, normalized.Model, normalized.SystemPrompt,
			normalized.Driver, normalized.GuestImage, normalized.WorkspaceID, envJSON, volumesJSON, normalized.ConfigJSON, capsetIDsJSON,
			skillsJSON, normalized.ManagedProjectID, normalized.ManagedProjectRevision, normalized.ManagedAgentName, normalized.UpdatedAt.Unix(), normalized.ID)
		if err != nil {
			return domain.AgentDefinition{}, fmt.Errorf("update managed agent definition %s: %w", normalized.ID, err)
		}
		if rows, _ := result.RowsAffected(); rows == 0 {
			return domain.AgentDefinition{}, domain.ResourceError(domain.ErrNotFound, "managed agent definition", normalized.ID, fmt.Sprintf("managed agent definition %s not found", normalized.ID), nil)
		}
		return s.GetAgentDefinition(ctx, normalized.ID)
	}

	normalized.CreatedAt = now
	normalized.UpdatedAt = now
	normalized.DeletedAt = time.Time{}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO agent_definition(
		id, name, description, enabled, deleted_at, provider, model, system_prompt, driver, guest_image, workspace_id, env_json, volumes_json, config_json, capset_ids, skills,
		managed_project_id, managed_project_revision, managed_agent_name, created_at, updated_at
	) VALUES(?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		normalized.ID, normalized.Name, normalized.Description, BoolToInt(normalized.Enabled), normalized.Provider, normalized.Model, normalized.SystemPrompt,
		normalized.Driver, normalized.GuestImage, normalized.WorkspaceID, envJSON, volumesJSON, normalized.ConfigJSON, capsetIDsJSON, skillsJSON,
		normalized.ManagedProjectID, normalized.ManagedProjectRevision, normalized.ManagedAgentName, normalized.CreatedAt.Unix(), normalized.UpdatedAt.Unix()); err != nil {
		return domain.AgentDefinition{}, fmt.Errorf("insert managed agent definition %s: %w", normalized.ID, err)
	}
	return normalized, nil
}

func (s *coreStore) GetAgentDefinition(ctx context.Context, id string) (domain.AgentDefinition, error) {
	return s.getAgentDefinition(ctx, id, false)
}

func (s *coreStore) GetAgentDefinitionIncludingDeleted(ctx context.Context, id string) (domain.AgentDefinition, error) {
	return s.getAgentDefinition(ctx, id, true)
}

func (s *coreStore) getAgentDefinition(ctx context.Context, id string, includeDeleted bool) (domain.AgentDefinition, error) {
	item, found, err := s.getAgentDefinitionIfExists(ctx, id, includeDeleted)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	if !found {
		trimmedID := strings.TrimSpace(id)
		return domain.AgentDefinition{}, domain.ResourceError(domain.ErrNotFound, "agent definition", trimmedID, fmt.Sprintf("agent definition %s not found", trimmedID), sql.ErrNoRows)
	}
	return item, nil
}

func (s *coreStore) getAgentDefinitionIfExists(ctx context.Context, id string, includeDeleted bool) (domain.AgentDefinition, bool, error) {
	where := "id = ? AND deleted_at = 0"
	if includeDeleted {
		where = "id = ?"
	}
	row := s.db.QueryRowContext(ctx, `SELECT id, name, description, enabled, deleted_at, provider, model, system_prompt, driver, guest_image, workspace_id, env_json, volumes_json, config_json, capset_ids, skills,
		managed_project_id, managed_project_revision, managed_agent_name, created_at, updated_at
		FROM agent_definition WHERE `+where, strings.TrimSpace(id))
	item, err := ScanAgentDefinition(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.AgentDefinition{}, false, nil
		}
		return domain.AgentDefinition{}, false, err
	}
	return item, true, nil
}

func (s *coreStore) GetAgentDefinitionIfExists(ctx context.Context, id string, includeDeleted bool) (domain.AgentDefinition, bool, error) {
	return s.getAgentDefinitionIfExists(ctx, id, includeDeleted)
}

func (s *coreStore) ListAgentDefinitions(ctx context.Context, options domain.AgentDefinitionListOptions) (domain.AgentDefinitionListResult, error) {
	limit := options.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := options.Offset
	if offset < 0 {
		offset = 0
	}
	query := strings.ToLower(strings.TrimSpace(options.Query))
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, enabled, deleted_at, provider, model, system_prompt, driver, guest_image, workspace_id, env_json, volumes_json, config_json, capset_ids, skills,
		managed_project_id, managed_project_revision, managed_agent_name, created_at, updated_at
		FROM agent_definition
		ORDER BY CASE WHEN deleted_at = 0 THEN 0 ELSE 1 END, updated_at DESC, created_at DESC, id ASC`)
	if err != nil {
		return domain.AgentDefinitionListResult{}, fmt.Errorf("query agent definitions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	matched := make([]domain.AgentDefinition, 0)
	for rows.Next() {
		item, err := ScanAgentDefinition(rows.Scan)
		if err != nil {
			return domain.AgentDefinitionListResult{}, err
		}
		if !options.IncludeDisabled && (!item.Enabled || !item.DeletedAt.IsZero()) {
			continue
		}
		if query != "" && !AgentMatchesQuery(item, query) {
			continue
		}
		matched = append(matched, item)
	}
	if err := rows.Err(); err != nil {
		return domain.AgentDefinitionListResult{}, fmt.Errorf("iterate agent definitions: %w", err)
	}
	total := len(matched)
	end := offset + limit
	if offset > total {
		offset = total
	}
	if end > total {
		end = total
	}
	page := matched[offset:end]
	return domain.AgentDefinitionListResult{
		Agents:     page,
		TotalCount: total,
		HasMore:    end < total,
		NextOffset: end,
	}, nil
}

func (s *coreStore) ListManagedAgentDefinitions(ctx context.Context, projectID string, includeDeleted bool) ([]domain.AgentDefinition, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	where := "managed_project_id = ? AND deleted_at = 0"
	if includeDeleted {
		where = "managed_project_id = ?"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, enabled, deleted_at, provider, model, system_prompt, driver, guest_image, workspace_id, env_json, volumes_json, config_json, capset_ids, skills,
		managed_project_id, managed_project_revision, managed_agent_name, created_at, updated_at
		FROM agent_definition WHERE `+where+` ORDER BY managed_agent_name ASC, id ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query managed agent definitions %s: %w", projectID, err)
	}
	defer func() { _ = rows.Close() }()
	var items []domain.AgentDefinition
	for rows.Next() {
		item, err := ScanAgentDefinition(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed agent definitions %s: %w", projectID, err)
	}
	return items, nil
}

func (s *coreStore) DeleteAgentDefinition(ctx context.Context, id string) error {
	trimmedID := strings.TrimSpace(id)
	if trimmedID == "" {
		return fmt.Errorf("agent definition id is required")
	}
	now := time.Now().UTC().Unix()
	result, err := s.db.ExecContext(ctx, `UPDATE agent_definition SET enabled = 0, deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at = 0`, now, now, trimmedID)
	if err != nil {
		return fmt.Errorf("delete agent definition %s: %w", trimmedID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.ResourceError(domain.ErrNotFound, "agent definition", trimmedID, fmt.Sprintf("agent definition %s not found", trimmedID), nil)
	}
	return nil
}

func (s *coreStore) SetAgentDefinitionEnabled(ctx context.Context, id string, enabled bool) (domain.AgentDefinition, error) {
	trimmedID := strings.TrimSpace(id)
	if trimmedID == "" {
		return domain.AgentDefinition{}, fmt.Errorf("agent definition id is required")
	}
	now := time.Now().UTC().Unix()
	result, err := s.db.ExecContext(ctx, `UPDATE agent_definition SET enabled = ?, updated_at = ? WHERE id = ? AND deleted_at = 0`, BoolToInt(enabled), now, trimmedID)
	if err != nil {
		return domain.AgentDefinition{}, fmt.Errorf("set agent definition enabled %s: %w", trimmedID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.AgentDefinition{}, domain.ResourceError(domain.ErrNotFound, "agent definition", trimmedID, fmt.Sprintf("agent definition %s not found", trimmedID), nil)
	}
	return s.GetAgentDefinition(ctx, trimmedID)
}

func (s *coreStore) ensureWorkspaceNotReferencedByAgent(ctx context.Context, workspaceID string) error {
	trimmedID := strings.TrimSpace(workspaceID)
	if trimmedID == "" {
		return nil
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_definition WHERE deleted_at = 0 AND workspace_id = ?`, trimmedID).Scan(&count); err != nil {
		return fmt.Errorf("query workspace agent references %s: %w", trimmedID, err)
	}
	if count > 0 {
		return domain.ResourceError(domain.ErrReferenced, "workspace config", trimmedID, fmt.Sprintf("workspace config %s is referenced by %d agent definition(s)", trimmedID, count), nil)
	}
	return nil
}
