package projects

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"agent-compose/pkg/capability"
	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/images"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
)

var (
	ErrInvalidRequest = errors.New("invalid project request")
	ErrUnavailable    = errors.New("project dependency unavailable")
	ErrUnimplemented  = errors.New("project operation unimplemented")
)

type ValidationIssue struct {
	Severity ValidationSeverity
	Path     string
	Message  string
}

type ValidationSeverity string

const (
	ValidationSeverityError   ValidationSeverity = "error"
	ValidationSeverityWarning ValidationSeverity = "warning"
)

func HasValidationErrors(issues []ValidationIssue) bool {
	for _, issue := range issues {
		if issue.Severity != ValidationSeverityWarning {
			return true
		}
	}
	return false
}

type CapabilityGatewaySource interface {
	GetCapabilityGateway(context.Context) (domain.CapabilityGatewaySettings, error)
}

type NormalizedProject struct {
	Spec       *compose.NormalizedProjectSpec
	SpecHash   string
	SourcePath string

	// managedLoaderOverrides is populated only by the legacy-v1 compatibility
	// projection. It lets that boundary adopt an existing loader so identity,
	// history, runtime overrides, and task-local environment survive migration.
	managedLoaderOverrides map[string]legacyManagedLoaderOverride
}

type ProjectRef struct {
	ProjectID  string
	Name       string
	SourcePath string
}

type ControllerStore interface {
	GetProject(context.Context, string) (domain.ProjectRecord, error)
	GetProjectIfExists(context.Context, string, bool) (domain.ProjectRecord, bool, error)
	ListProjects(context.Context, domain.ProjectListOptions) (domain.ProjectListResult, error)
	UpsertProject(context.Context, domain.ProjectRecord) (domain.ProjectRecord, error)
	MarkProjectRemoved(context.Context, string) (domain.ProjectRecord, error)
	SaveProjectRevision(context.Context, domain.ProjectRevisionRecord) (domain.ProjectRevisionRecord, bool, error)
	GetProjectAgent(context.Context, string, string) (domain.ProjectAgentRecord, error)
	UpsertProjectAgent(context.Context, domain.ProjectAgentRecord) (domain.ProjectAgentRecord, error)
	ListProjectAgents(context.Context, string) ([]domain.ProjectAgentRecord, error)
	ListProjectSchedulers(context.Context, string) ([]domain.ProjectSchedulerRecord, error)
	ReconcileAgentDefinitionStore
	ReconcileSchedulerStore
	DownStore
}

type SandboxStore interface {
	DownSandboxStore
}

type LoaderValidator interface {
	Validate(ctx context.Context, runtime, script string) (loaders.LoaderValidationResult, error)
	Refresh(ctx context.Context) error
}

type VolumeManager interface {
	Ensure(ctx context.Context, item domain.VolumeRecord) (domain.VolumeRecord, bool, error)
	Inspect(ctx context.Context, nameOrID string) (domain.VolumeRecord, error)
	ReplaceProjectVolumes(ctx context.Context, projectID string, links map[string]domain.ProjectVolumeLink) error
	RemoveProjectVolumes(ctx context.Context, projectID string) error
}

type Controller struct {
	config    *appconfig.Config
	store     ControllerStore
	sandboxes SandboxStore
	images    images.Backend
	loaders   LoaderValidator
	volumes   VolumeManager
	gateway   CapabilityGatewaySource
	stop      func(context.Context, *domain.Sandbox) error
	defaultDR string
}

type ControllerDependencies struct {
	Config      *appconfig.Config
	Store       ControllerStore
	Sandboxes   SandboxStore
	Images      images.Backend
	Loaders     LoaderValidator
	Volumes     VolumeManager
	Gateway     CapabilityGatewaySource
	StopSandbox func(context.Context, *domain.Sandbox) error
}

func NewController(deps ControllerDependencies) *Controller {
	defaultDriver := driverpkg.RuntimeDriverDocker
	if deps.Config != nil && strings.TrimSpace(deps.Config.RuntimeDriver) != "" {
		defaultDriver = deps.Config.RuntimeDriver
	}
	return &Controller{
		config:    deps.Config,
		store:     deps.Store,
		sandboxes: deps.Sandboxes,
		images:    deps.Images,
		loaders:   deps.Loaders,
		volumes:   deps.Volumes,
		gateway:   deps.Gateway,
		stop:      deps.StopSandbox,
		defaultDR: defaultDriver,
	}
}

type ValidateResult struct {
	Valid    bool
	Issues   []ValidationIssue
	SpecHash string
}

func (c *Controller) ValidateProject(ctx context.Context, normalized NormalizedProject, issues []ValidationIssue) (ValidateResult, error) {
	if HasValidationErrors(issues) {
		return ValidateResult{Valid: false, Issues: issues, SpecHash: normalized.SpecHash}, nil
	}
	if normalized.Spec == nil {
		return ValidateResult{Valid: false, Issues: []ValidationIssue{{Path: "spec", Message: "project spec is required"}}}, nil
	}
	if issues := c.validateManagedAgentDefinitions(normalized); len(issues) > 0 {
		return ValidateResult{Valid: false, Issues: issues, SpecHash: normalized.SpecHash}, nil
	}
	if issues := c.validateManagedSchedulers(ctx, normalized); len(issues) > 0 {
		return ValidateResult{Valid: false, Issues: issues, SpecHash: normalized.SpecHash}, nil
	}
	warnings, err := c.capabilityGatewayWarnings(ctx, normalized.Spec)
	if err != nil {
		return ValidateResult{}, err
	}
	return ValidateResult{Valid: true, Issues: append(issues, warnings...), SpecHash: normalized.SpecHash}, nil
}

type ApplyRequest struct {
	Normalized NormalizedProject
	Issues     []ValidationIssue
	DryRun     bool
}

type ApplyResult struct {
	Project      domain.ProjectRecord
	Revision     domain.ProjectRevisionRecord
	Agents       []domain.ProjectAgentRecord
	Schedulers   []domain.ProjectSchedulerRecord
	Changes      []Change
	Issues       []ValidationIssue
	Applied      bool
	Unchanged    bool
	RevisionSpec *compose.NormalizedProjectSpec
}

func (c *Controller) ApplyProject(ctx context.Context, req ApplyRequest) (ApplyResult, error) {
	normalized := req.Normalized
	if HasValidationErrors(req.Issues) {
		return ApplyResult{Issues: req.Issues, RevisionSpec: normalized.Spec}, nil
	}
	if c.store == nil {
		return ApplyResult{}, fmt.Errorf("apply project: config store is required")
	}
	project, err := NewRecordFromSpec(normalized.Spec, normalized.SourcePath)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("%w: apply project: %w", ErrInvalidRequest, err)
	}
	normalized, err = c.preserveLegacyManagedLoaderIdentities(ctx, project, normalized)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("apply project %s: preserve legacy scheduler identities: %w", normalized.Spec.Name, err)
	}
	if issues := c.validateManagedAgentDefinitions(normalized); len(issues) > 0 {
		return ApplyResult{Issues: issues, RevisionSpec: normalized.Spec}, nil
	}
	if issues := c.validateManagedSchedulers(ctx, normalized); len(issues) > 0 {
		return ApplyResult{Issues: issues, RevisionSpec: normalized.Spec}, nil
	}
	warnings, err := c.capabilityGatewayWarnings(ctx, normalized.Spec)
	if err != nil {
		return ApplyResult{}, err
	}
	agentRecords, agentDefinitions, schedulerRecords, managedLoaders, err := c.projectArtifacts(ctx, project, 0, normalized)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("%w: apply project %s: %w", ErrInvalidRequest, normalized.Spec.Name, err)
	}
	if req.DryRun {
		return ApplyResult{
			Project:      project,
			Revision:     domain.ProjectRevisionRecord{ProjectID: project.ID, SpecHash: normalized.SpecHash},
			Agents:       agentRecords,
			Schedulers:   schedulerRecords,
			Changes:      dryRunChanges(project, agentRecords, agentDefinitions, schedulerRecords, managedLoaders),
			Applied:      false,
			Issues:       append(req.Issues, warnings...),
			RevisionSpec: normalized.Spec,
		}, nil
	}
	if err := images.EnsureProjectAgentImages(ctx, c.config, c.images, normalized.Spec.Name, agentRecords); err != nil {
		return ApplyResult{}, fmt.Errorf("%w: apply project %s: %w", ErrUnavailable, normalized.Spec.Name, err)
	}
	if err := c.ensureProjectVolumes(ctx, project, normalized.Spec); err != nil {
		return ApplyResult{}, fmt.Errorf("%w: apply project %s: %w", ErrInvalidRequest, normalized.Spec.Name, err)
	}

	existingProject, projectFound, err := c.store.GetProjectIfExists(ctx, project.ID, true)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("apply project %s: load existing project: %w", normalized.Spec.Name, err)
	}
	project, err = c.store.UpsertProject(ctx, project)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("apply project %s: upsert project: %w", normalized.Spec.Name, err)
	}
	specJSON, err := normalized.Spec.MarshalCanonicalJSON(false)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("apply project %s: marshal project spec: %w", normalized.Spec.Name, err)
	}
	revision, revisionCreated, err := c.store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{
		ProjectID: project.ID,
		SpecHash:  normalized.SpecHash,
		SpecJSON:  string(specJSON),
	})
	if err != nil {
		return ApplyResult{}, fmt.Errorf("apply project %s: save revision: %w", normalized.Spec.Name, err)
	}
	project, err = c.store.GetProject(ctx, project.ID)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("apply project %s: reload project: %w", normalized.Spec.Name, err)
	}

	agentRecords, agentDefinitions, schedulerRecords, managedLoaders, err = c.projectArtifacts(ctx, project, revision.Revision, normalized)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("%w: apply project %s: %w", ErrInvalidRequest, normalized.Spec.Name, err)
	}
	changes := applyChanges(project, existingProject, projectFound, revision, revisionCreated)
	agentsUnchanged := true
	for _, agent := range agentRecords {
		existingAgent, found, err := c.getProjectAgentIfExists(ctx, project.ID, agent.AgentName)
		if err != nil {
			return ApplyResult{}, fmt.Errorf("apply project %s: load agent %s: %w", normalized.Spec.Name, agent.AgentName, err)
		}
		if _, err := c.store.UpsertProjectAgent(ctx, agent); err != nil {
			return ApplyResult{}, fmt.Errorf("apply project %s: upsert agent %s: %w", normalized.Spec.Name, agent.AgentName, err)
		}
		action := ProjectAgentChangeAction(existingAgent, found, agent)
		if action != ChangeActionUnchanged {
			agentsUnchanged = false
		}
		changes = append(changes, Change{
			Action:       action,
			ResourceType: "project_agent",
			ResourceID:   agent.ManagedAgentID,
			Name:         agent.AgentName,
		})
	}
	agentDefinitionChanges, agentDefinitionsUnchanged, err := ReconcileManagedAgentDefinitions(ctx, c.store, project, agentDefinitions)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("apply project %s: %w", normalized.Spec.Name, err)
	}
	if !agentDefinitionsUnchanged {
		agentsUnchanged = false
	}
	changes = append(changes, agentDefinitionChanges...)
	schedulerChanges, schedulersUnchanged, err := ReconcileManagedSchedulers(ctx, c.store, project, schedulerRecords, managedLoaders, ReconcileSchedulerOptions{
		CleanupFailedManagedScheduler: c.cleanupFailedManagedSchedulerReconcile,
		DisableManagedLoaderIfOwned:   c.disableManagedLoaderIfOwned,
		RefreshLoaders:                c.refreshLoaders,
	})
	if err != nil {
		changes = append(changes, schedulerChanges...)
		agents, listAgentsErr := c.store.ListProjectAgents(ctx, project.ID)
		if listAgentsErr != nil {
			return ApplyResult{}, fmt.Errorf("apply project %s: %w; list project agents after reconcile failure: %v", normalized.Spec.Name, err, listAgentsErr)
		}
		schedulers, listSchedulersErr := c.store.ListProjectSchedulers(ctx, project.ID)
		if listSchedulersErr != nil {
			return ApplyResult{}, fmt.Errorf("apply project %s: %w; list project schedulers after reconcile failure: %v", normalized.Spec.Name, err, listSchedulersErr)
		}
		return ApplyResult{
			Project:      project,
			Revision:     revision,
			Agents:       agents,
			Schedulers:   schedulers,
			Changes:      changes,
			Issues:       append(append(req.Issues, warnings...), ValidationIssue{Path: "reconcile.schedulers", Message: fmt.Sprintf("apply project %s: %v", normalized.Spec.Name, err)}),
			Applied:      false,
			Unchanged:    false,
			RevisionSpec: normalized.Spec,
		}, nil
	}
	if !schedulersUnchanged {
		agentsUnchanged = false
	}
	changes = append(changes, schedulerChanges...)

	agents, err := c.store.ListProjectAgents(ctx, project.ID)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("apply project %s: list project agents: %w", normalized.Spec.Name, err)
	}
	schedulers, err := c.store.ListProjectSchedulers(ctx, project.ID)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("apply project %s: list project schedulers: %w", normalized.Spec.Name, err)
	}
	return ApplyResult{
		Project:      project,
		Revision:     revision,
		Agents:       agents,
		Schedulers:   schedulers,
		Changes:      changes,
		Issues:       append(req.Issues, warnings...),
		Applied:      true,
		Unchanged:    projectFound && !revisionCreated && ProjectRecordUnchanged(existingProject, project) && agentsUnchanged,
		RevisionSpec: normalized.Spec,
	}, nil
}

func (c *Controller) capabilityGatewayWarnings(ctx context.Context, spec *compose.NormalizedProjectSpec) ([]ValidationIssue, error) {
	if c.gateway == nil || !usesGlobalCapabilityGateway(spec) {
		return nil, nil
	}
	settings, err := c.gateway.GetCapabilityGateway(ctx)
	if err != nil {
		return nil, fmt.Errorf("check global OctoBus configuration: %w", err)
	}
	if strings.TrimSpace(settings.Addr) != "" {
		return nil, nil
	}
	return []ValidationIssue{{
		Severity: ValidationSeverityWarning,
		Path:     "agents.capset_ids",
		Message:  "unqualified capset IDs require the daemon global OctoBus, but it is not configured",
	}}, nil
}

func usesGlobalCapabilityGateway(spec *compose.NormalizedProjectSpec) bool {
	if spec == nil {
		return false
	}
	for _, agent := range spec.Agents {
		for _, declaration := range agent.CapsetIDs {
			parsed, err := capability.ParseCapsetDeclaration(declaration)
			if err == nil && !parsed.Qualified() {
				return true
			}
		}
	}
	return false
}

type RemoveRequest struct {
	Project       ProjectRef
	RemoveHistory bool
}

type RemoveResult struct {
	Project    domain.ProjectRecord
	Agents     []domain.ProjectAgentRecord
	Schedulers []domain.ProjectSchedulerRecord
	Changes    []Change
}

func (c *Controller) RemoveProject(ctx context.Context, req RemoveRequest) (RemoveResult, error) {
	if c.store == nil {
		return RemoveResult{}, fmt.Errorf("config store is required")
	}
	if req.RemoveHistory {
		return RemoveResult{}, ErrUnimplemented
	}
	project, err := c.resolveProjectRef(ctx, req.Project, true)
	if err != nil {
		return RemoveResult{}, err
	}
	downChanges, err := DownProject(ctx, project, DownOptions{
		Store:                c.store,
		Sandboxes:            c.sandboxes,
		DisableManagedLoader: c.disableManagedLoaderIfOwned,
		RefreshLoaders:       c.refreshLoaders,
		StopSandbox:          c.stop,
	})
	changes := downChangesToChanges(downChanges)
	if err != nil {
		return RemoveResult{Project: project, Changes: changes}, err
	}
	if project.RemovedAt.IsZero() {
		removedProject, err := c.store.MarkProjectRemoved(ctx, project.ID)
		if err != nil {
			return RemoveResult{Project: project, Changes: changes}, err
		}
		project = removedProject
		changes = append(changes, Change{
			Action:       ChangeActionRemoved,
			ResourceType: "project",
			ResourceID:   project.ID,
			Name:         project.Name,
			Message:      "removed by project down",
		})
		if c.volumes != nil {
			if err := c.volumes.RemoveProjectVolumes(ctx, project.ID); err != nil {
				return RemoveResult{Project: project, Changes: changes}, err
			}
		}
	}
	agents, err := c.store.ListProjectAgents(ctx, project.ID)
	if err != nil {
		return RemoveResult{}, err
	}
	schedulers, err := c.store.ListProjectSchedulers(ctx, project.ID)
	if err != nil {
		return RemoveResult{}, err
	}
	return RemoveResult{Project: project, Agents: agents, Schedulers: schedulers, Changes: changes}, nil
}

func (c *Controller) ResolveProjectRef(ctx context.Context, ref ProjectRef) (domain.ProjectRecord, error) {
	return c.resolveProjectRef(ctx, ref, false)
}

func (c *Controller) resolveProjectRef(ctx context.Context, ref ProjectRef, includeRemoved bool) (domain.ProjectRecord, error) {
	if c.store == nil {
		return domain.ProjectRecord{}, fmt.Errorf("config store is required")
	}
	if projectID := strings.TrimSpace(ref.ProjectID); projectID != "" {
		if includeRemoved {
			project, found, err := c.store.GetProjectIfExists(ctx, projectID, true)
			if err != nil {
				return domain.ProjectRecord{}, err
			}
			if found {
				return project, nil
			}
			return domain.ProjectRecord{}, domain.ResourceError(domain.ErrNotFound, "project", projectID, fmt.Sprintf("project %s not found", projectID), sql.ErrNoRows)
		}
		return c.store.GetProject(ctx, projectID)
	}
	name := strings.TrimSpace(ref.Name)
	sourcePath := strings.TrimSpace(ref.SourcePath)
	if name != "" && sourcePath != "" {
		projectID, err := domain.StableProjectID(name, sourcePath)
		if err != nil {
			return domain.ProjectRecord{}, err
		}
		if includeRemoved {
			project, found, err := c.store.GetProjectIfExists(ctx, projectID, true)
			if err != nil {
				return domain.ProjectRecord{}, err
			}
			if found {
				return project, nil
			}
			return domain.ProjectRecord{}, domain.ResourceError(domain.ErrNotFound, "project", projectID, fmt.Sprintf("project %s not found", projectID), sql.ErrNoRows)
		}
		return c.store.GetProject(ctx, projectID)
	}
	if name == "" {
		return domain.ProjectRecord{}, domain.ClassifyError(domain.ErrRequired, "project id or name is required", nil)
	}
	result, err := c.store.ListProjects(ctx, domain.ProjectListOptions{Query: name, IncludeRemoved: includeRemoved, Limit: 200})
	if err != nil {
		return domain.ProjectRecord{}, err
	}
	var matches []domain.ProjectRecord
	for _, project := range result.Projects {
		if project.Name == name {
			matches = append(matches, project)
		}
	}
	if len(matches) == 0 {
		return domain.ProjectRecord{}, domain.ResourceError(domain.ErrNotFound, "project", name, fmt.Sprintf("project %s not found", name), sql.ErrNoRows)
	}
	if len(matches) > 1 {
		return domain.ProjectRecord{}, domain.ClassifyError(domain.ErrAmbiguous, fmt.Sprintf("project name %s is ambiguous; use project_id or source_path", name), nil)
	}
	return matches[0], nil
}

func (c *Controller) projectArtifacts(ctx context.Context, project domain.ProjectRecord, revision int64, normalized NormalizedProject) ([]domain.ProjectAgentRecord, []domain.AgentDefinition, []domain.ProjectSchedulerRecord, []domain.Loader, error) {
	spec := normalized.Spec
	agentRecords, err := NewAgentRecordsFromSpec(project.ID, revision, spec)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	agentDefinitions, err := NewAgentDefinitionsFromSpec(project, revision, spec)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if err := c.bindLegacyFileWorkspaces(ctx, project, spec, agentDefinitions); err != nil {
		return nil, nil, nil, nil, err
	}
	schedulerRecords, managedLoaders, err := c.projectManagedSchedulersFromSpec(ctx, project, revision, spec)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	schedulerRecords, managedLoaders, err = applyManagedLoaderOverrides(project, revision, schedulerRecords, managedLoaders, normalized.managedLoaderOverrides)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	syncProjectAgentSchedulerState(agentRecords, schedulerRecords)
	return agentRecords, agentDefinitions, schedulerRecords, managedLoaders, nil
}

func (c *Controller) projectManagedSchedulersFromSpec(ctx context.Context, project domain.ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]domain.ProjectSchedulerRecord, []domain.Loader, error) {
	builds, err := c.projectManagedSchedulerBuildsFromSpec(ctx, project, revision, spec)
	if err != nil {
		return nil, nil, err
	}
	return SchedulerRecords(builds), SchedulerLoaders(builds), nil
}

func (c *Controller) projectManagedSchedulerBuildsFromSpec(ctx context.Context, project domain.ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]SchedulerBuild, error) {
	builds, err := NewSchedulerBuildsFromSpec(project, revision, spec)
	if err != nil {
		return nil, err
	}
	inlineScripts := make(map[string]string, len(spec.Agents))
	for _, agent := range spec.Agents {
		if agent.Scheduler == nil {
			continue
		}
		if agent.Scheduler.HasScript() {
			inlineScripts[agent.Name] = agent.Scheduler.Script
		}
	}
	for i := range builds {
		script := inlineScripts[builds[i].Scheduler.AgentName]
		if strings.TrimSpace(script) == "" {
			continue
		}
		validation, err := c.validateInlineSchedulerScript(ctx, builds[i].Scheduler.AgentName, script)
		if err != nil {
			return nil, err
		}
		builds[i].ValidationTriggers = validation.Triggers
		builds[i].Loader.Triggers = validation.Triggers
		builds[i].Scheduler.TriggerCount = len(validation.Triggers)
	}
	return builds, nil
}

func (c *Controller) validateManagedSchedulers(ctx context.Context, normalized NormalizedProject) []ValidationIssue {
	project, err := NewRecordFromSpec(normalized.Spec, normalized.SourcePath)
	if err != nil {
		return []ValidationIssue{{Path: "spec", Message: err.Error()}}
	}
	builds, err := c.projectManagedSchedulerBuildsFromSpec(ctx, project, 0, normalized.Spec)
	if err != nil {
		return []ValidationIssue{managedSchedulerBuildIssue(err)}
	}
	builds, err = applyManagedLoaderOverrideBuilds(project, 0, builds, normalized.managedLoaderOverrides)
	if err != nil {
		return []ValidationIssue{{Path: "schedulers", Message: err.Error()}}
	}
	loaderRecords := SchedulerLoaders(builds)
	for _, loader := range loaderRecords {
		if _, err := loaders.NormalizeLoader(loader, false); err != nil {
			return []ValidationIssue{{Path: "schedulers." + loader.Summary.ManagedAgentName, Message: err.Error()}}
		}
		for _, trigger := range loader.Triggers {
			if _, err := loaders.NormalizeLoaderTrigger(loader.Summary.ID, trigger); err != nil {
				return []ValidationIssue{{Path: "schedulers." + loader.Summary.ManagedAgentName + ".triggers", Message: err.Error()}}
			}
		}
	}
	return nil
}

type managedSchedulerBuildError struct {
	path    string
	message string
}

func (e *managedSchedulerBuildError) Error() string {
	if e.path == "" {
		return e.message
	}
	return e.path + ": " + e.message
}

func (c *Controller) validateInlineSchedulerScript(ctx context.Context, agentName, script string) (loaders.LoaderValidationResult, error) {
	path := "agents." + agentName + ".scheduler.script"
	if c == nil || c.loaders == nil {
		return loaders.LoaderValidationResult{}, &managedSchedulerBuildError{path: path, message: "loader manager is required to validate scheduler script"}
	}
	validation, err := c.loaders.Validate(ctx, domain.LoaderRuntimeScheduler, script)
	if err != nil {
		return loaders.LoaderValidationResult{}, &managedSchedulerBuildError{path: path, message: err.Error()}
	}
	return validation, nil
}

func managedSchedulerBuildIssue(err error) ValidationIssue {
	var buildErr *managedSchedulerBuildError
	if errors.As(err, &buildErr) {
		return ValidationIssue{Path: buildErr.path, Message: buildErr.message}
	}
	return ValidationIssue{Path: "schedulers", Message: err.Error()}
}

func (c *Controller) validateManagedAgentDefinitions(normalized NormalizedProject) []ValidationIssue {
	project, err := NewRecordFromSpec(normalized.Spec, normalized.SourcePath)
	if err != nil {
		return []ValidationIssue{{Path: "spec", Message: err.Error()}}
	}
	agents, err := NewAgentDefinitionsFromSpec(project, 0, normalized.Spec)
	if err != nil {
		return []ValidationIssue{{Path: "agents", Message: err.Error()}}
	}
	var issues []ValidationIssue
	for _, agent := range agents {
		path := "agents." + agent.ManagedAgentName
		if _, err := domain.NormalizeAgentDefinition(agent, true); err != nil {
			issues = append(issues, ValidationIssue{Path: path, Message: err.Error()})
			continue
		}
		driver, err := driverpkg.ResolveSandboxRuntimeDriver(agent.Driver, c.defaultDR)
		if err != nil {
			issues = append(issues, ValidationIssue{Path: path + ".driver", Message: err.Error()})
			continue
		}
		if err := driverpkg.ValidateCompiledRuntimeDriver(driver); err != nil {
			issues = append(issues, ValidationIssue{Path: path + ".driver", Message: err.Error()})
		}
	}
	return issues
}

func (c *Controller) cleanupFailedManagedSchedulerReconcile(ctx context.Context, scheduler domain.ProjectSchedulerRecord, loaderID string) {
	if c == nil || c.store == nil {
		return
	}
	if strings.TrimSpace(loaderID) != "" {
		_ = c.store.SetLoaderEnabled(ctx, loaderID, false)
	}
	if strings.TrimSpace(scheduler.ProjectID) != "" && strings.TrimSpace(scheduler.SchedulerID) != "" {
		_, _ = c.store.SetProjectSchedulerEnabled(ctx, scheduler.ProjectID, scheduler.SchedulerID, false)
	}
	_ = c.refreshLoaders(ctx)
}

func (c *Controller) disableManagedLoaderIfOwned(ctx context.Context, loaderID, projectID, schedulerID string) error {
	return DisableManagedLoaderIfOwned(ctx, c.store, loaderID, projectID, schedulerID)
}

func (c *Controller) refreshLoaders(ctx context.Context) error {
	if c == nil || c.loaders == nil {
		return nil
	}
	return c.loaders.Refresh(ctx)
}

func (c *Controller) getProjectAgentIfExists(ctx context.Context, projectID, agentName string) (domain.ProjectAgentRecord, bool, error) {
	agent, err := c.store.GetProjectAgent(ctx, projectID, agentName)
	if err == nil {
		return agent, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProjectAgentRecord{}, false, nil
	}
	return domain.ProjectAgentRecord{}, false, err
}

func applyChanges(project, existing domain.ProjectRecord, found bool, revision domain.ProjectRevisionRecord, revisionCreated bool) []Change {
	projectAction := ChangeActionCreated
	if found {
		projectAction = ChangeActionUnchanged
		if !ProjectRecordUnchanged(existing, project) {
			projectAction = ChangeActionUpdated
		}
	}
	revisionAction := ChangeActionUnchanged
	if revisionCreated {
		revisionAction = ChangeActionCreated
	}
	return []Change{
		{Action: projectAction, ResourceType: "project", ResourceID: project.ID, Name: project.Name},
		{Action: revisionAction, ResourceType: "project_revision", ResourceID: fmt.Sprintf("%s/%d", revision.ProjectID, revision.Revision), Name: revision.SpecHash},
	}
}

func dryRunChanges(project domain.ProjectRecord, agents []domain.ProjectAgentRecord, agentDefinitions []domain.AgentDefinition, schedulers []domain.ProjectSchedulerRecord, loaders []domain.Loader) []Change {
	changes := []Change{{Action: ChangeActionCreated, ResourceType: "project", ResourceID: project.ID, Name: project.Name}}
	for _, agent := range agents {
		changes = append(changes, Change{Action: ChangeActionCreated, ResourceType: "project_agent", ResourceID: agent.ManagedAgentID, Name: agent.AgentName})
	}
	for _, agent := range agentDefinitions {
		changes = append(changes, Change{Action: ChangeActionCreated, ResourceType: "agent_definition", ResourceID: agent.ID, Name: agent.Name})
	}
	for _, scheduler := range schedulers {
		changes = append(changes, Change{Action: ChangeActionCreated, ResourceType: "project_scheduler", ResourceID: scheduler.SchedulerID, Name: scheduler.AgentName})
	}
	for _, loader := range loaders {
		changes = append(changes, Change{Action: ChangeActionCreated, ResourceType: "loader", ResourceID: loader.Summary.ID, Name: loader.Summary.Name})
	}
	return changes
}

func downChangesToChanges(changes []DownChange) []Change {
	result := make([]Change, 0, len(changes))
	for _, change := range changes {
		action := ChangeActionUnchanged
		if change.Action == DownChangeUpdated {
			action = ChangeActionUpdated
		}
		result = append(result, Change{
			Action:       action,
			ResourceType: change.ResourceType,
			ResourceID:   change.ResourceID,
			Name:         change.Name,
			Message:      change.Message,
		})
	}
	return result
}
