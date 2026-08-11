package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
)

type (
	WorkspaceConfig        = domain.WorkspaceConfig
	Scheduler              = domain.Scheduler
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
// configs.
type coreStore struct {
	db *sql.DB
}

// ConfigStore is the composite persistence facade over DATA_ROOT/data.db. Each
// domain lives on its own sub-store sharing the same *sql.DB; embedding
// promotes every domain method onto ConfigStore, so callers and the domain
// packages' consumer interfaces are unaffected by the internal split.
//
// ConfigStore must be constructed via FromDB, which wires the embedded
// sub-stores. The zero value (or a struct literal) leaves them nil and every
// promoted method would panic; the sub-store types are unexported to keep direct
// construction confined to this package.
type ConfigStore struct {
	db *sql.DB

	*coreStore
	*schedulerStore
	*eventStore
	*projectStore
	*llmStore
	*capabilityGatewayStore
	*volumeStore
}

func FromDB(db *sql.DB) *ConfigStore {
	return &ConfigStore{
		db:                     db,
		coreStore:              &coreStore{db: db},
		schedulerStore:         &schedulerStore{db: db},
		eventStore:             &eventStore{db: db},
		projectStore:           &projectStore{db: db},
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

func (s *coreStore) ListGlobalEnv(ctx context.Context) ([]domain.SandboxEnvVar, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, value, secret FROM global_env ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("query global env: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]domain.SandboxEnvVar, 0)
	for rows.Next() {
		var item domain.SandboxEnvVar
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

func (s *coreStore) ReplaceGlobalEnv(ctx context.Context, items []domain.SandboxEnvVar) ([]domain.SandboxEnvVar, error) {
	credentials := llms.ResolveEnvDefaultProviderCredentials(items)
	return s.replaceGlobalEnv(ctx, items, &credentials)
}

func (s *coreStore) ReplaceGlobalEnvWithProviderCredentials(ctx context.Context, items []domain.SandboxEnvVar, credentials llms.EnvDefaultProviderCredentials) ([]domain.SandboxEnvVar, error) {
	return s.replaceGlobalEnv(ctx, items, &credentials)
}

func (s *coreStore) replaceGlobalEnv(ctx context.Context, items []domain.SandboxEnvVar, credentials *llms.EnvDefaultProviderCredentials) ([]domain.SandboxEnvVar, error) {
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
	if err := syncEnvDefaultProviderCredentials(ctx, tx, credentials); err != nil {
		return nil, err
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
	result, err := s.db.ExecContext(ctx, `DELETE FROM workspace_config WHERE id = ?`, trimmedID)
	if err != nil {
		return fmt.Errorf("delete workspace config %s: %w", trimmedID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.ResourceError(domain.ErrNotFound, "workspace config", trimmedID, fmt.Sprintf("workspace config %s not found", trimmedID), nil)
	}
	return nil
}

func (s *coreStore) GetAgentDefinition(ctx context.Context, id string) (domain.AgentDefinition, error) {
	return s.loadRevisionAgentDefinition(ctx, id)
}

func (s *coreStore) GetAgentDefinitionIncludingDeleted(ctx context.Context, id string) (domain.AgentDefinition, error) {
	return s.loadRevisionAgentDefinition(ctx, id)
}

func (s *coreStore) GetAgentDefinitionIfExists(ctx context.Context, id string, includeDeleted bool) (domain.AgentDefinition, bool, error) {
	item, err := s.loadRevisionAgentDefinition(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
			return domain.AgentDefinition{}, false, nil
		}
		return domain.AgentDefinition{}, false, err
	}
	return item, true, nil
}
