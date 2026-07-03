package agentcompose

import (
	"context"
	"database/sql"

	"github.com/samber/do/v2"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/configstore"
)

const storedUnixMillisecondThreshold int64 = configstore.StoredUnixMillisecondThreshold

type ConfigStore struct {
	*configstore.ConfigStore
	db *sql.DB
}

type (
	ProjectRecord          = domain.ProjectRecord
	ProjectRevisionRecord  = domain.ProjectRevisionRecord
	ProjectAgentRecord     = domain.ProjectAgentRecord
	ProjectSchedulerRecord = domain.ProjectSchedulerRecord
	ProjectRunRecord       = domain.ProjectRunRecord
	ProjectListOptions     = domain.ProjectListOptions
	ProjectRunListOptions  = domain.ProjectRunListOptions
	ProjectListResult      = domain.ProjectListResult
)

func NewConfigStore(di do.Injector) (*ConfigStore, error) {
	inner, err := configstore.NewConfigStore(di)
	if err != nil {
		return nil, err
	}
	return &ConfigStore{ConfigStore: inner, db: inner.DB()}, nil
}

func (s *ConfigStore) inner() *configstore.ConfigStore {
	if s == nil {
		return nil
	}
	if s.ConfigStore == nil && s.db != nil {
		s.ConfigStore = configstore.FromDB(s.db)
	}
	if s.db == nil && s.ConfigStore != nil {
		s.db = s.DB()
	}
	return s.ConfigStore
}

func (s *ConfigStore) initSchema(ctx context.Context) error {
	return s.inner().InitSchema(ctx)
}

func (s *ConfigStore) tableColumnTypes(ctx context.Context, tableName string) (map[string]string, error) {
	return s.inner().TableColumnTypes(ctx, tableName)
}

func (s *ConfigStore) ensureGlobalEnvSchema(ctx context.Context) error {
	return s.inner().EnsureGlobalEnvSchema(ctx)
}

func (s *ConfigStore) ensureWorkspaceConfigSchema(ctx context.Context) error {
	return s.inner().EnsureWorkspaceConfigSchema(ctx)
}

func (s *ConfigStore) ensureAgentDefinitionSchema(ctx context.Context) error {
	return s.inner().EnsureAgentDefinitionSchema(ctx)
}

func (s *ConfigStore) ensureCapabilityGatewaySchema(ctx context.Context) error {
	return s.inner().EnsureCapabilityGatewaySchema(ctx)
}

func (s *ConfigStore) ensureLoaderSchema(ctx context.Context) error {
	return s.inner().EnsureLoaderSchema(ctx)
}

func (s *ConfigStore) ensureEventSchema(ctx context.Context) error {
	return s.inner().EnsureEventSchema(ctx)
}

func (s *ConfigStore) rebuildGlobalEnvTable(ctx context.Context) error {
	return s.inner().RebuildGlobalEnvTable(ctx)
}

func (s *ConfigStore) rebuildWorkspaceConfigTable(ctx context.Context) error {
	return s.inner().RebuildWorkspaceConfigTable(ctx)
}

func (s *ConfigStore) getProject(ctx context.Context, projectID string, includeRemoved bool) (ProjectRecord, bool, error) {
	return s.inner().GetProjectIfExists(ctx, projectID, includeRemoved)
}
