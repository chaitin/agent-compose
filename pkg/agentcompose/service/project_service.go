package agentcompose

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"gopkg.in/yaml.v3"

	"agent-compose/pkg/agentcompose/api"
	"agent-compose/pkg/compose"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/images"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
	"agent-compose/pkg/runs"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type normalizedV2Project struct {
	spec       *compose.NormalizedProjectSpec
	specProto  *agentcomposev2.ProjectSpec
	specHash   string
	sourcePath string
}

type projectManagedSchedulerBuild struct {
	scheduler          ProjectSchedulerRecord
	loader             Loader
	validationTriggers []domain.LoaderTrigger
}

func (s *Service) ValidateProject(ctx context.Context, req *connect.Request[agentcomposev2.ValidateProjectRequest]) (*connect.Response[agentcomposev2.ValidateProjectResponse], error) {
	normalized, issues, err := normalizeProjectServiceSpec(req.Msg.GetSpec(), req.Msg.GetSource(), req.Msg.GetExpectedSpecHash())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if len(issues) > 0 {
		return connect.NewResponse(&agentcomposev2.ValidateProjectResponse{
			Valid:    false,
			Issues:   issues,
			SpecHash: specHashOrEmpty(normalized),
		}), nil
	}
	if issues := s.validateProjectManagedAgentDefinitions(normalized); len(issues) > 0 {
		return connect.NewResponse(&agentcomposev2.ValidateProjectResponse{
			Valid:    false,
			Issues:   issues,
			SpecHash: normalized.specHash,
		}), nil
	}
	if issues := s.validateProjectManagedSchedulers(ctx, normalized); len(issues) > 0 {
		return connect.NewResponse(&agentcomposev2.ValidateProjectResponse{
			Valid:    false,
			Issues:   issues,
			SpecHash: normalized.specHash,
		}), nil
	}
	return connect.NewResponse(&agentcomposev2.ValidateProjectResponse{
		Valid:    true,
		SpecHash: normalized.specHash,
	}), nil
}

func (s *Service) ApplyProject(ctx context.Context, req *connect.Request[agentcomposev2.ApplyProjectRequest]) (*connect.Response[agentcomposev2.ApplyProjectResponse], error) {
	normalized, issues, err := normalizeProjectServiceSpec(req.Msg.GetSpec(), req.Msg.GetSource(), req.Msg.GetExpectedSpecHash())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if len(issues) > 0 {
		return connect.NewResponse(&agentcomposev2.ApplyProjectResponse{
			Issues: issues,
		}), nil
	}
	if s.configDB == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("apply project %s: config store is required", normalized.spec.Name))
	}

	project, err := projects.NewRecordFromSpec(normalized.spec, normalized.sourcePath)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("apply project %s: %w", normalized.spec.Name, err))
	}
	if issues := s.validateProjectManagedAgentDefinitions(normalized); len(issues) > 0 {
		return connect.NewResponse(&agentcomposev2.ApplyProjectResponse{Issues: issues}), nil
	}
	if issues := s.validateProjectManagedSchedulers(ctx, normalized); len(issues) > 0 {
		return connect.NewResponse(&agentcomposev2.ApplyProjectResponse{Issues: issues}), nil
	}
	agentRecords, err := projects.NewAgentRecordsFromSpec(project.ID, 0, normalized.spec)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("apply project %s: %w", normalized.spec.Name, err))
	}
	agentDefinitions, err := projects.NewAgentDefinitionsFromSpec(project, 0, normalized.spec)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("apply project %s: %w", normalized.spec.Name, err))
	}
	schedulerRecords, managedLoaders, err := s.projectManagedSchedulersFromSpec(ctx, project, 0, normalized.spec)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("apply project %s: %w", normalized.spec.Name, err))
	}
	if req.Msg.GetDryRun() {
		return connect.NewResponse(&agentcomposev2.ApplyProjectResponse{
			Project:  api.ProjectToProto(project, normalized.specProto, agentRecords, schedulerRecords),
			Revision: api.ProjectRevisionToProto(ProjectRevisionRecord{ProjectID: project.ID, SpecHash: normalized.specHash}, normalized.specProto),
			Changes:  api.DryRunProjectChanges(project, agentRecords, agentDefinitions, schedulerRecords, managedLoaders),
			Applied:  false,
		}), nil
	}
	if err := images.EnsureProjectAgentImages(ctx, s.config, s.images, normalized.spec.Name, agentRecords); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("apply project %s: %w", normalized.spec.Name, err))
	}

	existingProject, projectFound, err := s.configDB.getProject(ctx, project.ID, true)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("apply project %s: load existing project: %w", normalized.spec.Name, err))
	}
	project, err = s.configDB.UpsertProject(ctx, project)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("apply project %s: upsert project: %w", normalized.spec.Name, err))
	}
	specJSON, err := normalized.spec.MarshalCanonicalJSON(false)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("apply project %s: marshal project spec: %w", normalized.spec.Name, err))
	}
	revision, revisionCreated, err := s.configDB.SaveProjectRevision(ctx, ProjectRevisionRecord{
		ProjectID: project.ID,
		SpecHash:  normalized.specHash,
		SpecJSON:  string(specJSON),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("apply project %s: save revision: %w", normalized.spec.Name, err))
	}
	project, err = s.configDB.GetProject(ctx, project.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("apply project %s: reload project: %w", normalized.spec.Name, err))
	}

	agentRecords, err = projects.NewAgentRecordsFromSpec(project.ID, revision.Revision, normalized.spec)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("apply project %s: %w", normalized.spec.Name, err))
	}
	agentDefinitions, err = projects.NewAgentDefinitionsFromSpec(project, revision.Revision, normalized.spec)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("apply project %s: %w", normalized.spec.Name, err))
	}
	schedulerRecords, managedLoaders, err = s.projectManagedSchedulersFromSpec(ctx, project, revision.Revision, normalized.spec)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("apply project %s: %w", normalized.spec.Name, err))
	}
	changes := api.ProjectApplyChanges(project, existingProject, projectFound, revision, revisionCreated)
	agentsUnchanged := true
	for _, agent := range agentRecords {
		existingAgent, found, err := getProjectAgentIfExists(ctx, s.configDB, project.ID, agent.AgentName)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("apply project %s: load agent %s: %w", normalized.spec.Name, agent.AgentName, err))
		}
		if _, err := s.configDB.UpsertProjectAgent(ctx, agent); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("apply project %s: upsert agent %s: %w", normalized.spec.Name, agent.AgentName, err))
		}
		action := agentChangeAction(existingAgent, found, agent)
		if action != agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED {
			agentsUnchanged = false
		}
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       action,
			ResourceType: "project_agent",
			ResourceId:   agent.ManagedAgentID,
			Name:         agent.AgentName,
		})
	}
	agentDefinitionChanges, agentDefinitionsUnchanged, err := s.reconcileProjectManagedAgentDefinitions(ctx, project, agentDefinitions)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("apply project %s: %w", normalized.spec.Name, err))
	}
	if !agentDefinitionsUnchanged {
		agentsUnchanged = false
	}
	changes = append(changes, agentDefinitionChanges...)
	schedulerChanges, schedulersUnchanged, err := s.reconcileProjectManagedSchedulers(ctx, project, schedulerRecords, managedLoaders)
	if err != nil {
		changes = append(changes, schedulerChanges...)
		agents, listAgentsErr := s.configDB.ListProjectAgents(ctx, project.ID)
		if listAgentsErr != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("apply project %s: %w; list project agents after reconcile failure: %v", normalized.spec.Name, err, listAgentsErr))
		}
		schedulers, listSchedulersErr := s.configDB.ListProjectSchedulers(ctx, project.ID)
		if listSchedulersErr != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("apply project %s: %w; list project schedulers after reconcile failure: %v", normalized.spec.Name, err, listSchedulersErr))
		}
		return connect.NewResponse(&agentcomposev2.ApplyProjectResponse{
			Project:  api.ProjectToProto(project, normalized.specProto, agents, schedulers),
			Revision: api.ProjectRevisionToProto(revision, normalized.specProto),
			Changes:  changes,
			Issues: []*agentcomposev2.ProjectValidationIssue{
				api.ProjectValidationIssue("reconcile.schedulers", fmt.Sprintf("apply project %s: %v", normalized.spec.Name, err)),
			},
			Applied:   false,
			Unchanged: false,
		}), nil
	}
	if !schedulersUnchanged {
		agentsUnchanged = false
	}
	changes = append(changes, schedulerChanges...)

	agents, err := s.configDB.ListProjectAgents(ctx, project.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("apply project %s: list project agents: %w", normalized.spec.Name, err))
	}
	schedulers, err := s.configDB.ListProjectSchedulers(ctx, project.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("apply project %s: list project schedulers: %w", normalized.spec.Name, err))
	}
	return connect.NewResponse(&agentcomposev2.ApplyProjectResponse{
		Project:  api.ProjectToProto(project, normalized.specProto, agents, schedulers),
		Revision: api.ProjectRevisionToProto(revision, normalized.specProto),
		Changes:  changes,
		Applied:  true,
		Unchanged: projectFound &&
			!revisionCreated &&
			projects.ProjectRecordUnchanged(existingProject, project) &&
			agentsUnchanged,
	}), nil
}

func (s *Service) GetProject(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
	if s.configDB == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	project, err := s.resolveProjectRef(ctx, req.Msg.GetProject())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		if errors.Is(err, ErrRequired) || errors.Is(err, ErrAmbiguous) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	agents, err := s.configDB.ListProjectAgents(ctx, project.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	schedulers, err := s.configDB.ListProjectSchedulers(ctx, project.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var spec *agentcomposev2.ProjectSpec
	if req.Msg.GetIncludeSpec() && project.CurrentRevision > 0 {
		revision, err := s.configDB.GetProjectRevision(ctx, project.ID, project.CurrentRevision)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		spec, err = runs.DecodeRevisionSpec(revision.SpecJSON)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decode project %s revision %d: %w", project.Name, project.CurrentRevision, err))
		}
	}
	return connect.NewResponse(&agentcomposev2.GetProjectResponse{
		Project: api.ProjectToProto(project, spec, agents, schedulers),
	}), nil
}

func (s *Service) ListProjects(ctx context.Context, req *connect.Request[agentcomposev2.ListProjectsRequest]) (*connect.Response[agentcomposev2.ListProjectsResponse], error) {
	if s.configDB == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	result, err := s.configDB.ListProjects(ctx, ProjectListOptions{
		Query:          req.Msg.GetQuery(),
		IncludeRemoved: req.Msg.GetIncludeRemoved(),
		Offset:         int(req.Msg.GetOffset()),
		Limit:          int(req.Msg.GetLimit()),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev2.ListProjectsResponse{
		TotalCount: uint32(result.TotalCount),
		HasMore:    result.HasMore,
		NextOffset: uint32(result.NextOffset),
	}
	for _, project := range result.Projects {
		agents, err := s.configDB.ListProjectAgents(ctx, project.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list project %s agents: %w", project.Name, err))
		}
		schedulers, err := s.configDB.ListProjectSchedulers(ctx, project.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list project %s schedulers: %w", project.Name, err))
		}
		resp.Projects = append(resp.Projects, api.ProjectSummaryToProto(project, agents, schedulers))
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) RemoveProject(ctx context.Context, req *connect.Request[agentcomposev2.RemoveProjectRequest]) (*connect.Response[agentcomposev2.RemoveProjectResponse], error) {
	if s.configDB == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	if req.Msg.GetRemoveHistory() {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("project history removal is not implemented"))
	}
	project, err := s.resolveProjectRef(ctx, req.Msg.GetProject())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		if errors.Is(err, ErrRequired) || errors.Is(err, ErrAmbiguous) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	changes, err := s.downProject(ctx, project)
	if err != nil {
		return nil, err
	}
	agents, err := s.configDB.ListProjectAgents(ctx, project.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	schedulers, err := s.configDB.ListProjectSchedulers(ctx, project.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentcomposev2.RemoveProjectResponse{
		Project: api.ProjectToProto(project, nil, agents, schedulers),
		Changes: changes,
	}), nil
}

func (s *Service) resolveProjectRef(ctx context.Context, ref *agentcomposev2.ProjectRef) (ProjectRecord, error) {
	if ref == nil {
		return ProjectRecord{}, classifyError(ErrRequired, "project ref is required", nil)
	}
	if projectID := strings.TrimSpace(ref.GetProjectId()); projectID != "" {
		return s.configDB.GetProject(ctx, projectID)
	}
	name := strings.TrimSpace(ref.GetName())
	sourcePath := strings.TrimSpace(ref.GetSourcePath())
	if name != "" && sourcePath != "" {
		projectID, err := domain.StableProjectID(name, sourcePath)
		if err != nil {
			return ProjectRecord{}, err
		}
		return s.configDB.GetProject(ctx, projectID)
	}
	if name == "" {
		return ProjectRecord{}, classifyError(ErrRequired, "project id or name is required", nil)
	}
	result, err := s.configDB.ListProjects(ctx, ProjectListOptions{Query: name, Limit: 200})
	if err != nil {
		return ProjectRecord{}, err
	}
	var matches []ProjectRecord
	for _, project := range result.Projects {
		if project.Name == name {
			matches = append(matches, project)
		}
	}
	if len(matches) == 0 {
		return ProjectRecord{}, resourceError(ErrNotFound, "project", name, fmt.Sprintf("project %s not found", name), sql.ErrNoRows)
	}
	if len(matches) > 1 {
		return ProjectRecord{}, classifyError(ErrAmbiguous, fmt.Sprintf("project name %s is ambiguous; use project_id or source_path", name), nil)
	}
	return matches[0], nil
}

func normalizeProjectServiceSpec(spec *agentcomposev2.ProjectSpec, source *agentcomposev2.ProjectSource, expectedHash string) (normalizedV2Project, []*agentcomposev2.ProjectValidationIssue, error) {
	if spec == nil {
		return normalizedV2Project{}, []*agentcomposev2.ProjectValidationIssue{api.ProjectValidationIssue("spec", "project spec is required")}, nil
	}
	raw, issues := api.ProjectSpecYAMLShape(spec)
	if len(issues) > 0 {
		return normalizedV2Project{}, issues, nil
	}
	data, err := yaml.Marshal(raw)
	if err != nil {
		return normalizedV2Project{}, nil, fmt.Errorf("marshal project spec: %w", err)
	}
	parsed, err := compose.Parse(data)
	if err != nil {
		return normalizedV2Project{}, []*agentcomposev2.ProjectValidationIssue{api.IssueFromComposeError(err)}, nil
	}
	sourcePath := api.ProjectServiceSourcePath(source)
	normalized, err := compose.Normalize(parsed, compose.NormalizeOptions{
		ComposePath: sourcePath,
		ProjectDir:  strings.TrimSpace(source.GetProjectDir()),
	})
	if err != nil {
		return normalizedV2Project{}, []*agentcomposev2.ProjectValidationIssue{api.IssueFromComposeError(err)}, nil
	}
	hash, err := normalized.Hash()
	if err != nil {
		return normalizedV2Project{}, nil, fmt.Errorf("hash project spec: %w", err)
	}
	result := normalizedV2Project{
		spec:       normalized,
		specProto:  ProjectSpecResponse(normalized),
		specHash:   hash,
		sourcePath: sourcePath,
	}
	expectedHash = strings.TrimSpace(expectedHash)
	if expectedHash != "" && expectedHash != hash {
		return result, []*agentcomposev2.ProjectValidationIssue{api.ProjectValidationIssue("expected_spec_hash", fmt.Sprintf("expected spec hash %s does not match normalized spec hash %s", expectedHash, hash))}, nil
	}
	return result, nil, nil
}

func specHashOrEmpty(normalized normalizedV2Project) string {
	return normalized.specHash
}

func (s *Service) projectManagedSchedulersFromSpec(ctx context.Context, project ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]ProjectSchedulerRecord, []Loader, error) {
	builds, err := s.projectManagedSchedulerBuildsFromSpec(ctx, project, revision, spec)
	if err != nil {
		return nil, nil, err
	}
	projectBuilds := projectSchedulerBuildsToProjects(builds)
	return projects.SchedulerRecords(projectBuilds), projects.SchedulerLoaders(projectBuilds), nil
}

func projectManagedSchedulerBuildsFromSpec(project ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]projectManagedSchedulerBuild, error) {
	builds, err := projects.NewSchedulerBuildsFromSpec(project, revision, spec)
	if err != nil {
		return nil, err
	}
	return projectSchedulerBuildsFromProjects(builds), nil
}

func projectSchedulerBuildsToProjects(builds []projectManagedSchedulerBuild) []projects.SchedulerBuild {
	result := make([]projects.SchedulerBuild, 0, len(builds))
	for _, build := range builds {
		result = append(result, projects.SchedulerBuild{
			Scheduler:          build.scheduler,
			Loader:             build.loader,
			ValidationTriggers: build.validationTriggers,
		})
	}
	return result
}

func projectSchedulerBuildsFromProjects(builds []projects.SchedulerBuild) []projectManagedSchedulerBuild {
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

func (s *Service) projectManagedSchedulerBuildsFromSpec(ctx context.Context, project ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]projectManagedSchedulerBuild, error) {
	builds, err := projects.NewSchedulerBuildsFromSpec(project, revision, spec)
	if err != nil {
		return nil, err
	}
	inlineScripts := make(map[string]string, len(spec.Agents))
	for _, agent := range spec.Agents {
		if agent.Scheduler == nil {
			continue
		}
		if script := strings.TrimSpace(agent.Scheduler.Script); script != "" {
			inlineScripts[agent.Name] = agent.Scheduler.Script
		}
	}
	for i := range builds {
		script := inlineScripts[builds[i].Scheduler.AgentName]
		if strings.TrimSpace(script) == "" {
			continue
		}
		validation, err := s.validateInlineSchedulerScript(ctx, builds[i].Scheduler.AgentName, script)
		if err != nil {
			return nil, err
		}
		builds[i].ValidationTriggers = validation.Triggers
		builds[i].Loader.Triggers = validation.Triggers
		builds[i].Scheduler.TriggerCount = len(validation.Triggers)
	}
	return projectSchedulerBuildsFromProjects(builds), nil
}

func (s *Service) validateProjectManagedSchedulers(ctx context.Context, normalized normalizedV2Project) []*agentcomposev2.ProjectValidationIssue {
	project, err := projects.NewRecordFromSpec(normalized.spec, normalized.sourcePath)
	if err != nil {
		return []*agentcomposev2.ProjectValidationIssue{api.ProjectValidationIssue("spec", err.Error())}
	}
	builds, err := s.projectManagedSchedulerBuildsFromSpec(ctx, project, 0, normalized.spec)
	if err != nil {
		return []*agentcomposev2.ProjectValidationIssue{projectManagedSchedulerBuildIssue(err)}
	}
	loaderRecords := projects.SchedulerLoaders(projectSchedulerBuildsToProjects(builds))
	for _, loader := range loaderRecords {
		if _, err := loaders.NormalizeLoader(loader, false); err != nil {
			return []*agentcomposev2.ProjectValidationIssue{api.ProjectValidationIssue("schedulers."+loader.Summary.ManagedAgentName, err.Error())}
		}
		for _, trigger := range loader.Triggers {
			if _, err := loaders.NormalizeLoaderTrigger(loader.Summary.ID, trigger); err != nil {
				return []*agentcomposev2.ProjectValidationIssue{api.ProjectValidationIssue("schedulers."+loader.Summary.ManagedAgentName+".triggers", err.Error())}
			}
		}
	}
	return nil
}

type projectManagedSchedulerBuildError struct {
	path    string
	message string
}

func (e *projectManagedSchedulerBuildError) Error() string {
	if e.path == "" {
		return e.message
	}
	return e.path + ": " + e.message
}

func (s *Service) validateInlineSchedulerScript(ctx context.Context, agentName string, script string) (loaders.LoaderValidationResult, error) {
	path := "agents." + agentName + ".scheduler.script"
	if s == nil || s.loaders == nil {
		return loaders.LoaderValidationResult{}, &projectManagedSchedulerBuildError{path: path, message: "loader manager is required to validate scheduler script"}
	}
	if s.loaders.engine == nil {
		return loaders.LoaderValidationResult{}, &projectManagedSchedulerBuildError{path: path, message: "loader engine is required to validate scheduler script"}
	}
	validation, err := s.loaders.Validate(ctx, domain.LoaderRuntimeScheduler, script)
	if err != nil {
		return loaders.LoaderValidationResult{}, &projectManagedSchedulerBuildError{path: path, message: err.Error()}
	}
	return validation, nil
}

func projectManagedSchedulerBuildIssue(err error) *agentcomposev2.ProjectValidationIssue {
	var buildErr *projectManagedSchedulerBuildError
	if errors.As(err, &buildErr) {
		return api.ProjectValidationIssue(buildErr.path, buildErr.message)
	}
	return api.ProjectValidationIssue("schedulers", err.Error())
}

func (s *Service) validateProjectManagedAgentDefinitions(normalized normalizedV2Project) []*agentcomposev2.ProjectValidationIssue {
	project, err := projects.NewRecordFromSpec(normalized.spec, normalized.sourcePath)
	if err != nil {
		return []*agentcomposev2.ProjectValidationIssue{api.ProjectValidationIssue("spec", err.Error())}
	}
	agents, err := projects.NewAgentDefinitionsFromSpec(project, 0, normalized.spec)
	if err != nil {
		return []*agentcomposev2.ProjectValidationIssue{api.ProjectValidationIssue("agents", err.Error())}
	}
	var issues []*agentcomposev2.ProjectValidationIssue
	defaultDriver := driverpkg.RuntimeDriverDocker
	if s != nil && s.config != nil && strings.TrimSpace(s.config.RuntimeDriver) != "" {
		defaultDriver = s.config.RuntimeDriver
	}
	for _, agent := range agents {
		path := "agents." + agent.ManagedAgentName
		if _, err := domain.NormalizeAgentDefinition(agent, true); err != nil {
			issues = append(issues, api.ProjectValidationIssue(path, err.Error()))
			continue
		}
		if strings.TrimSpace(agent.Driver) != "" {
			if _, err := driverpkg.ResolveSessionRuntimeDriver(agent.Driver, defaultDriver); err != nil {
				issues = append(issues, api.ProjectValidationIssue(path+".driver", err.Error()))
			}
		}
	}
	return issues
}

func (s *Service) reconcileProjectManagedAgentDefinitions(ctx context.Context, project ProjectRecord, current []domain.AgentDefinition) ([]*agentcomposev2.ProjectChange, bool, error) {
	changes, unchanged, err := projects.ReconcileManagedAgentDefinitions(ctx, s.configDB, project, current)
	return projectChangesToProto(changes), unchanged, err
}

func (s *Service) reconcileProjectManagedSchedulers(ctx context.Context, project ProjectRecord, schedulers []ProjectSchedulerRecord, loaders []Loader) ([]*agentcomposev2.ProjectChange, bool, error) {
	changes, unchanged, err := projects.ReconcileManagedSchedulers(ctx, s.configDB, project, schedulers, loaders, projects.ReconcileSchedulerOptions{
		CleanupFailedManagedScheduler: s.cleanupFailedManagedSchedulerReconcile,
		DisableManagedLoaderIfOwned:   s.disableManagedLoaderIfOwned,
		RefreshLoaders:                s.refreshLoaders,
	})
	return projectChangesToProto(changes), unchanged, err
}

func (s *Service) cleanupFailedManagedSchedulerReconcile(ctx context.Context, scheduler ProjectSchedulerRecord, loaderID string) {
	if s == nil || s.configDB == nil {
		return
	}
	if strings.TrimSpace(loaderID) != "" {
		_ = s.configDB.SetLoaderEnabled(ctx, loaderID, false)
	}
	if strings.TrimSpace(scheduler.ProjectID) != "" && strings.TrimSpace(scheduler.SchedulerID) != "" {
		_, _ = s.configDB.SetProjectSchedulerEnabled(ctx, scheduler.ProjectID, scheduler.SchedulerID, false)
	}
	if s.loaders != nil {
		_ = s.loaders.Refresh(ctx)
	}
}

func (s *Service) disableManagedLoaderIfOwned(ctx context.Context, loaderID, projectID, schedulerID string) error {
	return projects.DisableManagedLoaderIfOwned(ctx, s.configDB, loaderID, projectID, schedulerID)
}

func getProjectAgentIfExists(ctx context.Context, store *ConfigStore, projectID, agentName string) (ProjectAgentRecord, bool, error) {
	agent, err := store.GetProjectAgent(ctx, projectID, agentName)
	if err == nil {
		return agent, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectAgentRecord{}, false, nil
	}
	return ProjectAgentRecord{}, false, err
}

func agentChangeAction(existing ProjectAgentRecord, found bool, current ProjectAgentRecord) agentcomposev2.ProjectChangeAction {
	return projectChangeActionToProto(projects.ProjectAgentChangeAction(existing, found, current))
}

func projectChangesToProto(changes []projects.Change) []*agentcomposev2.ProjectChange {
	items := make([]*agentcomposev2.ProjectChange, 0, len(changes))
	for _, change := range changes {
		items = append(items, &agentcomposev2.ProjectChange{
			Action:       projectChangeActionToProto(change.Action),
			ResourceType: change.ResourceType,
			ResourceId:   change.ResourceID,
			Name:         change.Name,
			Message:      change.Message,
		})
	}
	return items
}

func projectChangeActionToProto(action string) agentcomposev2.ProjectChangeAction {
	switch action {
	case projects.ChangeActionCreated:
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED
	case projects.ChangeActionUpdated:
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED
	case projects.ChangeActionRemoved:
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_REMOVED
	case projects.ChangeActionUnchanged:
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED
	default:
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNSPECIFIED
	}
}

// ProjectSpecResponse converts a normalized compose spec into the v2 ProjectSpec API shape.
func ProjectSpecResponse(spec *compose.NormalizedProjectSpec) *agentcomposev2.ProjectSpec {
	return api.ProjectSpecToProto(spec)
}
