package projects

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/capability"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/images"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/projectdef"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

var (
	ErrInvalidRequest   = errors.New("invalid project request")
	ErrRevisionConflict = errors.New("project revision conflict")
	ErrUnavailable      = errors.New("project dependency unavailable")
	ErrUnimplemented    = errors.New("project operation unimplemented")
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
	Spec       *projectdef.NormalizedProjectSpec
	SpecHash   string
	SourcePath string
}

type ControllerStore interface {
	GetProject(context.Context, string) (domain.ProjectRecord, error)
	GetProjectIfExists(context.Context, string, bool) (domain.ProjectRecord, bool, error)
	ListProjects(context.Context, domain.ProjectListOptions) (domain.ProjectListResult, error)
	UpsertProject(context.Context, domain.ProjectRecord) (domain.ProjectRecord, error)
	MarkProjectRemoved(context.Context, string) (domain.ProjectRecord, error)
	SaveProjectRevision(context.Context, domain.ProjectRevisionRecord) (domain.ProjectRevisionRecord, bool, error)
	GetProjectRevision(context.Context, string, int64) (domain.ProjectRevisionRecord, error)
	GetProjectAgent(context.Context, string, string) (domain.ProjectAgentRecord, error)
	UpsertProjectAgent(context.Context, domain.ProjectAgentRecord) (domain.ProjectAgentRecord, error)
	ListProjectAgents(context.Context, string) ([]domain.ProjectAgentRecord, error)
	ListProjectSchedulers(context.Context, string) ([]domain.ProjectSchedulerRecord, error)
	ReconcileSchedulerStore
	DownStore
}

type SandboxStore interface {
	DownSandboxStore
}

type SchedulerValidator interface {
	Validate(ctx context.Context, runtime, script string) (schedulers.SchedulerValidationResult, error)
	Refresh(ctx context.Context) error
}

type VolumeManager interface {
	Ensure(ctx context.Context, item domain.VolumeRecord) (domain.VolumeRecord, bool, error)
	Inspect(ctx context.Context, nameOrID string) (domain.VolumeRecord, error)
	ListProjectVolumes(ctx context.Context, projectID string) (map[string]domain.VolumeRecord, error)
	ReplaceProjectVolumes(ctx context.Context, projectID string, links map[string]domain.ProjectVolumeLink) error
	RemoveProjectVolumes(ctx context.Context, projectID string) error
}

type Controller struct {
	config     *appconfig.Config
	store      ControllerStore
	sandboxes  SandboxStore
	images     images.Backend
	schedulers SchedulerValidator
	volumes    VolumeManager
	gateway    CapabilityGatewaySource
	stop       func(context.Context, *domain.Sandbox) error
	defaultDR  string
	lifecycle  projectLifecycleGates
}

type ControllerDependencies struct {
	Config      *appconfig.Config
	Store       ControllerStore
	Sandboxes   SandboxStore
	Images      images.Backend
	Schedulers  SchedulerValidator
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
		config:     deps.Config,
		store:      deps.Store,
		sandboxes:  deps.Sandboxes,
		images:     deps.Images,
		schedulers: deps.Schedulers,
		volumes:    deps.Volumes,
		gateway:    deps.Gateway,
		stop:       deps.StopSandbox,
		defaultDR:  defaultDriver,
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
	if issues := c.validateProjectAgentDefinitions(normalized); len(issues) > 0 {
		return ValidateResult{Valid: false, Issues: issues, SpecHash: normalized.SpecHash}, nil
	}
	if issues := c.validateSchedulers(ctx, normalized); len(issues) > 0 {
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
	RevisionSpec *projectdef.NormalizedProjectSpec
}

func (c *Controller) ApplyProject(ctx context.Context, req ApplyRequest) (ApplyResult, error) {
	return c.applyProject(ctx, req, false)
}

type PatchRequest struct {
	Project                 ProjectRef
	ExpectedCurrentSpecHash string
	Spec                    *projectdef.ProjectSpec
	Issues                  []ValidationIssue
	DryRun                  bool
}

func (c *Controller) PatchProject(ctx context.Context, req PatchRequest) (ApplyResult, error) {
	if HasValidationErrors(req.Issues) {
		return ApplyResult{Issues: req.Issues}, nil
	}
	if c.store == nil {
		return ApplyResult{}, fmt.Errorf("patch project: config store is required")
	}
	expectedHash := strings.TrimSpace(req.ExpectedCurrentSpecHash)
	if expectedHash == "" {
		return ApplyResult{}, fmt.Errorf("%w: expected current spec hash is required", ErrInvalidRequest)
	}
	project, err := c.resolveProjectRef(ctx, req.Project, false)
	if err != nil {
		return ApplyResult{}, err
	}
	release, err := c.lifecycle.acquire(ctx, project.Name)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("patch project %s: wait for lifecycle operation: %w", project.Name, err)
	}
	defer release()
	project, err = c.resolveProjectRef(ctx, ProjectRefByID(project.ID), false)
	if err != nil {
		return ApplyResult{}, err
	}
	if project.CurrentRevision <= 0 {
		return ApplyResult{}, fmt.Errorf("%w: project %s has no current revision", ErrInvalidRequest, project.Name)
	}
	revision, err := c.store.GetProjectRevision(ctx, project.ID, project.CurrentRevision)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("patch project %s: load current revision: %w", project.Name, err)
	}
	if expectedHash != revision.SpecHash {
		return ApplyResult{}, fmt.Errorf("%w: expected spec hash %s does not match current spec hash %s", ErrRevisionConflict, expectedHash, revision.SpecHash)
	}
	current, err := projectdef.ParseCanonicalJSON([]byte(revision.SpecJSON))
	if err != nil {
		return ApplyResult{}, fmt.Errorf("patch project %s: parse current revision: %w", project.Name, err)
	}
	if req.Spec == nil {
		return ApplyResult{Issues: []ValidationIssue{{Path: "spec", Message: "project spec is required"}}, RevisionSpec: current}, nil
	}
	if strings.TrimSpace(req.Spec.Name) != project.Name {
		return ApplyResult{Issues: []ValidationIssue{{Path: "spec.name", Message: "project name cannot be changed by PatchProject"}}, RevisionSpec: current}, nil
	}
	restored, restoreIssues, err := RestoreProjectSecrets(current, req.Spec)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("patch project %s: restore secrets: %w", project.Name, err)
	}
	if HasValidationErrors(restoreIssues) {
		return ApplyResult{Issues: restoreIssues, RevisionSpec: current}, nil
	}
	normalizedSpec, err := projectdef.Normalize(restored, projectdef.NormalizeOptions{
		ComposePath:       project.SourcePath,
		SourceCredentials: projectdef.SourceCredentialsResolved,
	})
	if err != nil {
		var validationErr *projectdef.ValidationError
		if errors.As(err, &validationErr) {
			return ApplyResult{Issues: []ValidationIssue{{Path: validationErr.Path, Message: validationErr.Message}}, RevisionSpec: current}, nil
		}
		return ApplyResult{}, fmt.Errorf("patch project %s: normalize candidate: %w", project.Name, err)
	}
	specHash, err := normalizedSpec.Hash()
	if err != nil {
		return ApplyResult{}, fmt.Errorf("patch project %s: hash candidate: %w", project.Name, err)
	}
	return c.applyProject(ctx, ApplyRequest{
		Normalized: NormalizedProject{Spec: normalizedSpec, SpecHash: specHash, SourcePath: project.SourcePath},
		Issues:     req.Issues,
		DryRun:     req.DryRun,
	}, true)
}

//nolint:funlen // orchestrates project/agent/workspace/volume reconciliation with heavily shared mutable state and many partial-result early returns; a forced split would move state between functions rather than reduce complexity.
func (c *Controller) applyProject(ctx context.Context, req ApplyRequest, lifecycleHeld bool) (ApplyResult, error) {
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
	if issues := c.validateProjectAgentDefinitions(normalized); len(issues) > 0 {
		return ApplyResult{Issues: issues, RevisionSpec: normalized.Spec}, nil
	}
	release := func() {}
	if !lifecycleHeld {
		release, err = c.lifecycle.acquire(ctx, project.Name)
		if err != nil {
			return ApplyResult{}, fmt.Errorf("apply project %s: wait for lifecycle operation: %w", normalized.Spec.Name, err)
		}
	}
	defer release()
	existingByName, nameFound, err := c.projectByNameIfExists(ctx, project.Name)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("apply project %s: resolve existing name: %w", normalized.Spec.Name, err)
	}
	if nameFound {
		project.ID = existingByName.ID
		project.ShortID = existingByName.ShortID
	}
	if issues := c.validateSchedulers(ctx, normalized); len(issues) > 0 {
		return ApplyResult{Issues: issues, RevisionSpec: normalized.Spec}, nil
	}
	warnings, err := c.capabilityGatewayWarnings(ctx, normalized.Spec)
	if err != nil {
		return ApplyResult{}, err
	}
	agentRecords, agentDefinitions, schedulerRecords, _, err := c.projectArtifacts(ctx, project, 0, normalized)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("%w: apply project %s: %w", ErrInvalidRequest, normalized.Spec.Name, err)
	}
	if req.DryRun {
		return ApplyResult{
			Project:      project,
			Revision:     domain.ProjectRevisionRecord{ProjectID: project.ID, SpecHash: normalized.SpecHash},
			Agents:       agentRecords,
			Schedulers:   schedulerRecords,
			Changes:      dryRunChanges(project, agentRecords, agentDefinitions, schedulerRecords),
			Applied:      false,
			Issues:       append(req.Issues, warnings...),
			RevisionSpec: normalized.Spec,
		}, nil
	}
	if err := images.EnsureProjectAgentImages(ctx, c.config, c.images, images.ProjectAgentImagesRequest{
		ProjectName: normalized.Spec.Name,
		Agents:      agentRecords,
	}); err != nil {
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

	agentRecords, agentDefinitions, schedulerRecords, schedulerDefinitions, err := c.projectArtifacts(ctx, project, revision.Revision, normalized)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("%w: apply project %s: %w", ErrInvalidRequest, normalized.Spec.Name, err)
	}
	changes := applyChanges(applyChangesInput{
		Project:         project,
		Existing:        existingProject,
		Found:           projectFound,
		Revision:        revision,
		RevisionCreated: revisionCreated,
	})
	agentsUnchanged := true
	agentActions := make(map[string]string, len(agentRecords))
	for _, agent := range agentRecords {
		existingAgent, found, err := c.getProjectAgentIfExists(ctx, project.ID, agent.AgentName)
		if err != nil {
			return ApplyResult{}, fmt.Errorf("apply project %s: load agent %s: %w", normalized.Spec.Name, agent.AgentName, err)
		}
		if _, err := c.store.UpsertProjectAgent(ctx, agent); err != nil {
			return ApplyResult{}, fmt.Errorf("apply project %s: upsert agent %s: %w", normalized.Spec.Name, agent.AgentName, err)
		}
		action := ProjectAgentChangeAction(existingAgent, found, agent)
		agentActions[agent.ID] = action
		if action != ChangeActionUnchanged {
			agentsUnchanged = false
		}
		changes = append(changes, Change{
			Action:       action,
			ResourceType: "project_agent",
			ResourceID:   agent.ID,
			Name:         agent.AgentName,
		})
	}
	// Keep the historical change-report shape while project_agent and the
	// immutable revision now own identity and runtime configuration.
	for _, definition := range agentDefinitions {
		action := agentActions[definition.ID]
		if action == "" {
			action = ChangeActionUnchanged
		}
		changes = append(changes, Change{
			Action:       action,
			ResourceType: "agent_definition",
			ResourceID:   definition.ID,
			Name:         definition.Name,
		})
	}
	schedulerChanges, schedulersUnchanged, err := ReconcileSchedulers(ctx, c.store, ReconcileSchedulersRequest{
		Project:     project,
		Schedulers:  schedulerRecords,
		Definitions: schedulerDefinitions,
	}, ReconcileSchedulerOptions{
		CleanupFailedScheduler: c.cleanupFailedSchedulerReconcile,
		RefreshSchedulers:      c.refreshSchedulers,
	})
	if err != nil {
		changes = append(changes, schedulerChanges...)
		if schedulerReconcileNeedsFailClosed(err) {
			if failClosedErr := c.disableProjectSchedulersAfterReconcileFailure(ctx, project.ID); failClosedErr != nil {
				err = errors.Join(err, failClosedErr)
			}
		}
		agents, listAgentsErr := c.store.ListProjectAgents(ctx, project.ID)
		if listAgentsErr != nil {
			// The listing failure is reported for diagnosis only and is formatted
			// with %v on purpose. projectConnectError classifies this error with an
			// ordered switch, so wrapping a second, unrelated cause would let the
			// follow-up failure decide the status code instead of the reconcile
			// failure that actually broke the apply.
			//nolint:errorlint // secondary cause is diagnostic; see comment above
			return ApplyResult{}, fmt.Errorf("apply project %s: %w; list project agents after reconcile failure: %v", normalized.Spec.Name, err, listAgentsErr)
		}
		schedulers, listSchedulersErr := c.store.ListProjectSchedulers(ctx, project.ID)
		if listSchedulersErr != nil {
			//nolint:errorlint // secondary cause is diagnostic; see comment above
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

func (c *Controller) projectByNameIfExists(ctx context.Context, name string) (domain.ProjectRecord, bool, error) {
	project, err := c.resolveProjectRef(ctx, ProjectRefByName(name), true)
	if err == nil {
		return project, true, nil
	}
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ProjectRecord{}, false, nil
	}
	return domain.ProjectRecord{}, false, err
}

func (c *Controller) capabilityGatewayWarnings(ctx context.Context, spec *projectdef.NormalizedProjectSpec) ([]ValidationIssue, error) {
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

func usesGlobalCapabilityGateway(spec *projectdef.NormalizedProjectSpec) bool {
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
	release, err := c.lifecycle.acquire(ctx, project.Name)
	if err != nil {
		return RemoveResult{}, fmt.Errorf("remove project %s: wait for lifecycle operation: %w", project.Name, err)
	}
	defer release()
	project, err = c.resolveProjectRef(ctx, ProjectRefByID(project.ID), true)
	if err != nil {
		return RemoveResult{}, err
	}
	downChanges, err := DownProject(ctx, project, DownOptions{
		Store:             c.store,
		Sandboxes:         c.sandboxes,
		RefreshSchedulers: c.refreshSchedulers,
		StopSandbox:       c.stop,
	})
	changes := downChangesToChanges(downChanges)
	if err != nil {
		return RemoveResult{Project: project, Changes: changes}, err
	}
	if DownChangesHaveFailures(downChanges) {
		agents, listAgentsErr := c.store.ListProjectAgents(ctx, project.ID)
		if listAgentsErr != nil {
			return RemoveResult{Project: project, Changes: changes}, listAgentsErr
		}
		schedulers, listSchedulersErr := c.store.ListProjectSchedulers(ctx, project.ID)
		if listSchedulersErr != nil {
			return RemoveResult{Project: project, Agents: agents, Changes: changes}, listSchedulersErr
		}
		return RemoveResult{Project: project, Agents: agents, Schedulers: schedulers, Changes: changes}, nil
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
	}
	if c.volumes != nil {
		if err := c.volumes.RemoveProjectVolumes(ctx, project.ID); err != nil {
			return RemoveResult{Project: project, Changes: changes}, err
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
	if !includeRemoved {
		return ResolveProjectRef(ctx, c.store, ref)
	}
	value := strings.TrimSpace(ref.value)
	if value == "" {
		return domain.ProjectRecord{}, domain.ClassifyError(domain.ErrRequired, "project selector is required", nil)
	}
	if ref.kind == projectRefID {
		project, found, err := c.store.GetProjectIfExists(ctx, value, true)
		if err != nil {
			return domain.ProjectRecord{}, err
		}
		if found {
			return project, nil
		}
		return domain.ProjectRecord{}, domain.ResourceError(domain.ErrNotFound, "project", value, fmt.Sprintf("project %s not found", value), sql.ErrNoRows)
	}
	query := value
	projectValue := func(project domain.ProjectRecord) string { return project.Name }
	selectorName := "name"
	if ref.kind == projectRefSourcePath {
		query = NormalizeProjectSourcePath(value)
		projectValue = func(project domain.ProjectRecord) string {
			return NormalizeProjectSourcePath(project.SourcePath)
		}
		selectorName = "source path"
	} else if ref.kind != projectRefName {
		return domain.ProjectRecord{}, domain.ClassifyError(domain.ErrRequired, "project selector is required", nil)
	}
	return resolveProjectByExactMatch(ctx, c.store, exactMatchRequest{
		Value:          query,
		IncludeRemoved: true,
		SelectorName:   selectorName,
		FieldValue:     projectValue,
	})
}

func (c *Controller) projectArtifacts(ctx context.Context, project domain.ProjectRecord, revision int64, normalized NormalizedProject) ([]domain.ProjectAgentRecord, []domain.AgentDefinition, []domain.ProjectSchedulerRecord, []domain.Scheduler, error) {
	spec := normalized.Spec
	agentRecords, err := NewAgentRecordsFromSpec(project.ID, revision, spec)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	agentDefinitions, err := NewAgentDefinitionsFromSpec(project, revision, spec)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	schedulerRecords, schedulerDefinitions, err := c.projectSchedulersFromSpec(ctx, project, revision, spec)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	syncProjectAgentSchedulerState(agentRecords, schedulerRecords)
	return agentRecords, agentDefinitions, schedulerRecords, schedulerDefinitions, nil
}

func (c *Controller) projectSchedulersFromSpec(ctx context.Context, project domain.ProjectRecord, revision int64, spec *projectdef.NormalizedProjectSpec) ([]domain.ProjectSchedulerRecord, []domain.Scheduler, error) {
	builds, err := c.projectSchedulerBuildsFromSpec(ctx, project, revision, spec)
	if err != nil {
		return nil, nil, err
	}
	return SchedulerRecords(builds), SchedulerDefinitions(builds), nil
}

func (c *Controller) projectSchedulerBuildsFromSpec(ctx context.Context, project domain.ProjectRecord, revision int64, spec *projectdef.NormalizedProjectSpec) ([]SchedulerBuild, error) {
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
		script := inlineScripts[builds[i].Record.AgentName]
		if strings.TrimSpace(script) == "" {
			continue
		}
		validation, err := c.validateInlineSchedulerScript(ctx, builds[i].Record.AgentName, script)
		if err != nil {
			return nil, err
		}
		builds[i].ValidationTriggers = validation.Triggers
		builds[i].Definition.Triggers = validation.Triggers
		builds[i].Record.TriggerCount = len(validation.Triggers)
	}
	return builds, nil
}

func (c *Controller) validateSchedulers(ctx context.Context, normalized NormalizedProject) []ValidationIssue {
	project, err := NewRecordFromSpec(normalized.Spec, normalized.SourcePath)
	if err != nil {
		return []ValidationIssue{{Path: "spec", Message: err.Error()}}
	}
	builds, err := c.projectSchedulerBuildsFromSpec(ctx, project, 0, normalized.Spec)
	if err != nil {
		return []ValidationIssue{projectSchedulerBuildIssue(err)}
	}
	schedulerRecords := SchedulerDefinitions(builds)
	for _, definition := range schedulerRecords {
		if _, err := schedulers.NormalizeScheduler(definition, false); err != nil {
			return []ValidationIssue{{Path: "schedulers." + definition.Summary.AgentName, Message: err.Error()}}
		}
		for _, trigger := range definition.Triggers {
			if _, err := schedulers.NormalizeSchedulerTrigger(definition.Summary.ID, trigger); err != nil {
				return []ValidationIssue{{Path: "schedulers." + definition.Summary.AgentName + ".triggers", Message: err.Error()}}
			}
		}
	}
	return nil
}

type projectSchedulerBuildError struct {
	path    string
	message string
}

func (e *projectSchedulerBuildError) Error() string {
	if e.path == "" {
		return e.message
	}
	return e.path + ": " + e.message
}

func (c *Controller) validateInlineSchedulerScript(ctx context.Context, agentName, script string) (schedulers.SchedulerValidationResult, error) {
	path := "agents." + agentName + ".scheduler.script"
	if c == nil || c.schedulers == nil {
		return schedulers.SchedulerValidationResult{}, &projectSchedulerBuildError{path: path, message: "scheduler controller is required to validate scheduler script"}
	}
	validation, err := c.schedulers.Validate(ctx, domain.SchedulerRuntimeScheduler, script)
	if err != nil {
		return schedulers.SchedulerValidationResult{}, &projectSchedulerBuildError{path: path, message: err.Error()}
	}
	return validation, nil
}

func projectSchedulerBuildIssue(err error) ValidationIssue {
	var buildErr *projectSchedulerBuildError
	if errors.As(err, &buildErr) {
		return ValidationIssue{Path: buildErr.path, Message: buildErr.message}
	}
	return ValidationIssue{Path: "schedulers", Message: err.Error()}
}

func (c *Controller) validateProjectAgentDefinitions(normalized NormalizedProject) []ValidationIssue {
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
		path := "agents." + agent.AgentName
		if _, err := NormalizeAgentDefinition(agent, true); err != nil {
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

func (c *Controller) cleanupFailedSchedulerReconcile(ctx context.Context, scheduler domain.ProjectSchedulerRecord, _ string) {
	if c == nil || c.store == nil {
		return
	}
	if strings.TrimSpace(scheduler.ProjectID) != "" && strings.TrimSpace(scheduler.SchedulerID) != "" {
		_, _ = c.store.SetProjectSchedulerEnabled(ctx, scheduler.ProjectID, scheduler.SchedulerID, false)
	}
	_ = c.refreshSchedulers(ctx)
}

func (c *Controller) refreshSchedulers(ctx context.Context) error {
	if c == nil || c.schedulers == nil {
		return nil
	}
	return c.schedulers.Refresh(ctx)
}

// disableProjectSchedulersAfterReconcileFailure makes a partially applied
// project fail closed. Without this compensation, schedulers that were enabled
// before a later scheduler failed could continue running against a revision
// whose complete scheduler set was never reconciled.
func (c *Controller) disableProjectSchedulersAfterReconcileFailure(ctx context.Context, projectID string) error {
	schedulers, err := c.store.ListProjectSchedulers(ctx, projectID)
	if err != nil {
		return fmt.Errorf("list schedulers while failing closed: %w", err)
	}
	var disableErr error
	for _, scheduler := range schedulers {
		if !scheduler.Enabled {
			continue
		}
		if _, err := c.store.SetProjectSchedulerEnabled(ctx, projectID, scheduler.SchedulerID, false); err != nil {
			disableErr = errors.Join(disableErr, fmt.Errorf("disable scheduler %s: %w", scheduler.SchedulerID, err))
		}
	}
	if err := c.refreshSchedulers(ctx); err != nil {
		disableErr = errors.Join(disableErr, fmt.Errorf("refresh scheduler controller after fail-closed compensation: %w", err))
	}
	if disableErr != nil {
		return fmt.Errorf("fail-closed scheduler compensation: %w", disableErr)
	}
	return nil
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

// applyChangesInput describes the before/after state of a project apply used
// to compute the resulting Change log.
type applyChangesInput struct {
	Project         domain.ProjectRecord
	Existing        domain.ProjectRecord
	Found           bool
	Revision        domain.ProjectRevisionRecord
	RevisionCreated bool
}

func applyChanges(in applyChangesInput) []Change {
	projectAction := ChangeActionCreated
	if in.Found {
		projectAction = ChangeActionUnchanged
		if !ProjectRecordUnchanged(in.Existing, in.Project) {
			projectAction = ChangeActionUpdated
		}
	}
	revisionAction := ChangeActionUnchanged
	if in.RevisionCreated {
		revisionAction = ChangeActionCreated
	}
	return []Change{
		{Action: projectAction, ResourceType: "project", ResourceID: in.Project.ID, Name: in.Project.Name},
		{Action: revisionAction, ResourceType: "project_revision", ResourceID: fmt.Sprintf("%s/%d", in.Revision.ProjectID, in.Revision.Revision), Name: in.Revision.SpecHash},
	}
}

func dryRunChanges(project domain.ProjectRecord, agents []domain.ProjectAgentRecord, agentDefinitions []domain.AgentDefinition, schedulers []domain.ProjectSchedulerRecord) []Change {
	changes := []Change{{Action: ChangeActionCreated, ResourceType: "project", ResourceID: project.ID, Name: project.Name}}
	for _, agent := range agents {
		changes = append(changes, Change{Action: ChangeActionCreated, ResourceType: "project_agent", ResourceID: agent.ID, Name: agent.AgentName})
	}
	for _, agent := range agentDefinitions {
		changes = append(changes, Change{Action: ChangeActionCreated, ResourceType: "agent_definition", ResourceID: agent.ID, Name: agent.Name})
	}
	for _, scheduler := range schedulers {
		changes = append(changes, Change{Action: ChangeActionCreated, ResourceType: "scheduler", ResourceID: scheduler.ID, Name: scheduler.AgentName})
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
