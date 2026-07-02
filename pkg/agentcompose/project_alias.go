package agentcompose

import (
	"context"

	"github.com/samber/do/v2"

	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	projectspkg "agent-compose/pkg/projects"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type ProjectService = projectspkg.Service
type ProjectRunStartRequest = projectspkg.ProjectRunStartRequest
type ProjectRunTransitionRequest = projectspkg.ProjectRunTransitionRequest
type ProjectRunPreparation = projectspkg.ProjectRunPreparation
type ProjectRunSessionResult = projectspkg.ProjectRunSessionResult
type RunCoordinator = projectspkg.RunCoordinator

type projectManagedSchedulerBuild struct {
	scheduler          ProjectSchedulerRecord
	loader             Loader
	validationTriggers []LoaderTrigger
}

func NewRunCoordinator(store *ConfigStore) *RunCoordinator {
	return projectspkg.NewRunCoordinator(store)
}

func projectManagedSchedulerBuildsFromSpec(project ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]projectManagedSchedulerBuild, error) {
	builds, err := projectspkg.ProjectManagedSchedulerBuildsFromSpec(project, revision, spec)
	if err != nil {
		return nil, err
	}
	return projectManagedSchedulerBuildsFromProjects(builds), nil
}

func (s *Service) projectManagedSchedulerBuildsFromSpec(ctx context.Context, project ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]projectManagedSchedulerBuild, error) {
	builds, err := s.projectService().ProjectManagedSchedulerBuildsFromSpec(ctx, project, revision, spec)
	if err != nil {
		return nil, err
	}
	return projectManagedSchedulerBuildsFromProjects(builds), nil
}

func projectManagedSchedulerBuildsFromProjects(builds []projectspkg.ProjectManagedSchedulerBuild) []projectManagedSchedulerBuild {
	result := make([]projectManagedSchedulerBuild, 0, len(builds))
	for _, build := range builds {
		result = append(result, projectManagedSchedulerBuild{
			scheduler:          build.Scheduler,
			loader:             build.Loader,
			validationTriggers: build.ValidationTriggers,
		})
	}
	return result
}

func projectManagedLoaderTriggersAndScript(projectID, agentName, schedulerName string, scheduler *compose.NormalizedSchedulerSpec) ([]LoaderTrigger, string, error) {
	return projectspkg.ProjectManagedLoaderTriggersAndScript(projectID, agentName, schedulerName, scheduler)
}

func (s *Service) reconcileProjectManagedSchedulers(ctx context.Context, project ProjectRecord, schedulers []ProjectSchedulerRecord, loaders []Loader) ([]*agentcomposev2.ProjectChange, bool, error) {
	return s.projectService().ReconcileProjectManagedSchedulers(ctx, project, schedulers, loaders)
}

func sameLoaderTriggerSpecs(a, b []LoaderTrigger) bool {
	return projectspkg.SameLoaderTriggerSpecs(a, b)
}

func projectRunSessionTags(run ProjectRunRecord) []SessionTag {
	return projectspkg.ProjectRunSessionTags(run)
}

func mergeSessionTags(existing, additions []SessionTag) []SessionTag {
	return projectspkg.MergeSessionTags(existing, additions)
}

func (s *Service) prepareProjectRun(ctx context.Context, run ProjectRunRecord, requestEnv []*agentcomposev2.EnvVarSpec) (ProjectRunPreparation, error) {
	return s.projectService().PrepareProjectRun(ctx, run, requestEnv)
}

func ProjectSpecResponse(spec *compose.NormalizedProjectSpec) *agentcomposev2.ProjectSpec {
	return projectspkg.ProjectSpecResponse(spec)
}

func NewProjectService(di do.Injector) (*ProjectService, error) {
	imageBackends, err := imageBackendsFromDI(di)
	if err != nil {
		return nil, err
	}
	return NewProjectServiceFromDeps(&Service{
		config:    do.MustInvoke[*appconfig.Config](di),
		store:     do.MustInvoke[*Store](di),
		configDB:  do.MustInvoke[*ConfigStore](di),
		driver:    do.MustInvoke[Driver](di),
		executor:  do.MustInvoke[*Executor](di),
		images:    imageBackends.docker,
		loaders:   do.MustInvoke[*LoaderManager](di),
		cap:       do.MustInvoke[capabilityIntegration](di),
		bus:       do.MustInvoke[*LoaderBus](di),
		streams:   do.MustInvoke[*SessionStreamBroker](di),
		dashboard: mustDashboardHub(di),
	}), nil
}

func NewProjectServiceFromDeps(s *Service) *ProjectService {
	deps := ProjectServiceDeps(s)
	return projectspkg.NewService(deps)
}

func ProjectServiceDeps(s *Service) projectspkg.ServiceDeps {
	if s == nil {
		return projectspkg.ServiceDeps{}
	}
	var executorComponent *projectspkg.Executor
	if s.executor != nil {
		executorComponent = s.executor.componentExecutor()
	}
	var streams *projectspkg.SessionStreamBroker
	if s.streams != nil {
		streams = s.streams.componentBroker()
	}
	return projectspkg.ServiceDeps{
		Config:    s.config,
		Store:     s.store,
		ConfigDB:  s.configDB,
		Driver:    s.driver,
		Executor:  executorComponent,
		Images:    s.images,
		Loaders:   s.loaders,
		Cap:       s.cap,
		Bus:       s.bus,
		Streams:   streams,
		Dashboard: s.dashboard,
	}
}
