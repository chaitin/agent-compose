package agentcompose

import (
	"context"
	"database/sql"

	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/storage"

	"github.com/samber/do/v2"
)

type Store = storage.Store
type ConfigStore = storage.ConfigStore
type CapabilityGatewaySettings = storage.CapabilityGatewaySettings
type ProjectRecord = storage.ProjectRecord
type ProjectRevisionRecord = storage.ProjectRevisionRecord
type ProjectAgentRecord = storage.ProjectAgentRecord
type ProjectSchedulerRecord = storage.ProjectSchedulerRecord
type ProjectRunRecord = storage.ProjectRunRecord
type ProjectListOptions = storage.ProjectListOptions
type ProjectRunListOptions = storage.ProjectRunListOptions
type ProjectListResult = storage.ProjectListResult
type ProjectSessionRelationFilter = storage.ProjectSessionRelationFilter
type ProjectSessionStatus = storage.ProjectSessionStatus

const (
	ProjectRunStatusPending   = storage.ProjectRunStatusPending
	ProjectRunStatusRunning   = storage.ProjectRunStatusRunning
	ProjectRunStatusSucceeded = storage.ProjectRunStatusSucceeded
	ProjectRunStatusFailed    = storage.ProjectRunStatusFailed
	ProjectRunStatusCanceled  = storage.ProjectRunStatusCanceled

	ProjectRunSourceManual    = storage.ProjectRunSourceManual
	ProjectRunSourceScheduler = storage.ProjectRunSourceScheduler
	ProjectRunSourceAPI       = storage.ProjectRunSourceAPI

	storedUnixMillisecondThreshold = storage.StoredUnixMillisecondThreshold
	llmFacadeTokenRetention        = storage.LLMFacadeTokenRetention
	defaultSessionListLimit        = storage.DefaultSessionListLimit
)

func NewStore(di do.Injector) (*Store, error) {
	return storage.NewStore(di)
}

func NewStoreFromConfig(config *appconfig.Config) (*Store, error) {
	return storage.NewStoreFromConfig(config)
}

func NewConfigStore(di do.Injector) (*ConfigStore, error) {
	return storage.NewConfigStore(di)
}

func NewConfigStoreFromConfig(config *appconfig.Config) (*ConfigStore, error) {
	return storage.NewConfigStoreFromConfig(config)
}

func NewConfigStoreFromDB(db *sql.DB) *ConfigStore {
	return storage.NewConfigStoreFromDB(db)
}

func StableProjectID(name, sourcePath string) (string, error) {
	return storage.StableProjectID(name, sourcePath)
}

func StableManagedAgentID(projectID, agentName string) (string, error) {
	return storage.StableManagedAgentID(projectID, agentName)
}

func StableProjectSchedulerID(projectID, agentName, schedulerName string) (string, error) {
	return storage.StableProjectSchedulerID(projectID, agentName, schedulerName)
}

func StableManagedLoaderID(projectID, agentName, schedulerName string) (string, error) {
	return storage.StableManagedLoaderID(projectID, agentName, schedulerName)
}

func StableManagedTriggerID(projectID, agentName, schedulerName, triggerName string, triggerIndex int) (string, error) {
	return storage.StableManagedTriggerID(projectID, agentName, schedulerName, triggerName, triggerIndex)
}

func StableProjectRunID(projectID, agentName, source, idempotencyKey string) (string, error) {
	return storage.StableProjectRunID(projectID, agentName, source, idempotencyKey)
}

func StableReadableID(prefix, readable, seed string) string {
	return storage.StableReadableID(prefix, readable, seed)
}

func NewProjectRecordFromSpec(spec *compose.NormalizedProjectSpec, sourcePath string) (ProjectRecord, error) {
	return storage.NewProjectRecordFromSpec(spec, sourcePath)
}

func NewProjectAgentRecordFromSpec(projectID string, revision int64, agent compose.NormalizedAgentSpec) (ProjectAgentRecord, error) {
	return storage.NewProjectAgentRecordFromSpec(projectID, revision, agent)
}

func NewProjectSchedulerRecordFromSpec(projectID string, revision int64, agent compose.NormalizedAgentSpec) (ProjectSchedulerRecord, bool, error) {
	return storage.NewProjectSchedulerRecordFromSpec(projectID, revision, agent)
}

func ListProjectSessionStatuses(ctx context.Context, configDB *ConfigStore, store *Store, filter ProjectSessionRelationFilter) ([]ProjectSessionStatus, error) {
	return storage.ListProjectSessionStatuses(ctx, configDB, store, filter)
}

func sessionTypeFromTriggerSource(value string) string {
	return storage.SessionTypeFromTriggerSource(value)
}

func normalizeSessionTriggerSource(value string, tags []SessionTag) string {
	return storage.NormalizeSessionTriggerSource(value, tags)
}

func sessionMatchesListOptions(session *Session, options SessionListOptions) bool {
	return storage.SessionMatchesListOptions(session, options)
}

func normalizeSessionListBounds(offset, limit int) (int, int) {
	return storage.NormalizeSessionListBounds(offset, limit)
}

func paginateSessions(items []*Session, offset, limit int) []*Session {
	return storage.PaginateSessions(items, offset, limit)
}

func normalizeWorkspaceConfig(item WorkspaceConfig, assignID bool) (WorkspaceConfig, error) {
	return storage.NormalizeWorkspaceConfig(item, assignID)
}

func normalizeTopicEventRecord(item TopicEventRecord, assignID bool) (TopicEventRecord, error) {
	return storage.NormalizeTopicEventRecord(item, assignID)
}

func webhookSourceTopicMatches(topic, topicPrefix string) bool {
	return storage.WebhookSourceTopicMatches(topic, topicPrefix)
}
