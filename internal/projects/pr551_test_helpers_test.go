package projects

import (
	"context"
	"database/sql"

	"github.com/chaitin/agent-compose/pkg/images"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// pr551NoopImagesBackend satisfies images.Backend and reports every image as
// present so patch/apply flows can proceed past image ensuring in tests.
type pr551NoopImagesBackend struct{}

func (pr551NoopImagesBackend) ListImages(context.Context, images.ListRequest) (images.ListResult, error) {
	return images.ListResult{}, nil
}

func (pr551NoopImagesBackend) PullImage(context.Context, images.PullRequest) (images.PullResult, error) {
	return images.PullResult{}, nil
}

func (pr551NoopImagesBackend) InspectImage(context.Context, images.InspectRequest) (images.InspectResult, error) {
	return images.InspectResult{
		Image: &agentcomposev2.Image{ImageRef: "guest:latest"},
	}, nil
}

func (pr551NoopImagesBackend) RemoveImage(context.Context, images.RemoveRequest) (images.RemoveResult, error) {
	return images.RemoveResult{}, nil
}

// pr551NoopVolumeManager satisfies projects.VolumeManager with no-op behavior.
type pr551NoopVolumeManager struct{}

func (pr551NoopVolumeManager) Ensure(context.Context, domain.VolumeRecord) (domain.VolumeRecord, bool, error) {
	return domain.VolumeRecord{}, false, nil
}

func (pr551NoopVolumeManager) Inspect(context.Context, string) (domain.VolumeRecord, error) {
	return domain.VolumeRecord{}, sql.ErrNoRows
}

func (pr551NoopVolumeManager) ListProjectVolumes(context.Context, string) (map[string]domain.VolumeRecord, error) {
	return map[string]domain.VolumeRecord{}, nil
}

func (pr551NoopVolumeManager) ReplaceProjectVolumes(context.Context, string, map[string]domain.ProjectVolumeLink) error {
	return nil
}

func (pr551NoopVolumeManager) RemoveProjectVolumes(context.Context, string) error {
	return nil
}

type pr551Store struct {
	project  domain.ProjectRecord
	revision domain.ProjectRevisionRecord
}

func (s *pr551Store) GetProject(_ context.Context, id string) (domain.ProjectRecord, error) {
	if s.project.ID == id {
		return s.project, nil
	}
	return domain.ProjectRecord{}, sql.ErrNoRows
}

func (s *pr551Store) GetProjectIfExists(_ context.Context, id string, includeRemoved bool) (domain.ProjectRecord, bool, error) {
	if s.project.ID == id && (includeRemoved || s.project.RemovedAt.IsZero()) {
		return s.project, true, nil
	}
	return domain.ProjectRecord{}, false, nil
}

func (s *pr551Store) ListProjects(_ context.Context, _ domain.ProjectListOptions) (domain.ProjectListResult, error) {
	return domain.ProjectListResult{Projects: []domain.ProjectRecord{s.project}}, nil
}

func (s *pr551Store) UpsertProject(_ context.Context, p domain.ProjectRecord) (domain.ProjectRecord, error) {
	s.project = p
	return p, nil
}

func (s *pr551Store) SetProjectApplyTrustedHeaders(context.Context, string, []domain.TrustedHeader) error {
	return nil
}

func (s *pr551Store) MarkProjectRemoved(_ context.Context, _ string) (domain.ProjectRecord, error) {
	return domain.ProjectRecord{}, nil
}

func (s *pr551Store) SaveProjectRevision(_ context.Context, r domain.ProjectRevisionRecord) (domain.ProjectRevisionRecord, bool, error) {
	s.revision = r
	return r, true, nil
}

func (s *pr551Store) GetProjectRevision(_ context.Context, _ string, _ int64) (domain.ProjectRevisionRecord, error) {
	return s.revision, nil
}

func (s *pr551Store) GetProjectAgent(_ context.Context, _ string, _ string) (domain.ProjectAgentRecord, error) {
	return domain.ProjectAgentRecord{}, sql.ErrNoRows
}

func (s *pr551Store) UpsertProjectAgent(_ context.Context, _ domain.ProjectAgentRecord) (domain.ProjectAgentRecord, error) {
	return domain.ProjectAgentRecord{}, nil
}

func (s *pr551Store) ListProjectAgents(_ context.Context, _ string) ([]domain.ProjectAgentRecord, error) {
	return nil, nil
}

func (s *pr551Store) ListProjectSchedulers(_ context.Context, _ string) ([]domain.ProjectSchedulerRecord, error) {
	return nil, nil
}

func (s *pr551Store) GetAgentDefinitionIfExists(_ context.Context, _ string, _ bool) (domain.AgentDefinition, bool, error) {
	return domain.AgentDefinition{}, false, nil
}

func (s *pr551Store) UpsertManagedAgentDefinition(_ context.Context, _ domain.AgentDefinition) (domain.AgentDefinition, error) {
	return domain.AgentDefinition{}, nil
}

func (s *pr551Store) ListManagedAgentDefinitions(_ context.Context, _ string, _ bool) ([]domain.AgentDefinition, error) {
	return nil, nil
}

func (s *pr551Store) SetAgentDefinitionEnabled(_ context.Context, _ string, _ bool) (domain.AgentDefinition, error) {
	return domain.AgentDefinition{}, nil
}

func (s *pr551Store) GetProjectScheduler(_ context.Context, _ string, _ string) (domain.ProjectSchedulerRecord, error) {
	return domain.ProjectSchedulerRecord{}, sql.ErrNoRows
}

func (s *pr551Store) UpsertProjectScheduler(_ context.Context, _ domain.ProjectSchedulerRecord) (domain.ProjectSchedulerRecord, error) {
	return domain.ProjectSchedulerRecord{}, nil
}

func (s *pr551Store) DeleteProjectScheduler(_ context.Context, _ string, _ string) error {
	return nil
}

func (s *pr551Store) DeleteProjectSchedulers(_ context.Context, _ string) error {
	return nil
}

func (s *pr551Store) GetProjectSchedulers(_ context.Context, _ string) ([]domain.ProjectSchedulerRecord, error) {
	return nil, nil
}

func (s *pr551Store) SetProjectSchedulerEnabled(_ context.Context, _ string, _ string, _ bool) (domain.ProjectSchedulerRecord, error) {
	return domain.ProjectSchedulerRecord{}, nil
}

func (s *pr551Store) GetScheduler(_ context.Context, _ string) (domain.Scheduler, error) {
	return domain.Scheduler{}, sql.ErrNoRows
}

func (s *pr551Store) ReplaceSchedulerTriggers(_ context.Context, _ string, _ []domain.SchedulerTrigger) ([]domain.SchedulerTrigger, error) {
	return nil, nil
}

func (s *pr551Store) ListSandboxes(_ context.Context, _ domain.SandboxListOptions) (domain.SandboxListResult, error) {
	return domain.SandboxListResult{}, nil
}
