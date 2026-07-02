package agentcompose

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	projectdomain "agent-compose/internal/agentcompose/project"
	"agent-compose/pkg/compose"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

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
	return projectdomain.ManagedSchedulerBuildsFromSpec(ctx, project, revision, spec, serviceInlineSchedulerValidator{service: s})
}

func (s *Service) validateProjectManagedSchedulers(ctx context.Context, normalized normalizedV2Project) []*agentcomposev2.ProjectValidationIssue {
	project, err := NewProjectRecordFromSpec(normalized.spec, normalized.sourcePath)
	if err != nil {
		return []*agentcomposev2.ProjectValidationIssue{projectValidationIssue("spec", err.Error())}
	}
	return projectdomain.ValidateManagedSchedulers(ctx, project, normalized.spec, serviceInlineSchedulerValidator{service: s})
}

type serviceInlineSchedulerValidator struct {
	service *Service
}

func (v serviceInlineSchedulerValidator) ValidateInlineSchedulerScript(ctx context.Context, agentName, script string) ([]LoaderTrigger, error) {
	if v.service == nil {
		return nil, &projectdomain.ManagedSchedulerBuildError{Path: "agents." + agentName + ".scheduler.script", Message: "loader manager is required to validate scheduler script"}
	}
	validation, err := v.service.validateInlineSchedulerScript(ctx, agentName, script)
	if err != nil {
		return nil, err
	}
	return validation.Triggers, nil
}

func (s *Service) validateInlineSchedulerScript(ctx context.Context, agentName string, script string) (LoaderValidationResult, error) {
	path := "agents." + agentName + ".scheduler.script"
	if s == nil || s.loaders == nil {
		return LoaderValidationResult{}, &projectdomain.ManagedSchedulerBuildError{Path: path, Message: "loader manager is required to validate scheduler script"}
	}
	if s.loaders.engine == nil {
		return LoaderValidationResult{}, &projectdomain.ManagedSchedulerBuildError{Path: path, Message: "loader engine is required to validate scheduler script"}
	}
	validation, err := s.loaders.Validate(ctx, LoaderRuntimeScheduler, script)
	if err != nil {
		return LoaderValidationResult{}, &projectdomain.ManagedSchedulerBuildError{Path: path, Message: err.Error()}
	}
	return validation, nil
}

func projectManagedSchedulerBuildIssue(err error) *agentcomposev2.ProjectValidationIssue {
	return projectdomain.ManagedSchedulerBuildIssue(err)
}

func (s *Service) validateProjectManagedAgentDefinitions(normalized normalizedV2Project) []*agentcomposev2.ProjectValidationIssue {
	project, err := NewProjectRecordFromSpec(normalized.spec, normalized.sourcePath)
	if err != nil {
		return []*agentcomposev2.ProjectValidationIssue{projectValidationIssue("spec", err.Error())}
	}
	defaultDriver := ""
	if s != nil && s.config != nil && strings.TrimSpace(s.config.RuntimeDriver) != "" {
		defaultDriver = s.config.RuntimeDriver
	}
	return projectdomain.ValidateManagedAgentDefinitions(project, normalized.spec, defaultDriver)
}

func (s *Service) reconcileProjectManagedAgentDefinitions(ctx context.Context, project ProjectRecord, current []AgentDefinition) ([]*agentcomposev2.ProjectChange, bool, error) {
	return projectdomain.ReconcileManagedAgentDefinitions(ctx, s.configDB, project, current)
}

func (s *Service) reconcileProjectManagedSchedulers(ctx context.Context, project ProjectRecord, schedulers []ProjectSchedulerRecord, loaders []Loader) ([]*agentcomposev2.ProjectChange, bool, error) {
	return projectdomain.ReconcileManagedSchedulers(ctx, s.configDB, s.loaders, project, schedulers, loaders)
}

func (s *Service) cleanupFailedManagedSchedulerReconcile(ctx context.Context, scheduler ProjectSchedulerRecord, loaderID string) {
	if s == nil {
		return
	}
	projectdomain.CleanupFailedManagedSchedulerReconcile(ctx, s.configDB, s.loaders, scheduler, loaderID)
}

func (s *Service) disableManagedLoaderIfOwned(ctx context.Context, loaderID, projectID, schedulerID string) error {
	if s == nil {
		return nil
	}
	return projectdomain.DisableManagedLoaderIfOwned(ctx, s.configDB, loaderID, projectID, schedulerID)
}

func managedAgentDefinitionChangeAction(existing AgentDefinition, found bool, current AgentDefinition) agentcomposev2.ProjectChangeAction {
	return projectdomain.ManagedAgentDefinitionChangeAction(existing, found, current)
}

func schedulerChangeAction(existing ProjectSchedulerRecord, found bool, current ProjectSchedulerRecord) agentcomposev2.ProjectChangeAction {
	return projectdomain.SchedulerChangeAction(existing, found, current)
}

func managedLoaderChangeAction(existing Loader, found bool, current Loader) agentcomposev2.ProjectChangeAction {
	return projectdomain.ManagedLoaderChangeAction(existing, found, current)
}

func sameLoaderTriggerSpecs(a, b []LoaderTrigger) bool {
	return projectdomain.SameLoaderTriggerSpecs(a, b)
}

func sameSessionEnvItems(a, b []SessionEnvVar) bool {
	return projectdomain.SameSessionEnvItems(a, b)
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
	return projectdomain.GetProjectAgentIfExists(ctx, store, projectID, agentName)
}

func getProjectSchedulerIfExists(ctx context.Context, store *ConfigStore, projectID, schedulerID string) (ProjectSchedulerRecord, bool, error) {
	return projectdomain.GetProjectSchedulerIfExists(ctx, store, projectID, schedulerID)
}

func projectApplyChanges(project ProjectRecord, existing ProjectRecord, found bool, revision ProjectRevisionRecord, revisionCreated bool) []*agentcomposev2.ProjectChange {
	return projectdomain.ProjectApplyChanges(project, existing, found, revision, revisionCreated)
}

func dryRunProjectChanges(project ProjectRecord, agents []ProjectAgentRecord, agentDefinitions []AgentDefinition, schedulers []ProjectSchedulerRecord, loaders []Loader) []*agentcomposev2.ProjectChange {
	return projectdomain.DryRunProjectChanges(project, agents, agentDefinitions, schedulers, loaders)
}

func projectRecordUnchanged(existing ProjectRecord, current ProjectRecord) bool {
	return projectdomain.ProjectRecordUnchanged(existing, current)
}

func agentChangeAction(existing ProjectAgentRecord, found bool, current ProjectAgentRecord) agentcomposev2.ProjectChangeAction {
	return projectdomain.AgentChangeAction(existing, found, current)
}
