package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/samber/do/v2"
	"gopkg.in/yaml.v3"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/agentcompose/adapters"
	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
	"github.com/chaitin/agent-compose/pkg/apierror"
	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	sessionstream "github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
	"github.com/chaitin/agent-compose/pkg/volumes"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func NewProjectController(di do.Injector) (*projects.Controller, error) {
	imageBackends := do.MustInvoke[*adapters.ImageBackends](di)
	sessionStore := do.MustInvoke[*sandboxstore.Store](di)
	sandboxDriver := do.MustInvoke[*adapters.SandboxDriver](di)
	streams := do.MustInvoke[*sessionstream.StreamBroker](di)
	config := do.MustInvoke[*appconfig.Config](di)
	return projects.NewController(projects.ControllerDependencies{
		Config:     config,
		Admit:      networkPolicyAdmission(do.MustInvoke[*adapters.SandboxEgressProxy](di), config),
		Store:      do.MustInvoke[*configstore.ConfigStore](di),
		Sandboxes:  sessionStore,
		Images:     imageBackends.Auto,
		Schedulers: do.MustInvoke[*schedulers.Controller](di),
		Volumes:    do.MustInvoke[*volumes.Manager](di),
		Gateway:    do.MustInvoke[*configstore.ConfigStore](di),
		StopSandbox: func(ctx context.Context, session *domain.Sandbox) error {
			return stopProjectSandbox(ctx, stopProjectSandboxDeps{
				SandboxRoot:   do.MustInvoke[*appconfig.Config](di).SandboxRoot,
				Locks:         do.MustInvoke[*sessionstream.LifecycleLocks](di),
				Store:         sessionStore,
				Driver:        sandboxDriver,
				Streams:       streams,
				AccessRevoker: do.MustInvoke[*adapters.CapabilitySandboxResolver](di),
			}, session)
		},
	}), nil
}

type projectSandboxStreams interface {
	PublishSandboxUpdated(*domain.SandboxSummary)
	PublishEventAdded(string, domain.SandboxEvent)
}

// stopProjectSandboxDeps bundles the lifecycle dependencies stopProjectSandbox
// needs, as opposed to session, the sandbox actually being stopped.
type stopProjectSandboxDeps struct {
	SandboxRoot   string
	Locks         *sessionstream.LifecycleLocks
	Store         sessionstream.LifecycleStore
	Driver        sessionstream.SandboxDriver
	Streams       projectSandboxStreams
	AccessRevoker sessionstream.SandboxAccessRevoker
}

func stopProjectSandbox(ctx context.Context, deps stopProjectSandboxDeps, session *domain.Sandbox) error {
	if session == nil {
		return nil
	}
	if deps.Store == nil {
		return fmt.Errorf("sandbox store is required")
	}
	_, err := (sessionstream.Lifecycle{
		Config:        &appconfig.Config{SandboxRoot: deps.SandboxRoot},
		Store:         deps.Store,
		Driver:        deps.Driver,
		AccessRevoker: deps.AccessRevoker,
		Notifier: projectSandboxLifecycleNotifier{
			streams: deps.Streams,
		},
		Locks: deps.Locks,
	}).StopLoaded(ctx, session)
	return err
}

type projectSandboxLifecycleNotifier struct {
	streams projectSandboxStreams
}

func (n projectSandboxLifecycleNotifier) PublishSandboxUpdated(summary *domain.SandboxSummary) {
	if n.streams != nil {
		n.streams.PublishSandboxUpdated(summary)
	}
}

func (n projectSandboxLifecycleNotifier) PublishEventAdded(sandboxID string, event domain.SandboxEvent) {
	if n.streams != nil {
		n.streams.PublishEventAdded(sandboxID, event)
	}
}

func (projectSandboxLifecycleNotifier) NotifyDashboard(string) {
}

type projectControllerDelegate struct {
	controller *projects.Controller
	// mediation answers whether the engine's own forward proxy will be in a
	// sandbox's traffic path, so the admission report names the mechanism the
	// creation seam would actually install. A nil value reports the driver's own
	// mechanism, which is what a composition without a proxy has.
	mediation sandboxEgressMediation
	config    *appconfig.Config
}

func (d projectControllerDelegate) ValidateProject(ctx context.Context, req *connect.Request[agentcomposev2.ValidateProjectRequest]) (*connect.Response[agentcomposev2.ValidateProjectResponse], error) {
	normalized, issues, err := normalizeProjectRequest(req.Msg.GetSpec(), req.Msg.GetSource(), req.Msg.GetSubmittedSpecHash())
	if err != nil {
		return nil, api.ErrorForCode(connect.CodeInternal, api.ResourceProject, err)
	}
	result, err := d.controller.ValidateProject(ctx, normalized, issues)
	if err != nil {
		// ValidateProject is read-only: it carries a reason but no effect.
		return nil, api.ConnectErrorForDomainKind(err, api.ResourceProject)
	}
	reports, err := d.networkPolicyReports(normalized.Spec)
	if err != nil {
		return nil, networkPolicyConnectError(err)
	}
	return connect.NewResponse(&agentcomposev2.ValidateProjectResponse{
		Valid:           result.Valid,
		Issues:          validationIssuesToProto(result.Issues),
		SpecHash:        result.SpecHash,
		NetworkPolicies: api.NetworkPolicyReportsToProto(reports),
	}), nil
}

func (d projectControllerDelegate) ApplyProject(ctx context.Context, req *connect.Request[agentcomposev2.ApplyProjectRequest]) (*connect.Response[agentcomposev2.ApplyProjectResponse], error) {
	normalized, issues, err := normalizeProjectRequest(req.Msg.GetSpec(), req.Msg.GetSource(), req.Msg.GetSubmittedSpecHash())
	if err != nil {
		return nil, api.WriteErrorForCode(connect.CodeInternal, api.ResourceProject, err)
	}
	result, err := d.controller.ApplyProject(ctx, projects.ApplyRequest{
		Normalized: normalized,
		Issues:     issues,
		DryRun:     req.Msg.GetDryRun(),
	})
	if err != nil {
		return nil, projectConnectError(err)
	}
	response, err := d.applyResponse(result)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

// applyResponse accounts for the declared network policies of the project the
// daemon just accepted. The reports come from the normalized spec the revision
// persisted, so the report describes the project as stored rather than the
// request as submitted. The same declaration was admitted before the apply, so a
// report that cannot be derived here means the stored spec and the admitted
// candidate diverged; it is still returned rather than omitted because the
// project could not honor that declaration either.
func (d projectControllerDelegate) applyResponse(result projects.ApplyResult) (*agentcomposev2.ApplyProjectResponse, error) {
	response := applyProjectResponse(result)
	reports, err := agentNetworkPolicyReports(result.RevisionSpec, d.mediation, d.config)
	if err != nil {
		return nil, networkPolicyApplyError(err, result.Effect)
	}
	response.NetworkPolicies = api.NetworkPolicyReportsToProto(reports)
	return response, nil
}

// networkPolicyConnectError classifies a report derivation failure for a read
// RPC. It can only come from the declaration or from how it layers under the
// daemon-wide policy, both of which the caller can fix, so the answer stays
// InvalidArgument.
func networkPolicyConnectError(err error) error {
	return api.NewOperationError(connect.CodeInvalidArgument, apierror.ReasonNetworkPolicyRejected, apierror.EffectUnspecified, err)
}

// networkPolicyApplyError classifies a report derivation failure on the apply
// path. The cause is still the declaration or its layering under the daemon
// policy, both of which the caller can fix, so the code stays InvalidArgument
// like the read path; only the effect follows the apply result: NOT_APPLIED
// for a dry-run or a pre-write rejection, UNKNOWN once the project state may
// be durable.
func networkPolicyApplyError(err error, effect apierror.Effect) error {
	if effect == apierror.EffectUnspecified {
		effect = apierror.EffectUnknown
	}
	return api.NewOperationError(connect.CodeInvalidArgument, apierror.ReasonNetworkPolicyRejected, effect, err)
}

// networkPolicyReports accounts for the declared network policies of a spec the
// daemon validated without applying.
func (d projectControllerDelegate) networkPolicyReports(spec *compose.NormalizedProjectSpec) ([]driverpkg.AgentNetworkPolicyReport, error) {
	return agentNetworkPolicyReports(spec, d.mediation, d.config)
}

func (d projectControllerDelegate) PatchProject(ctx context.Context, req *connect.Request[agentcomposev2.PatchProjectRequest], projectRef projects.ProjectRef) (*connect.Response[agentcomposev2.ApplyProjectResponse], error) {
	raw, issues, err := parseProjectRequest(req.Msg.GetSpec())
	if err != nil {
		return nil, api.WriteErrorForCode(connect.CodeInternal, api.ResourceProject, err)
	}
	result, err := d.controller.PatchProject(ctx, projects.PatchRequest{
		Project:                 projectRef,
		ExpectedCurrentSpecHash: req.Msg.GetExpectedCurrentSpecHash(),
		Spec:                    raw,
		Issues:                  issues,
		DryRun:                  req.Msg.GetDryRun(),
	})
	if err != nil {
		return nil, projectConnectError(err)
	}
	response, err := d.applyResponse(result)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func applyProjectResponse(result projects.ApplyResult) *agentcomposev2.ApplyProjectResponse {
	spec := normalizedSpecToProto(result.RevisionSpec)
	resp := &agentcomposev2.ApplyProjectResponse{
		Changes:   projectChangesToProto(result.Changes),
		Issues:    validationIssuesToProto(result.Issues),
		Applied:   result.Applied,
		Unchanged: result.Unchanged,
		Effect:    api.EffectToProto(result.Effect),
	}
	if strings.TrimSpace(result.Project.ID) != "" {
		resp.Project = api.ProjectToProto(result.Project, spec, result.Agents, result.Schedulers)
	}
	if strings.TrimSpace(result.Revision.ProjectID) != "" {
		resp.Revision = api.ProjectRevisionToProto(result.Revision, spec)
	}
	return resp
}

func (d projectControllerDelegate) RemoveProject(ctx context.Context, req *connect.Request[agentcomposev2.RemoveProjectRequest], projectRef projects.ProjectRef) (*connect.Response[agentcomposev2.RemoveProjectResponse], error) {
	result, err := d.controller.RemoveProject(ctx, projects.RemoveRequest{
		Project:       projectRef,
		RemoveHistory: req.Msg.GetRemoveHistory(),
	})
	if err != nil {
		return nil, projectConnectError(err)
	}
	return connect.NewResponse(&agentcomposev2.RemoveProjectResponse{
		Project: api.ProjectToProto(result.Project, nil, result.Agents, result.Schedulers),
		Changes: projectChangesToProto(result.Changes),
		Effect:  api.EffectToProto(result.Effect),
		Reason:  api.ReasonToProto(result.Reason),
	}), nil
}

func (d projectControllerDelegate) WatchProject(ctx context.Context, req *connect.Request[agentcomposev2.WatchProjectRequest], stream *connect.ServerStream[agentcomposev2.WatchProjectResponse]) error {
	_ = ctx
	_ = req
	_ = stream
	return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("project watch is not implemented"))
}

func normalizeProjectRequest(spec *agentcomposev2.ProjectSpec, source *agentcomposev2.ProjectSource, submittedHash string) (projects.NormalizedProject, []projects.ValidationIssue, error) {
	parsed, issues, err := parseProjectRequest(spec)
	if err != nil || len(issues) > 0 {
		return projects.NormalizedProject{}, issues, err
	}
	sourcePath := api.ProjectServiceSourcePath(source)
	projectDir := ""
	if source != nil {
		projectDir = strings.TrimSpace(source.GetProjectDir())
	}
	normalized, err := compose.Normalize(parsed, compose.NormalizeOptions{
		ComposePath:       sourcePath,
		ProjectDir:        projectDir,
		SourceCredentials: compose.SourceCredentialsResolved,
	})
	if err != nil {
		return projects.NormalizedProject{}, []projects.ValidationIssue{validationIssueFromProto(api.IssueFromComposeError(err))}, nil
	}
	hash, err := normalized.Hash()
	if err != nil {
		return projects.NormalizedProject{}, nil, fmt.Errorf("hash project spec: %w", err)
	}
	result := projects.NormalizedProject{
		Spec:       normalized,
		SpecHash:   hash,
		SourcePath: sourcePath,
	}
	submittedHash = strings.TrimSpace(submittedHash)
	if submittedHash != "" && submittedHash != hash {
		return result, []projects.ValidationIssue{{Path: "submitted_spec_hash", Message: fmt.Sprintf("submitted spec hash %s does not match normalized spec hash %s", submittedHash, hash), Reason: apierror.ReasonProjectSpecInvalid}}, nil
	}
	return result, nil, nil
}

func parseProjectRequest(spec *agentcomposev2.ProjectSpec) (*compose.ProjectSpec, []projects.ValidationIssue, error) {
	if spec == nil {
		return nil, []projects.ValidationIssue{{Path: "spec", Message: "project spec is required", Reason: apierror.ReasonProjectSpecInvalid}}, nil
	}
	raw, protoIssues := api.ProjectSpecYAMLShape(spec)
	protoIssues = append(protoIssues, api.LegacyNetworkProtocolIssues(spec.GetAgents())...)
	if len(protoIssues) > 0 {
		return nil, validationIssuesFromProto(protoIssues), nil
	}
	data, err := yaml.Marshal(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal project spec: %w", err)
	}
	parsed, err := compose.Parse(data)
	if err != nil {
		return nil, []projects.ValidationIssue{validationIssueFromProto(api.IssueFromComposeError(err))}, nil
	}
	return parsed, nil, nil
}

func normalizedSpecToProto(spec *compose.NormalizedProjectSpec) *agentcomposev2.ProjectSpec {
	if spec == nil {
		return nil
	}
	return api.ProjectSpecToProtoRedacted(spec)
}

func validationIssuesFromProto(items []*agentcomposev2.ProjectValidationIssue) []projects.ValidationIssue {
	issues := make([]projects.ValidationIssue, 0, len(items))
	for _, item := range items {
		issues = append(issues, validationIssueFromProto(item))
	}
	return issues
}

func validationIssueFromProto(item *agentcomposev2.ProjectValidationIssue) projects.ValidationIssue {
	if item == nil {
		return projects.ValidationIssue{}
	}
	severity := projects.ValidationSeverityError
	if item.GetSeverity() == agentcomposev2.ProjectValidationSeverity_PROJECT_VALIDATION_SEVERITY_WARNING {
		severity = projects.ValidationSeverityWarning
	}
	reason := api.ReasonFromProto(item.GetReason())
	if reason == apierror.ReasonUnspecified && severity == projects.ValidationSeverityError {
		reason = apierror.ReasonProjectSpecInvalid
	}
	return projects.ValidationIssue{Severity: severity, Path: item.GetPath(), Message: item.GetMessage(), Reason: reason}
}

func validationIssuesToProto(items []projects.ValidationIssue) []*agentcomposev2.ProjectValidationIssue {
	issues := make([]*agentcomposev2.ProjectValidationIssue, 0, len(items))
	for _, item := range items {
		issue := api.ProjectValidationIssue(item.Path, item.Message)
		if item.Severity == projects.ValidationSeverityWarning {
			issue.Severity = agentcomposev2.ProjectValidationSeverity_PROJECT_VALIDATION_SEVERITY_WARNING
		}
		reason := item.Reason
		if reason == apierror.ReasonUnspecified && item.Severity != projects.ValidationSeverityWarning {
			// The proto contract requires a reason on error-severity issues;
			// an unspecified reason is the spec-invalid default.
			reason = apierror.ReasonProjectSpecInvalid
		}
		issue.Reason = api.ReasonToProto(reason)
		issues = append(issues, issue)
	}
	return issues
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

func projectConnectError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errNetworkPolicyRejected) {
		return api.NewOperationError(connect.CodeInvalidArgument, apierror.ReasonNetworkPolicyRejected, apierror.EffectFor(err), err)
	}
	return api.ConnectErrorForProject(err)
}
