package agentcompose

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gopkg.in/yaml.v3"

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

type projectManagedSchedulerBuild struct {
	scheduler          ProjectSchedulerRecord
	loader             Loader
	validationTriggers []LoaderTrigger
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

func (s *Service) DiffProject(ctx context.Context, req *connect.Request[agentcomposev2.DiffProjectRequest]) (*connect.Response[agentcomposev2.DiffProjectResponse], error) {
	if s.configDB == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	project, err := s.resolveProjectRef(ctx, req.Msg.GetProject())
	if err != nil {
		return nil, projectRefConnectError(err)
	}
	normalized, issues, err := normalizeProjectServiceSpec(req.Msg.GetIncomingSpec(), req.Msg.GetSource(), "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if len(issues) > 0 {
		return connect.NewResponse(&agentcomposev2.DiffProjectResponse{Issues: issues, CurrentSpecHash: project.SpecHash, IncomingSpecHash: specHashOrEmpty(normalized)}), nil
	}
	project.ID = strings.TrimSpace(project.ID)
	incomingProject, err := NewProjectRecordFromSpec(normalized.spec, normalized.sourcePath)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	incomingProject.ID = project.ID
	incomingProject.CurrentRevision = project.CurrentRevision
	currentAgents, err := s.configDB.ListProjectAgents(ctx, project.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	currentSchedulers, err := s.configDB.ListProjectSchedulers(ctx, project.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	incomingAgents, err := projectAgentRecordsFromSpec(project.ID, project.CurrentRevision, normalized.spec)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	incomingSchedulers, _, err := s.projectManagedSchedulersFromSpec(ctx, incomingProject, project.CurrentRevision, normalized.spec)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&agentcomposev2.DiffProjectResponse{
		Changes:          projectDiffChanges(project, incomingProject, currentAgents, incomingAgents, currentSchedulers, incomingSchedulers),
		CurrentSpecHash:  project.SpecHash,
		IncomingSpecHash: normalized.specHash,
	}), nil
}

func (s *Service) ListProjectRevisions(ctx context.Context, req *connect.Request[agentcomposev2.ListProjectRevisionsRequest]) (*connect.Response[agentcomposev2.ListProjectRevisionsResponse], error) {
	if s.configDB == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	project, err := s.resolveProjectRef(ctx, req.Msg.GetProject())
	if err != nil {
		return nil, projectRefConnectError(err)
	}
	result, err := s.configDB.ListProjectRevisions(ctx, project.ID, int(req.Msg.GetOffset()), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	items := make([]*agentcomposev2.ProjectRevision, 0, len(result.Revisions))
	for _, revision := range result.Revisions {
		spec, err := decodeProjectRevisionSpec(revision.SpecJSON)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		items = append(items, projectRevisionResponse(revision, spec))
	}
	return connect.NewResponse(&agentcomposev2.ListProjectRevisionsResponse{
		Revisions:  items,
		TotalCount: uint32(result.TotalCount),
		HasMore:    result.HasMore,
		NextOffset: uint32(result.NextOffset),
	}), nil
}

func (s *Service) RollbackProjectRevision(ctx context.Context, req *connect.Request[agentcomposev2.RollbackProjectRevisionRequest]) (*connect.Response[agentcomposev2.RollbackProjectRevisionResponse], error) {
	if s.configDB == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	project, err := s.resolveProjectRef(ctx, req.Msg.GetProject())
	if err != nil {
		return nil, projectRefConnectError(err)
	}
	revisionNumber := int64(req.Msg.GetRevision())
	if revisionNumber <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("revision is required"))
	}
	revision, err := s.configDB.GetProjectRevision(ctx, project.ID, revisionNumber)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	spec, err := decodeProjectRevisionSpec(revision.SpecJSON)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	applyResp, err := s.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec:   spec,
		Source: &agentcomposev2.ProjectSource{ComposePath: project.SourcePath},
		DryRun: req.Msg.GetDryRun(),
	}))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentcomposev2.RollbackProjectRevisionResponse{
		Project:  applyResp.Msg.GetProject(),
		Revision: applyResp.Msg.GetRevision(),
		Changes:  applyResp.Msg.GetChanges(),
		Issues:   applyResp.Msg.GetIssues(),
		Applied:  applyResp.Msg.GetApplied(),
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

func projectRefConnectError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "ambiguous") {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func normalizeProjectServiceSpec(spec *agentcomposev2.ProjectSpec, source *agentcomposev2.ProjectSource, expectedHash string) (normalizedV2Project, []*agentcomposev2.ProjectValidationIssue, error) {
	if spec == nil {
		return normalizedV2Project{}, []*agentcomposev2.ProjectValidationIssue{projectValidationIssue("spec", "project spec is required")}, nil
	}
	raw, issues := projectSpecYAMLShape(spec)
	if len(issues) > 0 {
		return normalizedV2Project{}, issues, nil
	}
	data, err := yaml.Marshal(raw)
	if err != nil {
		return normalizedV2Project{}, nil, fmt.Errorf("marshal project spec: %w", err)
	}
	parsed, err := compose.Parse(data)
	if err != nil {
		return normalizedV2Project{}, []*agentcomposev2.ProjectValidationIssue{issueFromComposeError(err)}, nil
	}
	sourcePath := projectServiceSourcePath(source)
	normalized, err := compose.Normalize(parsed, compose.NormalizeOptions{
		ComposePath: sourcePath,
		ProjectDir:  strings.TrimSpace(source.GetProjectDir()),
	})
	if err != nil {
		return normalizedV2Project{}, []*agentcomposev2.ProjectValidationIssue{issueFromComposeError(err)}, nil
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
		return result, []*agentcomposev2.ProjectValidationIssue{projectValidationIssue("expected_spec_hash", fmt.Sprintf("expected spec hash %s does not match normalized spec hash %s", expectedHash, hash))}, nil
	}
	return result, nil, nil
}

func projectSpecYAMLShape(spec *agentcomposev2.ProjectSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	root := map[string]any{}
	if strings.TrimSpace(spec.GetName()) != "" {
		root["name"] = spec.GetName()
	}
	if metadata := metadataYAMLShape(spec.GetMetadata()); len(metadata) > 0 {
		root["metadata"] = metadata
	}
	if variables, issues := envVarYAMLMap("variables", spec.GetVariables()); len(issues) > 0 {
		return nil, issues
	} else if len(variables) > 0 {
		root["variables"] = variables
	}
	if runtime, issues := runtimeYAMLShape(spec.GetRuntime()); len(issues) > 0 {
		return nil, issues
	} else if len(runtime) > 0 {
		root["runtime"] = runtime
	}
	if workspace := workspaceYAMLShape(spec.GetWorkspace()); len(workspace) > 0 {
		root["workspace"] = workspace
	}
	if agents, issues := agentYAMLMap(spec.GetAgents()); len(issues) > 0 {
		return nil, issues
	} else if len(agents) > 0 {
		root["agents"] = agents
	}
	if network := networkYAMLShape(spec.GetNetwork()); len(network) > 0 {
		root["network"] = network
	}
	if services, issues := serviceYAMLMap(spec.GetServices()); len(issues) > 0 {
		return nil, issues
	} else if len(services) > 0 {
		root["services"] = services
	}
	if triggers, issues := projectTriggerYAMLMap(spec.GetTriggers()); len(issues) > 0 {
		return nil, issues
	} else if len(triggers) > 0 {
		root["triggers"] = triggers
	}
	if permissions := permissionYAMLShape(spec.GetPermissions()); len(permissions) > 0 {
		root["permissions"] = permissions
	}
	if artifacts := artifactPolicyYAMLShape(spec.GetArtifacts()); len(artifacts) > 0 {
		root["artifacts"] = artifacts
	}
	return root, nil
}

func metadataYAMLShape(metadata *agentcomposev2.ProjectMetadata) map[string]any {
	if metadata == nil {
		return nil
	}
	raw := map[string]any{}
	if strings.TrimSpace(metadata.GetName()) != "" {
		raw["name"] = metadata.GetName()
	}
	if len(metadata.GetLabels()) > 0 {
		raw["labels"] = cloneStringMap(metadata.GetLabels())
	}
	if len(metadata.GetAnnotations()) > 0 {
		raw["annotations"] = cloneStringMap(metadata.GetAnnotations())
	}
	return raw
}

func runtimeYAMLShape(runtime *agentcomposev2.RuntimeSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	if runtime == nil {
		return nil, nil
	}
	raw := map[string]any{}
	if strings.TrimSpace(runtime.GetDriver()) != "" {
		raw["driver"] = runtime.GetDriver()
	}
	if strings.TrimSpace(runtime.GetImage()) != "" {
		raw["image"] = runtime.GetImage()
	}
	if env, issues := envVarYAMLMap("runtime.env", runtime.GetEnv()); len(issues) > 0 {
		return nil, issues
	} else if len(env) > 0 {
		raw["env"] = env
	}
	if len(runtime.GetResources()) > 0 {
		raw["resources"] = cloneStringMap(runtime.GetResources())
	}
	if network := networkYAMLShape(runtime.GetNetwork()); len(network) > 0 {
		raw["network"] = network
	}
	if cleanup := cleanupPolicyYAMLValue(runtime.GetCleanupPolicy()); cleanup != "" {
		raw["cleanup_policy"] = cleanup
	}
	return raw, nil
}

func envVarYAMLMap(path string, vars []*agentcomposev2.EnvVarSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	values := make(map[string]any, len(vars))
	for i, env := range vars {
		name := strings.TrimSpace(env.GetName())
		if _, ok := values[name]; ok {
			return nil, []*agentcomposev2.ProjectValidationIssue{projectValidationIssue(fmt.Sprintf("%s[%d].name", path, i), fmt.Sprintf("duplicate environment variable %q", name))}
		}
		if env.GetSecret() {
			values[name] = map[string]any{
				"value":  env.GetValue(),
				"secret": true,
			}
		} else {
			values[name] = env.GetValue()
		}
	}
	return values, nil
}

func agentYAMLMap(agents []*agentcomposev2.AgentSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	values := make(map[string]any, len(agents))
	for i, agent := range agents {
		name := strings.TrimSpace(agent.GetName())
		if _, ok := values[name]; ok {
			return nil, []*agentcomposev2.ProjectValidationIssue{projectValidationIssue(fmt.Sprintf("agents[%d].name", i), fmt.Sprintf("duplicate agent %q", name))}
		}
		raw := map[string]any{}
		if strings.TrimSpace(agent.GetProvider()) != "" {
			raw["provider"] = agent.GetProvider()
		}
		if strings.TrimSpace(agent.GetModel()) != "" {
			raw["model"] = agent.GetModel()
		}
		if agent.GetSystemPrompt() != "" {
			raw["system_prompt"] = agent.GetSystemPrompt()
		}
		if strings.TrimSpace(agent.GetImage()) != "" {
			raw["image"] = agent.GetImage()
		}
		if driver, issues := driverYAMLShape(fmt.Sprintf("agents[%d].driver", i), agent.GetDriver()); len(issues) > 0 {
			return nil, issues
		} else if len(driver) > 0 {
			raw["driver"] = driver
		}
		if env, issues := envVarYAMLMap(fmt.Sprintf("agents[%d].env", i), agent.GetEnv()); len(issues) > 0 {
			return nil, issues
		} else if len(env) > 0 {
			raw["env"] = env
		}
		if capsetIDs := normalizeCapsetIDs(agent.GetCapsetIds()); len(capsetIDs) > 0 {
			raw["capset_ids"] = capsetIDs
		}
		if workspace := workspaceYAMLShape(agent.GetWorkspace()); len(workspace) > 0 {
			raw["workspace"] = workspace
		}
		if scheduler := schedulerYAMLShape(agent.GetScheduler()); len(scheduler) > 0 {
			raw["scheduler"] = scheduler
		}
		values[name] = raw
	}
	return values, nil
}

func serviceYAMLMap(services []*agentcomposev2.ServiceSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	values := make(map[string]any, len(services))
	for i, service := range services {
		name := strings.TrimSpace(service.GetName())
		if _, ok := values[name]; ok {
			return nil, []*agentcomposev2.ProjectValidationIssue{projectValidationIssue(fmt.Sprintf("services[%d].name", i), fmt.Sprintf("duplicate service %q", name))}
		}
		raw := map[string]any{}
		if strings.TrimSpace(service.GetDescription()) != "" {
			raw["description"] = service.GetDescription()
		}
		if strings.TrimSpace(service.GetRuntime()) != "" {
			raw["runtime"] = service.GetRuntime()
		}
		if strings.TrimSpace(service.GetEntry()) != "" {
			raw["entry"] = service.GetEntry()
		}
		if strings.TrimSpace(service.GetInputSchemaJson()) != "" {
			raw["input_schema"] = service.GetInputSchemaJson()
		} else if strings.TrimSpace(service.GetInputSchemaRef()) != "" {
			raw["input_schema"] = service.GetInputSchemaRef()
		}
		if strings.TrimSpace(service.GetOutputSchemaJson()) != "" {
			raw["output_schema"] = service.GetOutputSchemaJson()
		} else if strings.TrimSpace(service.GetOutputSchemaRef()) != "" {
			raw["output_schema"] = service.GetOutputSchemaRef()
		}
		if strings.TrimSpace(service.GetErrorSchemaJson()) != "" {
			raw["error_schema"] = service.GetErrorSchemaJson()
		} else if strings.TrimSpace(service.GetErrorSchemaRef()) != "" {
			raw["error_schema"] = service.GetErrorSchemaRef()
		}
		if service.GetTimeoutMs() > 0 {
			raw["timeout"] = fmt.Sprintf("%dms", service.GetTimeoutMs())
		}
		if retry := retryPolicyYAMLShape(service.GetRetry()); len(retry) > 0 {
			raw["retry"] = retry
		}
		if permissions := permissionYAMLShape(service.GetPermissions()); len(permissions) > 0 {
			raw["permissions"] = permissions
		}
		if env, issues := envVarYAMLMap(fmt.Sprintf("services[%d].env", i), service.GetEnv()); len(issues) > 0 {
			return nil, issues
		} else if len(env) > 0 {
			raw["env"] = env
		}
		if len(service.GetAgents()) > 0 {
			raw["agents"] = append([]string(nil), service.GetAgents()...)
		}
		if examples := serviceExampleYAMLShapes(service.GetExamples()); len(examples) > 0 {
			raw["examples"] = examples
		}
		values[name] = raw
	}
	return values, nil
}

func retryPolicyYAMLShape(retry *agentcomposev2.RetryPolicySpec) map[string]any {
	if retry == nil {
		return nil
	}
	raw := map[string]any{}
	if retry.GetMaxAttempts() > 0 {
		raw["max_attempts"] = retry.GetMaxAttempts()
	}
	if retry.GetBackoffMs() > 0 {
		raw["backoff"] = fmt.Sprintf("%dms", retry.GetBackoffMs())
	}
	return raw
}

func serviceExampleYAMLShapes(examples []*agentcomposev2.ServiceExampleSpec) []map[string]any {
	items := make([]map[string]any, 0, len(examples))
	for _, example := range examples {
		raw := map[string]any{}
		if strings.TrimSpace(example.GetName()) != "" {
			raw["name"] = example.GetName()
		}
		if strings.TrimSpace(example.GetInputJson()) != "" {
			raw["input"] = example.GetInputJson()
		}
		if strings.TrimSpace(example.GetOutputJson()) != "" {
			raw["output"] = example.GetOutputJson()
		}
		if len(raw) > 0 {
			items = append(items, raw)
		}
	}
	return items
}

func projectTriggerYAMLMap(triggers []*agentcomposev2.ProjectTriggerSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	values := make(map[string]any, len(triggers))
	for i, trigger := range triggers {
		name := strings.TrimSpace(trigger.GetName())
		if _, ok := values[name]; ok {
			return nil, []*agentcomposev2.ProjectValidationIssue{projectValidationIssue(fmt.Sprintf("triggers[%d].name", i), fmt.Sprintf("duplicate trigger %q", name))}
		}
		raw := map[string]any{}
		if strings.TrimSpace(trigger.GetType()) != "" {
			raw["type"] = trigger.GetType()
		}
		if strings.TrimSpace(trigger.GetCron()) != "" {
			raw["cron"] = trigger.GetCron()
		}
		if strings.TrimSpace(trigger.GetInterval()) != "" {
			raw["interval"] = trigger.GetInterval()
		}
		if strings.TrimSpace(trigger.GetTimeout()) != "" {
			raw["timeout"] = trigger.GetTimeout()
		}
		if event := eventTriggerYAMLShape(trigger.GetEvent()); len(event) > 0 {
			raw["event"] = event
		}
		if webhook := webhookTriggerYAMLShape(trigger.GetWebhook()); len(webhook) > 0 {
			raw["webhook"] = webhook
		}
		if target := triggerTargetYAMLShape(trigger.GetTarget()); len(target) > 0 {
			raw["target"] = target
		}
		if strings.TrimSpace(trigger.GetInputJson()) != "" {
			raw["input"] = trigger.GetInputJson()
		}
		values[name] = raw
	}
	return values, nil
}

func driverYAMLShape(path string, driver *agentcomposev2.DriverSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	if driver == nil {
		return nil, nil
	}
	byName := strings.ToLower(strings.TrimSpace(driver.GetName()))
	runtimes := make(map[string]any, 3)
	if driver.GetBoxlite() != nil {
		runtimes[compose.DriverBoxlite] = map[string]any{
			"kernel": driver.GetBoxlite().GetKernel(),
			"rootfs": driver.GetBoxlite().GetRootfs(),
		}
	}
	if driver.GetDocker() != nil {
		runtimes[compose.DriverDocker] = map[string]any{"host": driver.GetDocker().GetHost()}
	}
	if driver.GetMicrosandbox() != nil {
		runtimes[compose.DriverMicrosandbox] = map[string]any{"profile": driver.GetMicrosandbox().GetProfile()}
	}
	switch byName {
	case "":
	case compose.DriverBoxlite, compose.DriverDocker, compose.DriverMicrosandbox:
		for runtimeName := range runtimes {
			if runtimeName != byName {
				return nil, []*agentcomposev2.ProjectValidationIssue{projectValidationIssue(path, fmt.Sprintf("driver name %q conflicts with %q runtime config", byName, runtimeName))}
			}
		}
		if existing, ok := runtimes[byName]; ok {
			return map[string]any{byName: existing}, nil
		}
		return map[string]any{byName: map[string]any{}}, nil
	default:
		return nil, []*agentcomposev2.ProjectValidationIssue{projectValidationIssue(path+".name", fmt.Sprintf("unsupported runtime driver %q", byName))}
	}
	return runtimes, nil
}

func schedulerYAMLShape(scheduler *agentcomposev2.SchedulerSpec) map[string]any {
	if scheduler == nil {
		return nil
	}
	raw := map[string]any{"enabled": scheduler.GetEnabled()}
	triggers := make([]map[string]any, 0, len(scheduler.GetTriggers()))
	for _, trigger := range scheduler.GetTriggers() {
		triggers = append(triggers, triggerYAMLShape(trigger))
	}
	if len(triggers) > 0 {
		raw["triggers"] = triggers
	}
	if scheduler.GetScript() != "" {
		raw["script"] = scheduler.GetScript()
	}
	return raw
}

func triggerYAMLShape(trigger *agentcomposev2.TriggerSpec) map[string]any {
	raw := map[string]any{}
	if strings.TrimSpace(trigger.GetName()) != "" {
		raw["name"] = trigger.GetName()
	}
	if trigger.GetPrompt() != "" {
		raw["prompt"] = trigger.GetPrompt()
	}
	kind := strings.ToLower(strings.TrimSpace(trigger.GetKind()))
	if kind == "" || kind == "cron" {
		if kind == "cron" || strings.TrimSpace(trigger.GetCron()) != "" {
			raw["cron"] = trigger.GetCron()
		}
	}
	if kind == "" || kind == "interval" {
		if kind == "interval" || strings.TrimSpace(trigger.GetInterval()) != "" {
			raw["interval"] = trigger.GetInterval()
		}
	}
	if kind == "" || kind == "timeout" {
		if kind == "timeout" || strings.TrimSpace(trigger.GetTimeout()) != "" {
			raw["timeout"] = trigger.GetTimeout()
		}
	}
	if kind == "" || kind == "event" {
		if kind == "event" || trigger.GetEvent() != nil {
			raw["event"] = map[string]any{"topic": trigger.GetEvent().GetTopic()}
		}
	}
	if kind != "" && kind != "cron" && kind != "interval" && kind != "timeout" && kind != "event" {
		raw[kind] = ""
	}
	return raw
}

func workspaceYAMLShape(workspace *agentcomposev2.WorkspaceSpec) map[string]any {
	if workspace == nil {
		return nil
	}
	raw := map[string]any{}
	if strings.TrimSpace(workspace.GetProvider()) != "" {
		raw["provider"] = workspace.GetProvider()
	}
	if strings.TrimSpace(workspace.GetUrl()) != "" {
		raw["url"] = workspace.GetUrl()
	}
	if strings.TrimSpace(workspace.GetBranch()) != "" {
		raw["branch"] = workspace.GetBranch()
	}
	if strings.TrimSpace(workspace.GetPath()) != "" {
		raw["path"] = workspace.GetPath()
	}
	return raw
}

func networkYAMLShape(network *agentcomposev2.NetworkSpec) map[string]any {
	if network == nil {
		return nil
	}
	return map[string]any{"mode": network.GetMode()}
}

func eventTriggerYAMLShape(event *agentcomposev2.EventTriggerSpec) map[string]any {
	if event == nil {
		return nil
	}
	raw := map[string]any{}
	if strings.TrimSpace(event.GetTopic()) != "" {
		raw["topic"] = event.GetTopic()
	}
	return raw
}

func webhookTriggerYAMLShape(webhook *agentcomposev2.WebhookTriggerSpec) map[string]any {
	if webhook == nil {
		return nil
	}
	raw := map[string]any{}
	if strings.TrimSpace(webhook.GetTopic()) != "" {
		raw["topic"] = webhook.GetTopic()
	}
	if strings.TrimSpace(webhook.GetMethod()) != "" {
		raw["method"] = webhook.GetMethod()
	}
	return raw
}

func triggerTargetYAMLShape(target *agentcomposev2.TriggerTargetSpec) map[string]any {
	if target == nil {
		return nil
	}
	raw := map[string]any{}
	if strings.TrimSpace(target.GetServiceName()) != "" {
		raw["service"] = target.GetServiceName()
	}
	if strings.TrimSpace(target.GetAgentName()) != "" {
		raw["agent"] = target.GetAgentName()
	}
	return raw
}

func permissionYAMLShape(permission *agentcomposev2.PermissionSpec) map[string]any {
	if permission == nil {
		return nil
	}
	raw := map[string]any{}
	if len(permission.GetAgents()) > 0 {
		raw["agents"] = append([]string(nil), permission.GetAgents()...)
	}
	if len(permission.GetCapabilities()) > 0 {
		raw["capabilities"] = append([]string(nil), permission.GetCapabilities()...)
	}
	if len(permission.GetResources()) > 0 {
		raw["resources"] = append([]string(nil), permission.GetResources()...)
	}
	return raw
}

func artifactPolicyYAMLShape(policy *agentcomposev2.ArtifactPolicySpec) map[string]any {
	if policy == nil {
		return nil
	}
	raw := map[string]any{}
	if strings.TrimSpace(policy.GetRetention()) != "" {
		raw["retention"] = policy.GetRetention()
	}
	if strings.TrimSpace(policy.GetStorage()) != "" {
		raw["storage"] = policy.GetStorage()
	}
	return raw
}

func cleanupPolicyYAMLValue(policy agentcomposev2.RunSessionCleanupPolicy) string {
	switch policy {
	case agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_KEEP_RUNNING:
		return "keep_running"
	case agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_STOP_ON_COMPLETION:
		return "stop_on_completion"
	default:
		return ""
	}
}

func projectServiceSourcePath(source *agentcomposev2.ProjectSource) string {
	if source == nil {
		return ""
	}
	if composePath := strings.TrimSpace(source.GetComposePath()); composePath != "" {
		return composePath
	}
	if projectDir := strings.TrimSpace(source.GetProjectDir()); projectDir != "" {
		return filepath.Join(projectDir, "agent-compose.yml")
	}
	return ""
}

func issueFromComposeError(err error) *agentcomposev2.ProjectValidationIssue {
	var validationErr *compose.ValidationError
	if errors.As(err, &validationErr) {
		return projectValidationIssue(validationErr.Path, validationErr.Message)
	}
	var parseErr *compose.ParseError
	if errors.As(err, &parseErr) {
		return projectValidationIssue(parseErr.Path, parseErr.Message)
	}
	return projectValidationIssue("spec", err.Error())
}

func projectValidationIssue(path, message string) *agentcomposev2.ProjectValidationIssue {
	if strings.TrimSpace(path) == "" {
		path = "spec"
	}
	return &agentcomposev2.ProjectValidationIssue{
		Severity: agentcomposev2.ProjectValidationSeverity_PROJECT_VALIDATION_SEVERITY_ERROR,
		Path:     path,
		Message:  message,
	}
}

func specHashOrEmpty(normalized normalizedV2Project) string {
	return normalized.specHash
}

func projectAgentRecordsFromSpec(projectID string, revision int64, spec *compose.NormalizedProjectSpec) ([]ProjectAgentRecord, error) {
	agents := make([]ProjectAgentRecord, 0, len(spec.Agents))
	for _, agent := range spec.Agents {
		record, err := NewProjectAgentRecordFromSpec(projectID, revision, agent)
		if err != nil {
			return nil, err
		}
		agents = append(agents, record)
	}
	return agents, nil
}

func projectManagedAgentDefinitionsFromSpec(project ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]AgentDefinition, error) {
	agents := make([]AgentDefinition, 0, len(spec.Agents))
	for _, agent := range spec.Agents {
		record, err := projectManagedAgentDefinitionFromSpec(project, revision, agent)
		if err != nil {
			return nil, err
		}
		agents = append(agents, record)
	}
	return agents, nil
}

func projectManagedAgentDefinitionFromSpec(project ProjectRecord, revision int64, agent compose.NormalizedAgentSpec) (AgentDefinition, error) {
	managedAgentID, err := StableManagedAgentID(project.ID, agent.Name)
	if err != nil {
		return AgentDefinition{}, err
	}
	driver := ""
	if agent.Driver != nil {
		driver = agent.Driver.Name
	}
	return AgentDefinition{
		ID:                     managedAgentID,
		Name:                   agent.Name,
		Enabled:                true,
		Provider:               agent.Provider,
		Model:                  agent.Model,
		SystemPrompt:           agent.SystemPrompt,
		Driver:                 driver,
		GuestImage:             agent.Image,
		EnvItems:               sessionEnvItemsFromCompose(agent.Env),
		ConfigJSON:             "{}",
		CapsetIDs:              normalizeCapsetIDs(agent.CapsetIDs),
		ManagedProjectID:       project.ID,
		ManagedProjectRevision: revision,
		ManagedAgentName:       agent.Name,
	}, nil
}

func sessionEnvItemsFromCompose(values map[string]compose.EnvVarSpec) []SessionEnvVar {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	items := make([]SessionEnvVar, 0, len(values))
	for _, name := range names {
		value := values[name]
		items = append(items, SessionEnvVar{Name: name, Value: value.Value, Secret: value.Secret})
	}
	return items
}

func (s *Service) projectManagedSchedulersFromSpec(ctx context.Context, project ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]ProjectSchedulerRecord, []Loader, error) {
	builds, err := s.projectManagedSchedulerBuildsFromSpec(ctx, project, revision, spec)
	if err != nil {
		return nil, nil, err
	}
	return projectManagedSchedulerRecords(builds), projectManagedSchedulerLoaders(builds), nil
}

func projectManagedSchedulerRecords(builds []projectManagedSchedulerBuild) []ProjectSchedulerRecord {
	schedulers := make([]ProjectSchedulerRecord, 0, len(builds))
	for _, build := range builds {
		schedulers = append(schedulers, build.scheduler)
	}
	return schedulers
}

func projectManagedSchedulerLoaders(builds []projectManagedSchedulerBuild) []Loader {
	loaders := make([]Loader, 0, len(builds))
	for _, build := range builds {
		loaders = append(loaders, build.loader)
	}
	return loaders
}

func projectManagedSchedulerBuildsFromSpec(project ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]projectManagedSchedulerBuild, error) {
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
		builds = append(builds, projectManagedSchedulerBuild{
			scheduler:          record,
			loader:             loader,
			validationTriggers: loader.Triggers,
		})
	}
	return builds, nil
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
	serviceBuilds, err := projectManagedServiceTriggerBuildsFromSpec(project, revision, spec)
	if err != nil {
		return nil, err
	}
	builds = append(builds, serviceBuilds...)
	return builds, nil
}

func projectManagedServiceTriggerBuildsFromSpec(project ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]projectManagedSchedulerBuild, error) {
	builds := make([]projectManagedSchedulerBuild, 0)
	for _, trigger := range spec.Triggers {
		serviceName := strings.TrimSpace(trigger.Target.Service)
		if serviceName == "" {
			continue
		}
		if !normalizedProjectHasService(spec, serviceName) {
			return nil, &projectManagedSchedulerBuildError{path: "triggers." + trigger.Name + ".target.service", message: fmt.Sprintf("service %q is not defined", serviceName)}
		}
		agentName := projectServiceTriggerSchedulerAgentName(serviceName, trigger.Name)
		schedulerID, err := StableProjectSchedulerID(project.ID, agentName, "")
		if err != nil {
			return nil, err
		}
		loaderID, err := StableManagedLoaderID(project.ID, agentName, "")
		if err != nil {
			return nil, err
		}
		loaderTrigger, registration, err := projectManagedServiceLoaderTriggerAndRegistration(project.ID, serviceName, trigger)
		if err != nil {
			return nil, &projectManagedSchedulerBuildError{path: "triggers." + trigger.Name, message: err.Error()}
		}
		script := "// Generated by agent-compose project service trigger reconcile.\n" + registration
		specJSON, err := marshalJSONCompact(trigger)
		if err != nil {
			return nil, err
		}
		record := ProjectSchedulerRecord{
			ProjectID:       project.ID,
			SchedulerID:     schedulerID,
			AgentName:       agentName,
			ManagedLoaderID: loaderID,
			Revision:        revision,
			Enabled:         true,
			TriggerCount:    1,
			SpecJSON:        specJSON,
		}
		loader := Loader{
			Summary: LoaderSummary{
				ID:                 loaderID,
				Name:               fmt.Sprintf("%s/%s trigger", project.Name, trigger.Name),
				Enabled:            true,
				Runtime:            LoaderRuntimeScheduler,
				SessionPolicy:      LoaderSessionPolicyNew,
				ConcurrencyPolicy:  LoaderConcurrencyPolicySkip,
				ManagedProjectID:   project.ID,
				ManagedRevision:    revision,
				ManagedAgentName:   agentName,
				ManagedSchedulerID: schedulerID,
			},
			Script:   script,
			Triggers: []LoaderTrigger{loaderTrigger},
		}
		builds = append(builds, projectManagedSchedulerBuild{scheduler: record, loader: loader, validationTriggers: loader.Triggers})
	}
	return builds, nil
}

func normalizedProjectHasService(spec *compose.NormalizedProjectSpec, serviceName string) bool {
	for _, service := range spec.Services {
		if service.Name == serviceName {
			return true
		}
	}
	return false
}

func projectServiceTriggerSchedulerAgentName(serviceName, triggerName string) string {
	return "service-" + strings.TrimSpace(serviceName) + "-" + strings.TrimSpace(triggerName)
}

func projectManagedLoaderFromScheduler(project ProjectRecord, scheduler ProjectSchedulerRecord, agent compose.NormalizedAgentSpec) (Loader, error) {
	managedAgentID, err := StableManagedAgentID(project.ID, agent.Name)
	if err != nil {
		return Loader{}, err
	}
	driver := ""
	if agent.Driver != nil {
		driver = agent.Driver.Name
	}
	var triggers []LoaderTrigger
	script := agent.Scheduler.Script
	if strings.TrimSpace(script) == "" {
		var err error
		triggers, script, err = projectManagedLoaderTriggersAndScript(project.ID, agent.Name, "", agent.Scheduler)
		if err != nil {
			return Loader{}, err
		}
	}
	return Loader{
		Summary: LoaderSummary{
			ID:                 scheduler.ManagedLoaderID,
			Name:               fmt.Sprintf("%s/%s scheduler", project.Name, agent.Name),
			Enabled:            scheduler.Enabled,
			Runtime:            LoaderRuntimeScheduler,
			AgentID:            managedAgentID,
			Driver:             driver,
			GuestImage:         agent.Image,
			DefaultAgent:       agent.Provider,
			SessionPolicy:      LoaderSessionPolicyNew,
			ConcurrencyPolicy:  LoaderConcurrencyPolicySkip,
			CapsetIDs:          normalizeCapsetIDs(agent.CapsetIDs),
			ManagedProjectID:   project.ID,
			ManagedRevision:    scheduler.Revision,
			ManagedAgentName:   agent.Name,
			ManagedSchedulerID: scheduler.SchedulerID,
		},
		Script:   script,
		Triggers: triggers,
		EnvItems: sessionEnvItemsFromCompose(agent.Env),
	}, nil
}

func projectManagedLoaderTriggersAndScript(projectID, agentName, schedulerName string, scheduler *compose.NormalizedSchedulerSpec) ([]LoaderTrigger, string, error) {
	if scheduler == nil {
		return nil, "", fmt.Errorf("scheduler is required")
	}
	triggers := make([]LoaderTrigger, 0, len(scheduler.Triggers))
	seenNames := make(map[string]struct{}, len(scheduler.Triggers))
	var script strings.Builder
	script.WriteString("// Generated by agent-compose project scheduler reconcile.\n")
	for i, trigger := range scheduler.Triggers {
		name := strings.TrimSpace(trigger.Name)
		if name != "" {
			if _, ok := seenNames[name]; ok {
				return nil, "", fmt.Errorf("duplicate scheduler trigger name %q", name)
			}
			seenNames[name] = struct{}{}
		}
		id, err := StableManagedTriggerID(projectID, agentName, schedulerName, name, i)
		if err != nil {
			return nil, "", err
		}
		loaderTrigger, registration, err := projectManagedLoaderTriggerAndRegistration(id, agentName, trigger)
		if err != nil {
			return nil, "", err
		}
		triggers = append(triggers, loaderTrigger)
		script.WriteString(registration)
	}
	if len(triggers) == 0 {
		script.WriteString("function main() { return { status: \"idle\" }; }\n")
	}
	return triggers, script.String(), nil
}

func projectManagedLoaderTriggerAndRegistration(id, agentName string, trigger compose.NormalizedTriggerSpec) (LoaderTrigger, string, error) {
	prompt := strings.TrimSpace(trigger.Prompt)
	if prompt == "" {
		prompt = fmt.Sprintf("Run agent %s.", agentName)
	}
	callback := fmt.Sprintf("async function(event) { return scheduler.agent(%s); }", jsStringLiteral(prompt))
	switch trigger.Kind {
	case "cron":
		specJSON, err := loaderCronSpecJSON(trigger.Cron, "")
		if err != nil {
			return LoaderTrigger{}, "", err
		}
		return LoaderTrigger{
			ID:       id,
			Kind:     LoaderTriggerKindCron,
			Enabled:  true,
			SpecJSON: specJSON,
		}, fmt.Sprintf("scheduler.cron(%s, %s, %s);\n", jsStringLiteral(id), jsStringLiteral(trigger.Cron), callback), nil
	case "interval":
		interval, err := time.ParseDuration(trigger.Interval)
		if err != nil {
			return LoaderTrigger{}, "", err
		}
		intervalMs := interval.Milliseconds()
		if intervalMs <= 0 {
			return LoaderTrigger{}, "", fmt.Errorf("interval trigger %s must be at least 1ms", id)
		}
		specJSON, err := marshalJSONCompact(map[string]any{"kind": LoaderTriggerKindInterval, "interval": trigger.Interval})
		if err != nil {
			return LoaderTrigger{}, "", err
		}
		return LoaderTrigger{
			ID:         id,
			Kind:       LoaderTriggerKindInterval,
			IntervalMs: intervalMs,
			Enabled:    true,
			SpecJSON:   specJSON,
		}, fmt.Sprintf("scheduler.interval(%s, %s, %d);\n", jsStringLiteral(id), callback, intervalMs), nil
	case "timeout":
		delay, err := time.ParseDuration(trigger.Timeout)
		if err != nil {
			return LoaderTrigger{}, "", err
		}
		delayMs := delay.Milliseconds()
		if delayMs <= 0 {
			return LoaderTrigger{}, "", fmt.Errorf("timeout trigger %s must be at least 1ms", id)
		}
		specJSON, err := marshalJSONCompact(map[string]any{"kind": LoaderTriggerKindTimeout, "timeout": trigger.Timeout})
		if err != nil {
			return LoaderTrigger{}, "", err
		}
		return LoaderTrigger{
			ID:         id,
			Kind:       LoaderTriggerKindTimeout,
			IntervalMs: delayMs,
			Enabled:    true,
			SpecJSON:   specJSON,
		}, fmt.Sprintf("scheduler.timeout(%s, %s, %d);\n", jsStringLiteral(id), callback, delayMs), nil
	case "event":
		if trigger.Event == nil {
			return LoaderTrigger{}, "", fmt.Errorf("event trigger topic is required")
		}
		topic := strings.TrimSpace(trigger.Event.Topic)
		specJSON, err := marshalJSONCompact(map[string]any{"kind": LoaderTriggerKindEvent, "topic": topic})
		if err != nil {
			return LoaderTrigger{}, "", err
		}
		return LoaderTrigger{
			ID:       id,
			Kind:     LoaderTriggerKindEvent,
			Topic:    topic,
			Enabled:  true,
			SpecJSON: specJSON,
		}, fmt.Sprintf("scheduler.on(%s, %s, %s);\n", jsStringLiteral(topic), jsStringLiteral(id), callback), nil
	default:
		return LoaderTrigger{}, "", fmt.Errorf("unsupported scheduler trigger kind %q", trigger.Kind)
	}
}

func projectManagedServiceLoaderTriggerAndRegistration(projectID, serviceName string, trigger compose.NormalizedProjectTriggerSpec) (LoaderTrigger, string, error) {
	id, err := StableManagedTriggerID(projectID, projectServiceTriggerSchedulerAgentName(serviceName, trigger.Name), "", trigger.Name, 0)
	if err != nil {
		return LoaderTrigger{}, "", err
	}
	input := strings.TrimSpace(trigger.Input)
	inputExpr := "(event == null ? {} : event)"
	if input == "" {
		input = "{}"
	} else {
		inputExpr = input
	}
	if !json.Valid([]byte(input)) {
		return LoaderTrigger{}, "", fmt.Errorf("service trigger input must be valid JSON")
	}
	callback := fmt.Sprintf("async function(event) { return scheduler.service(%s, %s); }", jsStringLiteral(serviceName), inputExpr)
	switch trigger.Kind {
	case "cron":
		specJSON, err := loaderCronSpecJSON(trigger.Cron, "")
		if err != nil {
			return LoaderTrigger{}, "", err
		}
		return LoaderTrigger{ID: id, Kind: LoaderTriggerKindCron, Enabled: true, SpecJSON: specJSON}, fmt.Sprintf("scheduler.cron(%s, %s, %s);\n", jsStringLiteral(id), jsStringLiteral(trigger.Cron), callback), nil
	case "interval":
		interval, err := time.ParseDuration(trigger.Interval)
		if err != nil {
			return LoaderTrigger{}, "", err
		}
		intervalMs := interval.Milliseconds()
		if intervalMs <= 0 {
			return LoaderTrigger{}, "", fmt.Errorf("interval trigger %s must be at least 1ms", id)
		}
		specJSON, err := marshalJSONCompact(map[string]any{"kind": LoaderTriggerKindInterval, "interval": trigger.Interval})
		if err != nil {
			return LoaderTrigger{}, "", err
		}
		return LoaderTrigger{ID: id, Kind: LoaderTriggerKindInterval, IntervalMs: intervalMs, Enabled: true, SpecJSON: specJSON}, fmt.Sprintf("scheduler.interval(%s, %s, %d);\n", jsStringLiteral(id), callback, intervalMs), nil
	case "timeout":
		delay, err := time.ParseDuration(trigger.Timeout)
		if err != nil {
			return LoaderTrigger{}, "", err
		}
		delayMs := delay.Milliseconds()
		if delayMs <= 0 {
			return LoaderTrigger{}, "", fmt.Errorf("timeout trigger %s must be at least 1ms", id)
		}
		specJSON, err := marshalJSONCompact(map[string]any{"kind": LoaderTriggerKindTimeout, "timeout": trigger.Timeout})
		if err != nil {
			return LoaderTrigger{}, "", err
		}
		return LoaderTrigger{ID: id, Kind: LoaderTriggerKindTimeout, IntervalMs: delayMs, Enabled: true, SpecJSON: specJSON}, fmt.Sprintf("scheduler.timeout(%s, %s, %d);\n", jsStringLiteral(id), callback, delayMs), nil
	case "event":
		if trigger.Event == nil {
			return LoaderTrigger{}, "", fmt.Errorf("event trigger topic is required")
		}
		topic := strings.TrimSpace(trigger.Event.Topic)
		specJSON, err := marshalJSONCompact(map[string]any{"kind": LoaderTriggerKindEvent, "topic": topic})
		if err != nil {
			return LoaderTrigger{}, "", err
		}
		return LoaderTrigger{ID: id, Kind: LoaderTriggerKindEvent, Topic: topic, Enabled: true, SpecJSON: specJSON}, fmt.Sprintf("scheduler.on(%s, %s, %s);\n", jsStringLiteral(topic), jsStringLiteral(id), callback), nil
	default:
		return LoaderTrigger{}, "", fmt.Errorf("unsupported service trigger kind %q", trigger.Kind)
	}
}

func jsStringLiteral(value string) string {
	data, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(data)
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

func projectDiffChanges(current ProjectRecord, incoming ProjectRecord, currentAgents []ProjectAgentRecord, incomingAgents []ProjectAgentRecord, currentSchedulers []ProjectSchedulerRecord, incomingSchedulers []ProjectSchedulerRecord) []*agentcomposev2.ProjectChange {
	changes := []*agentcomposev2.ProjectChange{{
		Action:       projectChangeAction(projectRecordUnchanged(current, incoming)),
		ResourceType: "project",
		ResourceId:   current.ID,
		Name:         incoming.Name,
	}}

	currentAgentByName := make(map[string]ProjectAgentRecord, len(currentAgents))
	for _, agent := range currentAgents {
		currentAgentByName[agent.AgentName] = agent
	}
	seenAgents := make(map[string]struct{}, len(incomingAgents))
	for _, agent := range incomingAgents {
		seenAgents[agent.AgentName] = struct{}{}
		existing, found := currentAgentByName[agent.AgentName]
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentChangeAction(existing, found, agent),
			ResourceType: "project_agent",
			ResourceId:   agent.ManagedAgentID,
			Name:         agent.AgentName,
		})
	}
	for _, agent := range currentAgents {
		if _, ok := seenAgents[agent.AgentName]; ok {
			continue
		}
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_REMOVED,
			ResourceType: "project_agent",
			ResourceId:   agent.ManagedAgentID,
			Name:         agent.AgentName,
		})
	}

	currentSchedulerByID := make(map[string]ProjectSchedulerRecord, len(currentSchedulers))
	for _, scheduler := range currentSchedulers {
		currentSchedulerByID[scheduler.SchedulerID] = scheduler
	}
	seenSchedulers := make(map[string]struct{}, len(incomingSchedulers))
	for _, scheduler := range incomingSchedulers {
		seenSchedulers[scheduler.SchedulerID] = struct{}{}
		existing, found := currentSchedulerByID[scheduler.SchedulerID]
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       schedulerChangeAction(existing, found, scheduler),
			ResourceType: "project_scheduler",
			ResourceId:   scheduler.SchedulerID,
			Name:         scheduler.AgentName,
		})
	}
	for _, scheduler := range currentSchedulers {
		if _, ok := seenSchedulers[scheduler.SchedulerID]; ok {
			continue
		}
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_REMOVED,
			ResourceType: "project_scheduler",
			ResourceId:   scheduler.SchedulerID,
			Name:         scheduler.AgentName,
		})
	}
	return changes
}

func projectChangeAction(unchanged bool) agentcomposev2.ProjectChangeAction {
	if unchanged {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED
	}
	return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED
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

func projectResponse(project ProjectRecord, spec *agentcomposev2.ProjectSpec, agents []ProjectAgentRecord, schedulers []ProjectSchedulerRecord) *agentcomposev2.Project {
	return &agentcomposev2.Project{
		Summary:    projectSummaryResponse(project, agents, schedulers),
		Spec:       spec,
		Agents:     projectAgentResponses(agents),
		Schedulers: projectSchedulerResponses(schedulers),
	}
}

func projectSummaryResponse(project ProjectRecord, agents []ProjectAgentRecord, schedulers []ProjectSchedulerRecord) *agentcomposev2.ProjectSummary {
	return &agentcomposev2.ProjectSummary{
		ProjectId:       project.ID,
		Name:            project.Name,
		SourcePath:      project.SourcePath,
		CurrentRevision: uint64(project.CurrentRevision),
		SpecHash:        project.SpecHash,
		AgentCount:      uint32(len(agents)),
		SchedulerCount:  uint32(len(schedulers)),
		CreatedAt:       formatProjectTime(project.CreatedAt),
		UpdatedAt:       formatProjectTime(project.UpdatedAt),
		RemovedAt:       formatProjectTime(project.RemovedAt),
	}
}

func projectRevisionResponse(revision ProjectRevisionRecord, spec *agentcomposev2.ProjectSpec) *agentcomposev2.ProjectRevision {
	return &agentcomposev2.ProjectRevision{
		ProjectId: revision.ProjectID,
		Revision:  uint64(revision.Revision),
		SpecHash:  revision.SpecHash,
		Spec:      spec,
		CreatedAt: formatProjectTime(revision.CreatedAt),
	}
}

func projectAgentResponses(agents []ProjectAgentRecord) []*agentcomposev2.ProjectAgent {
	items := make([]*agentcomposev2.ProjectAgent, 0, len(agents))
	for _, agent := range agents {
		items = append(items, &agentcomposev2.ProjectAgent{
			ProjectId:        agent.ProjectID,
			AgentName:        agent.AgentName,
			ManagedAgentId:   agent.ManagedAgentID,
			Provider:         agent.Provider,
			Model:            agent.Model,
			Image:            agent.Image,
			Driver:           agent.Driver,
			SchedulerEnabled: agent.SchedulerEnabled,
		})
	}
	return items
}

func projectSchedulerResponses(schedulers []ProjectSchedulerRecord) []*agentcomposev2.ProjectScheduler {
	items := make([]*agentcomposev2.ProjectScheduler, 0, len(schedulers))
	for _, scheduler := range schedulers {
		items = append(items, &agentcomposev2.ProjectScheduler{
			ProjectId:       scheduler.ProjectID,
			AgentName:       scheduler.AgentName,
			SchedulerId:     scheduler.SchedulerID,
			ManagedLoaderId: scheduler.ManagedLoaderID,
			Enabled:         scheduler.Enabled,
			TriggerCount:    uint32(scheduler.TriggerCount),
		})
	}
	return items
}

// ProjectSpecResponse converts a normalized compose spec into the v2 ProjectSpec API shape.
func ProjectSpecResponse(spec *compose.NormalizedProjectSpec) *agentcomposev2.ProjectSpec {
	if spec == nil {
		return nil
	}
	return &agentcomposev2.ProjectSpec{
		Name:        spec.Name,
		Metadata:    metadataResponse(spec.Metadata),
		Variables:   envVarResponses(spec.Variables),
		Runtime:     runtimeResponse(spec.Runtime),
		Workspace:   workspaceResponse(spec.Workspace),
		Agents:      agentSpecResponses(spec.Agents),
		Network:     networkResponse(spec.Network),
		Services:    serviceSpecResponses(spec.Services),
		Triggers:    projectTriggerSpecResponses(spec.Triggers),
		Permissions: permissionResponse(spec.Permissions),
		Artifacts:   artifactPolicyResponse(spec.Artifacts),
	}
}

func metadataResponse(metadata *compose.ProjectMetadataSpec) *agentcomposev2.ProjectMetadata {
	if metadata == nil {
		return nil
	}
	return &agentcomposev2.ProjectMetadata{
		Name:        metadata.Name,
		Labels:      cloneStringMap(metadata.Labels),
		Annotations: cloneStringMap(metadata.Annotations),
	}
}

func runtimeResponse(runtime *compose.NormalizedProjectRuntimeSpec) *agentcomposev2.RuntimeSpec {
	if runtime == nil {
		return nil
	}
	return &agentcomposev2.RuntimeSpec{
		Driver:        runtime.Driver,
		Image:         runtime.Image,
		Env:           envVarResponses(runtime.Env),
		Resources:     cloneStringMap(runtime.Resources),
		Network:       networkResponse(runtime.Network),
		CleanupPolicy: cleanupPolicyResponse(runtime.CleanupPolicy),
	}
}

func agentSpecResponses(agents []compose.NormalizedAgentSpec) []*agentcomposev2.AgentSpec {
	items := make([]*agentcomposev2.AgentSpec, 0, len(agents))
	for _, agent := range agents {
		items = append(items, &agentcomposev2.AgentSpec{
			Name:         agent.Name,
			Provider:     agent.Provider,
			Model:        agent.Model,
			SystemPrompt: agent.SystemPrompt,
			Image:        agent.Image,
			Driver:       driverResponse(agent.Driver),
			Env:          envVarResponses(agent.Env),
			CapsetIds:    normalizeCapsetIDs(agent.CapsetIDs),
			Workspace:    workspaceResponse(agent.Workspace),
			Scheduler:    schedulerResponse(agent.Scheduler),
		})
	}
	return items
}

func serviceSpecResponses(services []compose.NormalizedServiceSpec) []*agentcomposev2.ServiceSpec {
	items := make([]*agentcomposev2.ServiceSpec, 0, len(services))
	for _, service := range services {
		items = append(items, &agentcomposev2.ServiceSpec{
			Name:             service.Name,
			Description:      service.Description,
			Runtime:          service.Runtime,
			Entry:            service.Entry,
			InputSchemaJson:  service.InputSchema,
			OutputSchemaJson: service.OutputSchema,
			ErrorSchemaJson:  service.ErrorSchema,
			TimeoutMs:        durationStringToMillis(service.Timeout),
			Retry:            retryPolicyResponse(service.Retry),
			Permissions:      permissionResponse(service.Permissions),
			Env:              envVarResponses(service.Env),
			Agents:           append([]string(nil), service.Agents...),
			Examples:         serviceExampleResponses(service.Examples),
		})
	}
	return items
}

func retryPolicyResponse(retry *compose.RetryPolicySpec) *agentcomposev2.RetryPolicySpec {
	if retry == nil {
		return nil
	}
	return &agentcomposev2.RetryPolicySpec{MaxAttempts: uint32(retry.MaxAttempts), BackoffMs: durationStringToMillis(retry.Backoff)}
}

func serviceExampleResponses(examples []compose.ServiceExampleSpec) []*agentcomposev2.ServiceExampleSpec {
	items := make([]*agentcomposev2.ServiceExampleSpec, 0, len(examples))
	for _, example := range examples {
		items = append(items, &agentcomposev2.ServiceExampleSpec{Name: example.Name, InputJson: example.Input, OutputJson: example.Output})
	}
	return items
}

func projectTriggerSpecResponses(triggers []compose.NormalizedProjectTriggerSpec) []*agentcomposev2.ProjectTriggerSpec {
	items := make([]*agentcomposev2.ProjectTriggerSpec, 0, len(triggers))
	for _, trigger := range triggers {
		items = append(items, &agentcomposev2.ProjectTriggerSpec{
			Name:      trigger.Name,
			Type:      trigger.Kind,
			Cron:      trigger.Cron,
			Interval:  trigger.Interval,
			Timeout:   trigger.Timeout,
			Event:     eventTriggerResponse(trigger.Event),
			Webhook:   webhookTriggerResponse(trigger.Webhook),
			Target:    triggerTargetResponse(&trigger.Target),
			InputJson: trigger.Input,
		})
	}
	return items
}

func eventTriggerResponse(event *compose.EventTriggerSpec) *agentcomposev2.EventTriggerSpec {
	if event == nil {
		return nil
	}
	return &agentcomposev2.EventTriggerSpec{Topic: event.Topic}
}

func webhookTriggerResponse(webhook *compose.WebhookTriggerSpec) *agentcomposev2.WebhookTriggerSpec {
	if webhook == nil {
		return nil
	}
	return &agentcomposev2.WebhookTriggerSpec{Topic: webhook.Topic, Method: webhook.Method}
}

func triggerTargetResponse(target *compose.TriggerTargetSpec) *agentcomposev2.TriggerTargetSpec {
	if target == nil || (strings.TrimSpace(target.Service) == "" && strings.TrimSpace(target.Agent) == "") {
		return nil
	}
	return &agentcomposev2.TriggerTargetSpec{ServiceName: target.Service, AgentName: target.Agent}
}

func permissionResponse(permission *compose.PermissionSpec) *agentcomposev2.PermissionSpec {
	if permission == nil {
		return nil
	}
	return &agentcomposev2.PermissionSpec{
		Agents:       append([]string(nil), permission.Agents...),
		Capabilities: append([]string(nil), permission.Capabilities...),
		Resources:    append([]string(nil), permission.Resources...),
	}
}

func artifactPolicyResponse(policy *compose.ArtifactPolicySpec) *agentcomposev2.ArtifactPolicySpec {
	if policy == nil {
		return nil
	}
	return &agentcomposev2.ArtifactPolicySpec{Retention: policy.Retention, Storage: policy.Storage}
}

func durationStringToMillis(value string) uint32 {
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || duration <= 0 {
		return 0
	}
	return uint32(duration.Milliseconds())
}

func cleanupPolicyResponse(value string) agentcomposev2.RunSessionCleanupPolicy {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "keep_running", "keep-running", "keep":
		return agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_KEEP_RUNNING
	case "stop", "stop_on_completion", "stop-on-completion":
		return agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_STOP_ON_COMPLETION
	default:
		return agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_UNSPECIFIED
	}
}

func envVarResponses(values map[string]compose.EnvVarSpec) []*agentcomposev2.EnvVarSpec {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	items := make([]*agentcomposev2.EnvVarSpec, 0, len(values))
	for _, name := range names {
		value := values[name]
		items = append(items, &agentcomposev2.EnvVarSpec{Name: name, Value: value.Value, Secret: value.Secret})
	}
	return items
}

func workspaceResponse(workspace *compose.WorkspaceSpec) *agentcomposev2.WorkspaceSpec {
	if workspace == nil {
		return nil
	}
	return &agentcomposev2.WorkspaceSpec{
		Provider: workspace.Provider,
		Url:      workspace.URL,
		Branch:   workspace.Branch,
		Path:     workspace.Path,
	}
}

func networkResponse(network *compose.NetworkSpec) *agentcomposev2.NetworkSpec {
	if network == nil {
		return nil
	}
	return &agentcomposev2.NetworkSpec{Mode: network.Mode}
}

func driverResponse(driver *compose.NormalizedDriverSpec) *agentcomposev2.DriverSpec {
	if driver == nil {
		return nil
	}
	result := &agentcomposev2.DriverSpec{Name: driver.Name}
	switch driver.Name {
	case compose.DriverBoxlite:
		result.Boxlite = &agentcomposev2.BoxliteDriverSpec{}
		if driver.Boxlite != nil {
			result.Boxlite.Kernel = driver.Boxlite.Kernel
			result.Boxlite.Rootfs = driver.Boxlite.Rootfs
		}
	case compose.DriverDocker:
		result.Docker = &agentcomposev2.DockerDriverSpec{}
		if driver.Docker != nil {
			result.Docker.Host = driver.Docker.Host
		}
	case compose.DriverMicrosandbox:
		result.Microsandbox = &agentcomposev2.MicrosandboxDriverSpec{}
		if driver.Microsandbox != nil {
			result.Microsandbox.Profile = driver.Microsandbox.Profile
		}
	}
	return result
}

func schedulerResponse(scheduler *compose.NormalizedSchedulerSpec) *agentcomposev2.SchedulerSpec {
	if scheduler == nil {
		return nil
	}
	triggers := make([]*agentcomposev2.TriggerSpec, 0, len(scheduler.Triggers))
	for _, trigger := range scheduler.Triggers {
		triggers = append(triggers, triggerResponse(trigger))
	}
	return &agentcomposev2.SchedulerSpec{
		Enabled:  scheduler.Enabled,
		Triggers: triggers,
		Script:   scheduler.Script,
	}
}

func triggerResponse(trigger compose.NormalizedTriggerSpec) *agentcomposev2.TriggerSpec {
	result := &agentcomposev2.TriggerSpec{
		Name:   trigger.Name,
		Kind:   trigger.Kind,
		Prompt: trigger.Prompt,
	}
	switch trigger.Kind {
	case "cron":
		result.Cron = trigger.Cron
	case "interval":
		result.Interval = trigger.Interval
	case "timeout":
		result.Timeout = trigger.Timeout
	case "event":
		result.Event = &agentcomposev2.EventTriggerSpec{}
		if trigger.Event != nil {
			result.Event.Topic = trigger.Event.Topic
		}
	}
	return result
}

func formatProjectTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
