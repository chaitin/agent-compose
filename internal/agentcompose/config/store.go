package config

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	agentworkspace "agent-compose/internal/agentcompose/workspace"

	"github.com/google/uuid"
)

type PersistenceStore struct {
	db *sql.DB
}

func NewPersistenceStore(db *sql.DB) *PersistenceStore {
	return &PersistenceStore{db: db}
}

func (s *PersistenceStore) EnsureGlobalEnvSchema(ctx context.Context) error {
	const createStmt = `CREATE TABLE IF NOT EXISTS global_env (
		name TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		secret INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
	);`
	if _, err := s.db.ExecContext(ctx, createStmt); err != nil {
		return fmt.Errorf("create global env schema: %w", err)
	}
	columnTypes, err := TableColumnTypes(ctx, s.db, "global_env")
	if err != nil {
		return err
	}
	if isIntegerColumnType(columnTypes["updated_at"]) {
		return nil
	}
	return s.rebuildGlobalEnvTable(ctx)
}

func (s *PersistenceStore) EnsureWorkspaceConfigSchema(ctx context.Context) error {
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
	columnTypes, err := TableColumnTypes(ctx, s.db, "workspace_config")
	if err != nil {
		return err
	}
	if isIntegerColumnType(columnTypes["created_at"]) && isIntegerColumnType(columnTypes["updated_at"]) {
		return nil
	}
	return s.rebuildWorkspaceConfigTable(ctx)
}

func (s *PersistenceStore) rebuildGlobalEnvTable(ctx context.Context) error {
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

func (s *PersistenceStore) rebuildWorkspaceConfigTable(ctx context.Context) error {
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

func (s *PersistenceStore) ListGlobalEnv(ctx context.Context) ([]EnvVar, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, value, secret FROM global_env ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("query global env: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]EnvVar, 0)
	for rows.Next() {
		var item EnvVar
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

func (s *PersistenceStore) ReplaceGlobalEnv(ctx context.Context, items []EnvVar) ([]EnvVar, error) {
	normalized := NormalizeEnvItems(items)
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

func (s *PersistenceStore) ListWorkspaceConfigs(ctx context.Context) ([]agentworkspace.Config, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, config_json, comment, created_at, updated_at FROM workspace_config ORDER BY name ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query workspace configs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]agentworkspace.Config, 0)
	for rows.Next() {
		item, err := scanWorkspaceConfig(rows.Scan)
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

func (s *PersistenceStore) GetWorkspaceConfig(ctx context.Context, id string) (agentworkspace.Config, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, type, config_json, comment, created_at, updated_at FROM workspace_config WHERE id = ?`, strings.TrimSpace(id))
	item, err := scanWorkspaceConfig(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return agentworkspace.Config{}, fmt.Errorf("workspace config %s not found: %w", strings.TrimSpace(id), err)
		}
		return agentworkspace.Config{}, err
	}
	return item, nil
}

func (s *PersistenceStore) CreateWorkspaceConfig(ctx context.Context, item agentworkspace.Config) (agentworkspace.Config, error) {
	normalized, err := NormalizeWorkspaceConfig(item, true)
	if err != nil {
		return agentworkspace.Config{}, err
	}
	now := time.Now().UTC()
	normalized.CreatedAt = now
	normalized.UpdatedAt = now
	if _, err := s.db.ExecContext(ctx, `INSERT INTO workspace_config(id, name, type, config_json, comment, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?)`, normalized.ID, normalized.Name, normalized.Type, normalized.ConfigJSON, normalized.Comment, normalized.CreatedAt.Unix(), normalized.UpdatedAt.Unix()); err != nil {
		return agentworkspace.Config{}, fmt.Errorf("insert workspace config %s: %w", normalized.ID, err)
	}
	return normalized, nil
}

func (s *PersistenceStore) UpdateWorkspaceConfig(ctx context.Context, item agentworkspace.Config) (agentworkspace.Config, error) {
	normalized, err := NormalizeWorkspaceConfig(item, false)
	if err != nil {
		return agentworkspace.Config{}, err
	}
	existing, err := s.GetWorkspaceConfig(ctx, normalized.ID)
	if err != nil {
		return agentworkspace.Config{}, err
	}
	normalized.CreatedAt = existing.CreatedAt
	normalized.UpdatedAt = time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE workspace_config SET name = ?, type = ?, config_json = ?, comment = ?, updated_at = ? WHERE id = ?`, normalized.Name, normalized.Type, normalized.ConfigJSON, normalized.Comment, normalized.UpdatedAt.Unix(), normalized.ID)
	if err != nil {
		return agentworkspace.Config{}, fmt.Errorf("update workspace config %s: %w", normalized.ID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return agentworkspace.Config{}, fmt.Errorf("workspace config %s not found", normalized.ID)
	}
	return normalized, nil
}

func (s *PersistenceStore) DeleteWorkspaceConfig(ctx context.Context, id string) error {
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
		return fmt.Errorf("workspace config %s not found", trimmedID)
	}
	return nil
}

func (s *PersistenceStore) ensureWorkspaceNotReferencedByAgent(ctx context.Context, workspaceID string) error {
	trimmedID := strings.TrimSpace(workspaceID)
	if trimmedID == "" {
		return nil
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_definition WHERE deleted_at = 0 AND workspace_id = ?`, trimmedID).Scan(&count); err != nil {
		return fmt.Errorf("query workspace agent references %s: %w", trimmedID, err)
	}
	if count > 0 {
		return fmt.Errorf("workspace config %s is referenced by %d agent definition(s)", trimmedID, count)
	}
	return nil
}

func NormalizeWorkspaceConfig(item agentworkspace.Config, assignID bool) (agentworkspace.Config, error) {
	item.ID = strings.TrimSpace(item.ID)
	item.Name = strings.TrimSpace(item.Name)
	item.Type = strings.ToLower(strings.TrimSpace(item.Type))
	item.ConfigJSON = strings.TrimSpace(item.ConfigJSON)
	item.Comment = strings.TrimSpace(item.Comment)
	if assignID && item.ID == "" {
		item.ID = uuid.NewString()
	}
	if item.ID == "" {
		return agentworkspace.Config{}, fmt.Errorf("workspace config id is required")
	}
	if item.Name == "" {
		return agentworkspace.Config{}, fmt.Errorf("workspace config name is required")
	}
	if item.Type == "" {
		return agentworkspace.Config{}, fmt.Errorf("workspace config type is required")
	}
	if item.Type != "git" && item.Type != "file" {
		return agentworkspace.Config{}, fmt.Errorf("unsupported workspace config type %q", item.Type)
	}
	if item.ConfigJSON == "" {
		item.ConfigJSON = "{}"
	}
	return item, nil
}

func scanWorkspaceConfig(scan func(dest ...any) error) (agentworkspace.Config, error) {
	var item agentworkspace.Config
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(&item.ID, &item.Name, &item.Type, &item.ConfigJSON, &item.Comment, &createdAtRaw, &updatedAtRaw); err != nil {
		return agentworkspace.Config{}, fmt.Errorf("scan workspace config: %w", err)
	}
	item.CreatedAt = ParseStoredTime(createdAtRaw)
	item.UpdatedAt = ParseStoredTime(updatedAtRaw)
	return item, nil
}

func isIntegerColumnType(columnType string) bool {
	return strings.Contains(strings.ToUpper(strings.TrimSpace(columnType)), "INT")
}
