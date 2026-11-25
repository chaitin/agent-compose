package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/globalenv"
	"github.com/chaitin/agent-compose/ent/dao/workspaceconfig"
	"github.com/chaitin/agent-compose/ent/helper"
	"github.com/chaitin/agent-compose/pkg/storage/dialect"
	"github.com/chaitin/agent-compose/pkg/storedtime"
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
	db     *sql.DB
	client *dao.Client
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
	db      *sql.DB
	dialect dialect.Dialect

	*coreStore
	*schedulerStore
	*eventStore
	*projectStore
	*llmStore
	*credentialStore
	*capabilityGatewayStore
	*volumeStore
}

// FromDB constructs a store over the SQLite dialect. Existing callers and
// tests keep this default; a PostgreSQL composition root uses
// FromDBWithDialect.
func FromDB(db *sql.DB) *ConfigStore {
	return FromDBWithDialect(db, dialect.SQLite())
}

func FromDBWithDialect(db *sql.DB, d dialect.Dialect) *ConfigStore {
	d = dialect.OrSQLite(d)
	client := newEntClient(db, d)
	return &ConfigStore{
		db:                     db,
		dialect:                d,
		coreStore:              &coreStore{db: db, client: client},
		schedulerStore:         &schedulerStore{db: db, client: client},
		eventStore:             &eventStore{db: db, dialect: d, client: client},
		projectStore:           &projectStore{db: db, dialect: d, client: client},
		llmStore:               &llmStore{db: db, client: client},
		credentialStore:        &credentialStore{db: db, client: client},
		capabilityGatewayStore: &capabilityGatewayStore{db: db, client: client},
		volumeStore:            &volumeStore{db: db, dialect: d, client: client},
	}
}

func (s *ConfigStore) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

func (s *coreStore) ListGlobalEnv(ctx context.Context) ([]domain.SandboxEnvVar, error) {
	entities, err := s.client.GlobalEnv.Query().
		Select(globalenv.FieldID, globalenv.FieldValue, globalenv.FieldSecret).
		Order(globalenv.ByID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query global env: %w", err)
	}
	items := make([]domain.SandboxEnvVar, 0, len(entities))
	for _, entity := range entities {
		items = append(items, domain.SandboxEnvVar{
			Name:   entity.ID,
			Value:  entity.Value,
			Secret: entity.Secret != 0,
		})
	}
	return items, nil
}

func (s *coreStore) ReplaceGlobalEnv(ctx context.Context, items []domain.SandboxEnvVar) ([]domain.SandboxEnvVar, error) {
	normalized := domain.NormalizeEnvItems(items)
	err := helper.WithTx(ctx, s.client, func(tx *dao.Tx) error {
		client := tx.Client()
		if _, err := client.GlobalEnv.Delete().Exec(ctx); err != nil {
			return fmt.Errorf("reset global env: %w", err)
		}
		for _, item := range normalized {
			if _, err := client.GlobalEnv.Create().
				SetID(item.Name).
				SetValue(item.Value).
				SetSecret(BoolToInt(item.Secret)).
				SetUpdatedAt(time.Now().UTC().Unix()).
				Save(ctx); err != nil {
				return fmt.Errorf("insert global env %s: %w", item.Name, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

func (s *coreStore) ListWorkspaceConfigs(ctx context.Context) ([]WorkspaceConfig, error) {
	entities, err := s.client.WorkspaceConfig.Query().
		Order(workspaceconfig.ByName(), workspaceconfig.ByID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query workspace configs: %w", err)
	}
	items := make([]WorkspaceConfig, 0, len(entities))
	for _, entity := range entities {
		items = append(items, workspaceConfigFromEntity(entity))
	}
	return items, nil
}

func (s *coreStore) GetWorkspaceConfig(ctx context.Context, id string) (WorkspaceConfig, error) {
	trimmedID := strings.TrimSpace(id)
	entity, err := s.client.WorkspaceConfig.Query().
		Where(workspaceconfig.IDEQ(trimmedID)).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			message := fmt.Sprintf("workspace config %s not found", trimmedID)
			return WorkspaceConfig{}, domain.ResourceError(domain.ErrNotFound, "workspace config", trimmedID, message, sql.ErrNoRows)
		}
		return WorkspaceConfig{}, err
	}
	return workspaceConfigFromEntity(entity), nil
}

func (s *coreStore) CreateWorkspaceConfig(ctx context.Context, item WorkspaceConfig) (WorkspaceConfig, error) {
	normalized, err := NormalizeWorkspaceConfig(item, true)
	if err != nil {
		return WorkspaceConfig{}, err
	}
	now := time.Now().UTC()
	normalized.CreatedAt = now
	normalized.UpdatedAt = now
	if _, err := s.client.WorkspaceConfig.Create().
		SetID(normalized.ID).
		SetName(normalized.Name).
		SetType(normalized.Type).
		SetConfigJSON(normalized.ConfigJSON).
		SetComment(normalized.Comment).
		SetCreatedAt(normalized.CreatedAt.Unix()).
		SetUpdatedAt(normalized.UpdatedAt.Unix()).
		Save(ctx); err != nil {
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
	affected, err := s.client.WorkspaceConfig.Update().
		Where(workspaceconfig.IDEQ(normalized.ID)).
		SetName(normalized.Name).
		SetType(normalized.Type).
		SetConfigJSON(normalized.ConfigJSON).
		SetComment(normalized.Comment).
		SetUpdatedAt(normalized.UpdatedAt.Unix()).
		Save(ctx)
	if err != nil {
		return WorkspaceConfig{}, fmt.Errorf("update workspace config %s: %w", normalized.ID, err)
	}
	if affected == 0 {
		return WorkspaceConfig{}, domain.ResourceError(domain.ErrNotFound, "workspace config", normalized.ID, fmt.Sprintf("workspace config %s not found", normalized.ID), nil)
	}
	return normalized, nil
}

func (s *coreStore) DeleteWorkspaceConfig(ctx context.Context, id string) error {
	trimmedID := strings.TrimSpace(id)
	if trimmedID == "" {
		return fmt.Errorf("workspace config id is required")
	}
	affected, err := s.client.WorkspaceConfig.Delete().
		Where(workspaceconfig.IDEQ(trimmedID)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete workspace config %s: %w", trimmedID, err)
	}
	if affected == 0 {
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

// workspaceConfigFromEntity maps one workspace_config row to its domain shape.
func workspaceConfigFromEntity(entity *dao.WorkspaceConfig) WorkspaceConfig {
	return WorkspaceConfig{
		ID:         entity.ID,
		Name:       entity.Name,
		Type:       entity.Type,
		ConfigJSON: entity.ConfigJSON,
		Comment:    entity.Comment,
		CreatedAt:  storedtime.ParseStoredTime(entity.CreatedAt),
		UpdatedAt:  storedtime.ParseStoredTime(entity.UpdatedAt),
	}
}
