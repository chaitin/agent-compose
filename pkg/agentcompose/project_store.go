package agentcompose

import (
	"context"

	projectdomain "agent-compose/internal/agentcompose/project"
	"agent-compose/pkg/agentcompose/domain"
	"agent-compose/pkg/compose"
)

const (
	ProjectRunStatusPending   = projectdomain.ProjectRunStatusPending
	ProjectRunStatusRunning   = projectdomain.ProjectRunStatusRunning
	ProjectRunStatusSucceeded = projectdomain.ProjectRunStatusSucceeded
	ProjectRunStatusFailed    = projectdomain.ProjectRunStatusFailed
	ProjectRunStatusCanceled  = projectdomain.ProjectRunStatusCanceled

	ProjectRunSourceManual    = projectdomain.ProjectRunSourceManual
	ProjectRunSourceScheduler = projectdomain.ProjectRunSourceScheduler
	ProjectRunSourceAPI       = projectdomain.ProjectRunSourceAPI
)

type ProjectRecord = projectdomain.ProjectRecord
type ProjectRevisionRecord = projectdomain.ProjectRevisionRecord
type ProjectAgentRecord = projectdomain.ProjectAgentRecord
type ProjectSchedulerRecord = projectdomain.ProjectSchedulerRecord
type ProjectRunRecord = projectdomain.ProjectRunRecord
type ProjectListOptions = projectdomain.ProjectListOptions
type ProjectRunListOptions = projectdomain.ProjectRunListOptions
type ProjectListResult = projectdomain.ProjectListResult

func StableProjectID(name, sourcePath string) (string, error) {
	return projectdomain.StableProjectID(name, sourcePath)
}

func StableManagedAgentID(projectID, agentName string) (string, error) {
	return projectdomain.StableManagedAgentID(projectID, agentName)
}

func StableProjectSchedulerID(projectID, agentName, schedulerName string) (string, error) {
	return projectdomain.StableProjectSchedulerID(projectID, agentName, schedulerName)
}

func StableManagedLoaderID(projectID, agentName, schedulerName string) (string, error) {
	return projectdomain.StableManagedLoaderID(projectID, agentName, schedulerName)
}

func StableManagedTriggerID(projectID, agentName, schedulerName, triggerName string, triggerIndex int) (string, error) {
	return projectdomain.StableManagedTriggerID(projectID, agentName, schedulerName, triggerName, triggerIndex)
}

func StableProjectRunID(projectID, agentName, source, idempotencyKey string) (string, error) {
	return projectdomain.StableProjectRunID(projectID, agentName, source, idempotencyKey)
}

func NewProjectRecordFromSpec(spec *compose.NormalizedProjectSpec, sourcePath string) (ProjectRecord, error) {
	return projectdomain.NewProjectRecordFromSpec(spec, sourcePath)
}

func NewProjectAgentRecordFromSpec(projectID string, revision int64, agent compose.NormalizedAgentSpec) (ProjectAgentRecord, error) {
	return projectdomain.NewProjectAgentRecordFromSpec(projectID, revision, agent)
}

func NewProjectSchedulerRecordFromSpec(projectID string, revision int64, agent compose.NormalizedAgentSpec) (ProjectSchedulerRecord, bool, error) {
	return projectdomain.NewProjectSchedulerRecordFromSpec(projectID, revision, agent)
}

func (s *ConfigStore) projectStore() *projectdomain.Store {
	return projectdomain.NewStore(s.db)
}

func (s *ConfigStore) UpsertProject(ctx context.Context, project ProjectRecord) (ProjectRecord, error) {
	return s.projectStore().UpsertProject(ctx, project)
}

func (s *ConfigStore) SaveProjectRevision(ctx context.Context, revision ProjectRevisionRecord) (ProjectRevisionRecord, bool, error) {
	return s.projectStore().SaveProjectRevision(ctx, revision)
}

func (s *ConfigStore) GetProject(ctx context.Context, projectID string) (ProjectRecord, error) {
	return s.projectStore().GetProject(ctx, projectID)
}

func (s *ConfigStore) ListProjects(ctx context.Context, options ProjectListOptions) (ProjectListResult, error) {
	return s.projectStore().ListProjects(ctx, options)
}

func (s *ConfigStore) GetProjectRevision(ctx context.Context, projectID string, revision int64) (ProjectRevisionRecord, error) {
	return s.projectStore().GetProjectRevision(ctx, projectID, revision)
}

func (s *ConfigStore) UpsertProjectAgent(ctx context.Context, agent ProjectAgentRecord) (ProjectAgentRecord, error) {
	return s.projectStore().UpsertProjectAgent(ctx, agent)
}

func (s *ConfigStore) GetProjectAgent(ctx context.Context, projectID, agentName string) (ProjectAgentRecord, error) {
	return s.projectStore().GetProjectAgent(ctx, projectID, agentName)
}

func (s *ConfigStore) ListProjectAgents(ctx context.Context, projectID string) ([]ProjectAgentRecord, error) {
	return s.projectStore().ListProjectAgents(ctx, projectID)
}

func (s *ConfigStore) UpsertProjectScheduler(ctx context.Context, scheduler ProjectSchedulerRecord) (ProjectSchedulerRecord, error) {
	return s.projectStore().UpsertProjectScheduler(ctx, scheduler)
}

func (s *ConfigStore) GetProjectScheduler(ctx context.Context, projectID, schedulerID string) (ProjectSchedulerRecord, error) {
	return s.projectStore().GetProjectScheduler(ctx, projectID, schedulerID)
}

func (s *ConfigStore) SetProjectSchedulerEnabled(ctx context.Context, projectID, schedulerID string, enabled bool) (ProjectSchedulerRecord, error) {
	return s.projectStore().SetProjectSchedulerEnabled(ctx, projectID, schedulerID, enabled)
}

func (s *ConfigStore) ListProjectSchedulers(ctx context.Context, projectID string) ([]ProjectSchedulerRecord, error) {
	return s.projectStore().ListProjectSchedulers(ctx, projectID)
}

func (s *ConfigStore) CreateProjectRun(ctx context.Context, run ProjectRunRecord) (ProjectRunRecord, error) {
	return s.projectStore().CreateProjectRun(ctx, run)
}

func (s *ConfigStore) UpdateProjectRun(ctx context.Context, run ProjectRunRecord) (ProjectRunRecord, error) {
	return s.projectStore().UpdateProjectRun(ctx, run)
}

func (s *ConfigStore) GetProjectRun(ctx context.Context, runID string) (ProjectRunRecord, error) {
	return s.projectStore().GetProjectRun(ctx, runID)
}

func (s *ConfigStore) ListProjectRuns(ctx context.Context, projectID string, limit int) ([]ProjectRunRecord, error) {
	return s.projectStore().ListProjectRuns(ctx, projectID, limit)
}

func (s *ConfigStore) ListProjectRunsByOptions(ctx context.Context, options ProjectRunListOptions) ([]ProjectRunRecord, error) {
	return s.projectStore().ListProjectRunsByOptions(ctx, options)
}

func (s *ConfigStore) GetAgentDefinitionIfExists(ctx context.Context, id string, includeDeleted bool) (AgentDefinition, bool, error) {
	return s.getAgentDefinitionIfExists(ctx, id, includeDeleted)
}

func (s *ConfigStore) getProject(ctx context.Context, projectID string, includeRemoved bool) (ProjectRecord, bool, error) {
	if includeRemoved {
		return s.projectStore().GetProjectIncludingRemoved(ctx, projectID)
	}
	project, err := s.GetProject(ctx, projectID)
	if err != nil {
		return ProjectRecord{}, false, err
	}
	return project, true, nil
}

func stableReadableID(prefix, readable, seed string) string {
	return domain.StableReadableID(prefix, readable, seed)
}

func isProjectStableIdentifier(value string) bool {
	return domain.IsProjectStableIdentifier(value)
}

func normalizeProjectRunStatus(status string) string {
	return projectdomain.NormalizeProjectRunStatus(status)
}

func scanProjectRun(scan func(dest ...any) error) (ProjectRunRecord, error) {
	return projectdomain.ScanProjectRun(scan)
}

func selectProjectRunSQL() string {
	return projectdomain.SelectProjectRunSQL()
}
