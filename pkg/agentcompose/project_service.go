package agentcompose

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"

	"agent-compose/pkg/compose"
	driverpkg "agent-compose/pkg/driver"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type normalizedV2Project struct {
	spec       *compose.NormalizedProjectSpec
	specProto  *agentcomposev2.ProjectSpec
	specHash   string
	sourcePath string
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

	project, err := NewProjectRecordFromSpec(normalized.spec, normalized.sourcePath)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("apply project %s: %w", normalized.spec.Name, err))
	}
	if issues := s.validateProjectManagedAgentDefinitions(normalized); len(issues) > 0 {
		return connect.NewResponse(&agentcomposev2.ApplyProjectResponse{Issues: issues}), nil
	}
	if issues := s.validateProjectManagedSchedulers(ctx, normalized); len(issues) > 0 {
		return connect.NewResponse(&agentcomposev2.ApplyProjectResponse{Issues: issues}), nil
	}
	agentRecords, err := projectAgentRecordsFromSpec(project.ID, 0, normalized.spec)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("apply project %s: %w", normalized.spec.Name, err))
	}
	agentDefinitions, err := projectManagedAgentDefinitionsFromSpec(project, 0, normalized.spec)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("apply project %s: %w", normalized.spec.Name, err))
	}
	schedulerRecords, managedLoaders, err := s.projectManagedSchedulersFromSpec(ctx, project, 0, normalized.spec)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("apply project %s: %w", normalized.spec.Name, err))
	}
	if req.Msg.GetDryRun() {
		return connect.NewResponse(&agentcomposev2.ApplyProjectResponse{
			Project:  projectResponse(project, normalized.specProto, agentRecords, schedulerRecords),
			Revision: projectRevisionResponse(ProjectRevisionRecord{ProjectID: project.ID, SpecHash: normalized.specHash}, normalized.specProto),
			Changes:  dryRunProjectChanges(project, agentRecords, agentDefinitions, schedulerRecords, managedLoaders),
			Applied:  false,
		}), nil
	}
	if err := s.ensureProjectAgentImages(ctx, normalized.spec.Name, agentRecords); err != nil {
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

	agentRecords, err = projectAgentRecordsFromSpec(project.ID, revision.Revision, normalized.spec)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("apply project %s: %w", normalized.spec.Name, err))
	}
	agentDefinitions, err = projectManagedAgentDefinitionsFromSpec(project, revision.Revision, normalized.spec)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("apply project %s: %w", normalized.spec.Name, err))
	}
	schedulerRecords, managedLoaders, err = s.projectManagedSchedulersFromSpec(ctx, project, revision.Revision, normalized.spec)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("apply project %s: %w", normalized.spec.Name, err))
	}
	changes := projectApplyChanges(project, existingProject, projectFound, revision, revisionCreated)
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
			Project:  projectResponse(project, normalized.specProto, agents, schedulers),
			Revision: projectRevisionResponse(revision, normalized.specProto),
			Changes:  changes,
			Issues: []*agentcomposev2.ProjectValidationIssue{
				projectValidationIssue("reconcile.schedulers", fmt.Sprintf("apply project %s: %v", normalized.spec.Name, err)),
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
		Project:  projectResponse(project, normalized.specProto, agents, schedulers),
		Revision: projectRevisionResponse(revision, normalized.specProto),
		Changes:  changes,
		Applied:  true,
		Unchanged: projectFound &&
			!revisionCreated &&
			projectRecordUnchanged(existingProject, project) &&
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
		if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "ambiguous") {
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
		spec, err = decodeProjectRevisionSpec(revision.SpecJSON)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decode project %s revision %d: %w", project.Name, project.CurrentRevision, err))
		}
	}
	return connect.NewResponse(&agentcomposev2.GetProjectResponse{
		Project: projectResponse(project, spec, agents, schedulers),
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
		resp.Projects = append(resp.Projects, projectSummaryResponse(project, nil, nil))
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
		if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "ambiguous") {
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
		Project: projectResponse(project, nil, agents, schedulers),
		Changes: changes,
	}), nil
}

func (s *Service) resolveProjectRef(ctx context.Context, ref *agentcomposev2.ProjectRef) (ProjectRecord, error) {
	if ref == nil {
		return ProjectRecord{}, fmt.Errorf("project ref is required")
	}
	if projectID := strings.TrimSpace(ref.GetProjectId()); projectID != "" {
		return s.configDB.GetProject(ctx, projectID)
	}
	name := strings.TrimSpace(ref.GetName())
	sourcePath := strings.TrimSpace(ref.GetSourcePath())
	if name != "" && sourcePath != "" {
		projectID, err := StableProjectID(name, sourcePath)
		if err != nil {
			return ProjectRecord{}, err
		}
		return s.configDB.GetProject(ctx, projectID)
	}
	if name == "" {
		return ProjectRecord{}, fmt.Errorf("project id or name is required")
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
		return ProjectRecord{}, fmt.Errorf("project %s not found: %w", name, sql.ErrNoRows)
	}
	if len(matches) > 1 {
		return ProjectRecord{}, fmt.Errorf("project name %s is ambiguous; use project_id or source_path", name)
	}
	return matches[0], nil
}

func (s *Service) projectManagedSchedulersFromSpec(ctx context.Context, project ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]ProjectSchedulerRecord, []Loader, error) {
	builds, err := s.projectManagedSchedulerBuildsFromSpec(ctx, project, revision, spec)
	if err != nil {
		return nil, nil, err
	}
	return projectManagedSchedulerRecords(builds), projectManagedSchedulerLoaders(builds), nil
}

func (s *Service) projectManagedSchedulerBuildsFromSpec(ctx context.Context, project ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]projectManagedSchedulerBuild, error) {
	builds := make([]projectManagedSchedulerBuild, 0)
	for _, agent := range spec.Agents {
		record, ok, err := NewProjectSchedulerRecordFromSpec(project.ID, revision, agent)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		loader, err := projectManagedLoaderFromScheduler(project, record, agent)
		if err != nil {
			return nil, err
		}
		validationTriggers := loader.Triggers
		if strings.TrimSpace(agent.Scheduler.Script) != "" {
			validation, err := s.validateInlineSchedulerScript(ctx, agent.Name, agent.Scheduler.Script)
			if err != nil {
				return nil, err
			}
			validationTriggers = validation.Triggers
			loader.Triggers = validation.Triggers
			record.TriggerCount = len(validation.Triggers)
		}
		builds = append(builds, projectManagedSchedulerBuild{
			scheduler:          record,
			loader:             loader,
			validationTriggers: validationTriggers,
		})
	}
	return builds, nil
}

func (s *Service) validateProjectManagedSchedulers(ctx context.Context, normalized normalizedV2Project) []*agentcomposev2.ProjectValidationIssue {
	project, err := NewProjectRecordFromSpec(normalized.spec, normalized.sourcePath)
	if err != nil {
		return []*agentcomposev2.ProjectValidationIssue{projectValidationIssue("spec", err.Error())}
	}
	builds, err := s.projectManagedSchedulerBuildsFromSpec(ctx, project, 0, normalized.spec)
	if err != nil {
		return []*agentcomposev2.ProjectValidationIssue{projectManagedSchedulerBuildIssue(err)}
	}
	loaders := projectManagedSchedulerLoaders(builds)
	for _, loader := range loaders {
		if _, err := normalizeLoader(loader, false); err != nil {
			return []*agentcomposev2.ProjectValidationIssue{projectValidationIssue("schedulers."+loader.Summary.ManagedAgentName, err.Error())}
		}
		for _, trigger := range loader.Triggers {
			if _, err := normalizeLoaderTrigger(loader.Summary.ID, trigger); err != nil {
				return []*agentcomposev2.ProjectValidationIssue{projectValidationIssue("schedulers."+loader.Summary.ManagedAgentName+".triggers", err.Error())}
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

func (s *Service) validateInlineSchedulerScript(ctx context.Context, agentName string, script string) (LoaderValidationResult, error) {
	path := "agents." + agentName + ".scheduler.script"
	if s == nil || s.loaders == nil {
		return LoaderValidationResult{}, &projectManagedSchedulerBuildError{path: path, message: "loader manager is required to validate scheduler script"}
	}
	if s.loaders.engine == nil {
		return LoaderValidationResult{}, &projectManagedSchedulerBuildError{path: path, message: "loader engine is required to validate scheduler script"}
	}
	validation, err := s.loaders.Validate(ctx, LoaderRuntimeScheduler, script)
	if err != nil {
		return LoaderValidationResult{}, &projectManagedSchedulerBuildError{path: path, message: err.Error()}
	}
	return validation, nil
}

func projectManagedSchedulerBuildIssue(err error) *agentcomposev2.ProjectValidationIssue {
	var buildErr *projectManagedSchedulerBuildError
	if errors.As(err, &buildErr) {
		return projectValidationIssue(buildErr.path, buildErr.message)
	}
	return projectValidationIssue("schedulers", err.Error())
}

func (s *Service) validateProjectManagedAgentDefinitions(normalized normalizedV2Project) []*agentcomposev2.ProjectValidationIssue {
	project, err := NewProjectRecordFromSpec(normalized.spec, normalized.sourcePath)
	if err != nil {
		return []*agentcomposev2.ProjectValidationIssue{projectValidationIssue("spec", err.Error())}
	}
	agents, err := projectManagedAgentDefinitionsFromSpec(project, 0, normalized.spec)
	if err != nil {
		return []*agentcomposev2.ProjectValidationIssue{projectValidationIssue("agents", err.Error())}
	}
	var issues []*agentcomposev2.ProjectValidationIssue
	defaultDriver := driverpkg.RuntimeDriverDocker
	if s != nil && s.config != nil && strings.TrimSpace(s.config.RuntimeDriver) != "" {
		defaultDriver = s.config.RuntimeDriver
	}
	for _, agent := range agents {
		path := "agents." + agent.ManagedAgentName
		if _, err := normalizeAgentDefinition(agent, true); err != nil {
			issues = append(issues, projectValidationIssue(path, err.Error()))
			continue
		}
		if strings.TrimSpace(agent.Driver) != "" {
			if _, err := driverpkg.ResolveSessionRuntimeDriver(agent.Driver, defaultDriver); err != nil {
				issues = append(issues, projectValidationIssue(path+".driver", err.Error()))
			}
		}
	}
	return issues
}

func (s *Service) reconcileProjectManagedAgentDefinitions(ctx context.Context, project ProjectRecord, current []AgentDefinition) ([]*agentcomposev2.ProjectChange, bool, error) {
	if s.configDB == nil {
		return nil, false, fmt.Errorf("config store is required")
	}
	currentByID := make(map[string]AgentDefinition, len(current))
	for _, agent := range current {
		currentByID[agent.ID] = agent
	}
	changes := make([]*agentcomposev2.ProjectChange, 0, len(current))
	unchanged := true
	for _, agent := range current {
		existing, found, err := s.configDB.getAgentDefinitionIfExists(ctx, agent.ID, true)
		if err != nil {
			return nil, false, fmt.Errorf("load managed agent definition %s: %w", agent.ID, err)
		}
		saved, err := s.configDB.UpsertManagedAgentDefinition(ctx, agent)
		if err != nil {
			return nil, false, fmt.Errorf("upsert managed agent definition %s: %w", agent.ID, err)
		}
		action := managedAgentDefinitionChangeAction(existing, found, agent)
		if action != agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED {
			unchanged = false
		}
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       action,
			ResourceType: "agent_definition",
			ResourceId:   saved.ID,
			Name:         saved.Name,
		})
	}

	existingManaged, err := s.configDB.ListManagedAgentDefinitions(ctx, project.ID, false)
	if err != nil {
		return nil, false, fmt.Errorf("list managed agent definitions: %w", err)
	}
	for _, existing := range existingManaged {
		if _, ok := currentByID[existing.ID]; ok {
			continue
		}
		if !existing.Enabled {
			continue
		}
		disabled, err := s.configDB.SetAgentDefinitionEnabled(ctx, existing.ID, false)
		if err != nil {
			return nil, false, fmt.Errorf("disable removed managed agent definition %s: %w", existing.ID, err)
		}
		unchanged = false
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED,
			ResourceType: "agent_definition",
			ResourceId:   disabled.ID,
			Name:         disabled.Name,
			Message:      "disabled because the agent is no longer present in the project spec",
		})
	}
	return changes, unchanged, nil
}

func (s *Service) reconcileProjectManagedSchedulers(ctx context.Context, project ProjectRecord, schedulers []ProjectSchedulerRecord, loaders []Loader) ([]*agentcomposev2.ProjectChange, bool, error) {
	if s.configDB == nil {
		return nil, false, fmt.Errorf("config store is required")
	}
	currentByID := make(map[string]ProjectSchedulerRecord, len(schedulers))
	loadersByID := make(map[string]Loader, len(loaders))
	for _, loader := range loaders {
		loadersByID[loader.Summary.ID] = loader
	}
	changes := make([]*agentcomposev2.ProjectChange, 0, len(schedulers)+len(loaders))
	unchanged := true
	for _, scheduler := range schedulers {
		currentByID[scheduler.SchedulerID] = scheduler
		existing, found, err := getProjectSchedulerIfExists(ctx, s.configDB, scheduler.ProjectID, scheduler.SchedulerID)
		if err != nil {
			return changes, false, fmt.Errorf("load project scheduler %s/%s: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
		}
		stagedScheduler := scheduler
		stagedScheduler.Enabled = false
		saved, err := s.configDB.UpsertProjectScheduler(ctx, stagedScheduler)
		if err != nil {
			return changes, false, fmt.Errorf("stage project scheduler %s/%s disabled: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
		}

		loader, ok := loadersByID[saved.ManagedLoaderID]
		if !ok {
			return changes, false, fmt.Errorf("managed loader %s for scheduler %s missing", saved.ManagedLoaderID, saved.SchedulerID)
		}
		existingLoader, loaderFound, err := s.configDB.getLoaderIfExists(ctx, loader.Summary.ID)
		if err != nil {
			return changes, false, fmt.Errorf("load managed loader %s: %w", loader.Summary.ID, err)
		}
		stagedLoader := loader
		stagedLoader.Summary.Enabled = false
		savedLoader, err := s.configDB.UpsertManagedLoader(ctx, stagedLoader)
		if err != nil {
			return changes, false, fmt.Errorf("stage managed loader %s disabled: %w", loader.Summary.ID, err)
		}
		if _, err := s.configDB.ReplaceLoaderTriggers(ctx, savedLoader.Summary.ID, loader.Triggers); err != nil {
			s.cleanupFailedManagedSchedulerReconcile(ctx, saved, savedLoader.Summary.ID)
			return changes, false, fmt.Errorf("replace managed loader triggers %s: %w", savedLoader.Summary.ID, err)
		}
		if loader.Summary.Enabled {
			if err := s.configDB.SetLoaderEnabled(ctx, savedLoader.Summary.ID, true); err != nil {
				s.cleanupFailedManagedSchedulerReconcile(ctx, saved, savedLoader.Summary.ID)
				return changes, false, fmt.Errorf("enable managed loader %s: %w", savedLoader.Summary.ID, err)
			}
		} else if err := s.configDB.SetLoaderEnabled(ctx, savedLoader.Summary.ID, false); err != nil {
			return changes, false, fmt.Errorf("disable managed loader %s: %w", savedLoader.Summary.ID, err)
		}
		if scheduler.Enabled {
			saved, err = s.configDB.SetProjectSchedulerEnabled(ctx, scheduler.ProjectID, scheduler.SchedulerID, true)
			if err != nil {
				s.cleanupFailedManagedSchedulerReconcile(ctx, stagedScheduler, savedLoader.Summary.ID)
				return changes, false, fmt.Errorf("enable project scheduler %s/%s: %w", scheduler.ProjectID, scheduler.SchedulerID, err)
			}
		} else {
			saved = stagedScheduler
		}
		action := schedulerChangeAction(existing, found, scheduler)
		if action != agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED {
			unchanged = false
		}
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       action,
			ResourceType: "project_scheduler",
			ResourceId:   saved.SchedulerID,
			Name:         saved.AgentName,
		})
		loaderAction := managedLoaderChangeAction(existingLoader, loaderFound, loader)
		if loaderAction != agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED {
			unchanged = false
		}
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       loaderAction,
			ResourceType: "loader",
			ResourceId:   savedLoader.Summary.ID,
			Name:         savedLoader.Summary.Name,
		})
	}
	existingSchedulers, err := s.configDB.ListProjectSchedulers(ctx, project.ID)
	if err != nil {
		return changes, false, fmt.Errorf("list project schedulers: %w", err)
	}
	for _, existing := range existingSchedulers {
		if _, ok := currentByID[existing.SchedulerID]; ok {
			continue
		}
		if !existing.Enabled {
			continue
		}
		disabled, err := s.configDB.SetProjectSchedulerEnabled(ctx, existing.ProjectID, existing.SchedulerID, false)
		if err != nil {
			return changes, false, fmt.Errorf("disable removed project scheduler %s/%s: %w", existing.ProjectID, existing.SchedulerID, err)
		}
		if err := s.disableManagedLoaderIfOwned(ctx, existing.ManagedLoaderID, project.ID, existing.SchedulerID); err != nil {
			return changes, false, fmt.Errorf("disable removed managed loader %s: %w", existing.ManagedLoaderID, err)
		}
		unchanged = false
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_REMOVED,
			ResourceType: "project_scheduler",
			ResourceId:   disabled.SchedulerID,
			Name:         disabled.AgentName,
			Message:      "disabled because the scheduler is no longer present in the project spec",
		}, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_REMOVED,
			ResourceType: "loader",
			ResourceId:   existing.ManagedLoaderID,
			Name:         existing.AgentName,
			Message:      "disabled because the scheduler is no longer present in the project spec",
		})
	}
	if s.loaders != nil {
		if err := s.loaders.Refresh(ctx); err != nil {
			return changes, false, fmt.Errorf("refresh loader manager: %w", err)
		}
	}
	return changes, unchanged, nil
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
	loaderID = strings.TrimSpace(loaderID)
	if loaderID == "" {
		return nil
	}
	loader, found, err := s.configDB.getLoaderIfExists(ctx, loaderID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if loader.Summary.ManagedProjectID != strings.TrimSpace(projectID) || loader.Summary.ManagedSchedulerID != strings.TrimSpace(schedulerID) {
		return nil
	}
	if !loader.Summary.Enabled {
		return nil
	}
	return s.configDB.SetLoaderEnabled(ctx, loaderID, false)
}

func managedAgentDefinitionChangeAction(existing AgentDefinition, found bool, current AgentDefinition) agentcomposev2.ProjectChangeAction {
	if !found {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED
	}
	if !existing.DeletedAt.IsZero() || !existing.Enabled {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED
	}
	if existing.Name == current.Name &&
		existing.Description == current.Description &&
		existing.Provider == current.Provider &&
		existing.Model == current.Model &&
		existing.SystemPrompt == current.SystemPrompt &&
		existing.Driver == current.Driver &&
		existing.GuestImage == current.GuestImage &&
		existing.WorkspaceID == current.WorkspaceID &&
		existing.ConfigJSON == current.ConfigJSON &&
		sameSessionEnvItems(existing.EnvItems, current.EnvItems) &&
		sameStringSlices(existing.CapsetIDs, current.CapsetIDs) &&
		existing.ManagedProjectID == current.ManagedProjectID &&
		existing.ManagedProjectRevision == current.ManagedProjectRevision &&
		existing.ManagedAgentName == current.ManagedAgentName {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED
	}
	return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED
}

func schedulerChangeAction(existing ProjectSchedulerRecord, found bool, current ProjectSchedulerRecord) agentcomposev2.ProjectChangeAction {
	if !found {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED
	}
	if existing.ManagedLoaderID == current.ManagedLoaderID &&
		existing.Revision == current.Revision &&
		existing.Enabled == current.Enabled &&
		existing.TriggerCount == current.TriggerCount &&
		existing.SpecJSON == current.SpecJSON {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED
	}
	return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED
}

func managedLoaderChangeAction(existing Loader, found bool, current Loader) agentcomposev2.ProjectChangeAction {
	if !found {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED
	}
	if existing.Summary.Name == current.Summary.Name &&
		existing.Summary.Description == current.Summary.Description &&
		existing.Summary.Enabled == current.Summary.Enabled &&
		existing.Summary.Runtime == current.Summary.Runtime &&
		existing.Summary.WorkspaceID == current.Summary.WorkspaceID &&
		existing.Summary.AgentID == current.Summary.AgentID &&
		existing.Summary.Driver == current.Summary.Driver &&
		existing.Summary.GuestImage == current.Summary.GuestImage &&
		existing.Summary.DefaultAgent == current.Summary.DefaultAgent &&
		existing.Summary.SessionPolicy == current.Summary.SessionPolicy &&
		existing.Summary.ConcurrencyPolicy == current.Summary.ConcurrencyPolicy &&
		existing.Summary.ManagedProjectID == current.Summary.ManagedProjectID &&
		existing.Summary.ManagedRevision == current.Summary.ManagedRevision &&
		existing.Summary.ManagedAgentName == current.Summary.ManagedAgentName &&
		existing.Summary.ManagedSchedulerID == current.Summary.ManagedSchedulerID &&
		existing.Script == current.Script &&
		sameSessionEnvItems(existing.EnvItems, current.EnvItems) &&
		sameStringSlices(existing.Summary.CapsetIDs, current.Summary.CapsetIDs) &&
		sameLoaderTriggerSpecs(existing.Triggers, current.Triggers) {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED
	}
	return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED
}

func sameLoaderTriggerSpecs(a, b []LoaderTrigger) bool {
	a = normalizeComparableLoaderTriggers(a)
	b = normalizeComparableLoaderTriggers(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID ||
			a[i].Kind != b[i].Kind ||
			a[i].Topic != b[i].Topic ||
			a[i].IntervalMs != b[i].IntervalMs ||
			a[i].AutoID != b[i].AutoID ||
			a[i].SpecJSON != b[i].SpecJSON {
			return false
		}
	}
	return true
}

func normalizeComparableLoaderTriggers(items []LoaderTrigger) []LoaderTrigger {
	cloned := append([]LoaderTrigger(nil), items...)
	for i := range cloned {
		cloned[i].ID = strings.TrimSpace(cloned[i].ID)
		cloned[i].Kind = strings.TrimSpace(cloned[i].Kind)
		cloned[i].Topic = strings.TrimSpace(cloned[i].Topic)
		cloned[i].SpecJSON = strings.TrimSpace(cloned[i].SpecJSON)
	}
	slices.SortFunc(cloned, func(a, b LoaderTrigger) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return strings.Compare(a.ID, b.ID)
	})
	return cloned
}

func sameSessionEnvItems(a, b []SessionEnvVar) bool {
	a = normalizeEnvItems(a)
	b = normalizeEnvItems(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameStringSlices(a, b []string) bool {
	a = normalizeCapsetIDs(a)
	b = normalizeCapsetIDs(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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

func getProjectSchedulerIfExists(ctx context.Context, store *ConfigStore, projectID, schedulerID string) (ProjectSchedulerRecord, bool, error) {
	scheduler, err := store.GetProjectScheduler(ctx, projectID, schedulerID)
	if err == nil {
		return scheduler, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectSchedulerRecord{}, false, nil
	}
	return ProjectSchedulerRecord{}, false, err
}

func projectApplyChanges(project ProjectRecord, existing ProjectRecord, found bool, revision ProjectRevisionRecord, revisionCreated bool) []*agentcomposev2.ProjectChange {
	projectAction := agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED
	if found {
		projectAction = agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED
		if !projectRecordUnchanged(existing, project) {
			projectAction = agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED
		}
	}
	revisionAction := agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED
	if revisionCreated {
		revisionAction = agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED
	}
	return []*agentcomposev2.ProjectChange{
		{
			Action:       projectAction,
			ResourceType: "project",
			ResourceId:   project.ID,
			Name:         project.Name,
		},
		{
			Action:       revisionAction,
			ResourceType: "project_revision",
			ResourceId:   fmt.Sprintf("%s/%d", revision.ProjectID, revision.Revision),
			Name:         revision.SpecHash,
		},
	}
}

func dryRunProjectChanges(project ProjectRecord, agents []ProjectAgentRecord, agentDefinitions []AgentDefinition, schedulers []ProjectSchedulerRecord, loaders []Loader) []*agentcomposev2.ProjectChange {
	changes := []*agentcomposev2.ProjectChange{{
		Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED,
		ResourceType: "project",
		ResourceId:   project.ID,
		Name:         project.Name,
	}}
	for _, agent := range agents {
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED,
			ResourceType: "project_agent",
			ResourceId:   agent.ManagedAgentID,
			Name:         agent.AgentName,
		})
	}
	for _, agent := range agentDefinitions {
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED,
			ResourceType: "agent_definition",
			ResourceId:   agent.ID,
			Name:         agent.Name,
		})
	}
	for _, scheduler := range schedulers {
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED,
			ResourceType: "project_scheduler",
			ResourceId:   scheduler.SchedulerID,
			Name:         scheduler.AgentName,
		})
	}
	for _, loader := range loaders {
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED,
			ResourceType: "loader",
			ResourceId:   loader.Summary.ID,
			Name:         loader.Summary.Name,
		})
	}
	return changes
}

func projectRecordUnchanged(existing ProjectRecord, current ProjectRecord) bool {
	return existing.ID == current.ID &&
		existing.Name == current.Name &&
		existing.SourcePath == current.SourcePath &&
		existing.SpecHash == current.SpecHash &&
		existing.CurrentRevision == current.CurrentRevision &&
		existing.RemovedAt.IsZero()
}

func agentChangeAction(existing ProjectAgentRecord, found bool, current ProjectAgentRecord) agentcomposev2.ProjectChangeAction {
	if !found {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED
	}
	if existing.ManagedAgentID == current.ManagedAgentID &&
		existing.Revision == current.Revision &&
		existing.Provider == current.Provider &&
		existing.Model == current.Model &&
		existing.Image == current.Image &&
		existing.Driver == current.Driver &&
		existing.SchedulerEnabled == current.SchedulerEnabled &&
		existing.SpecJSON == current.SpecJSON {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED
	}
	return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED
}
