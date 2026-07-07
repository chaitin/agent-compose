package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/samber/do/v2"
	"gopkg.in/yaml.v3"

	"agent-compose/pkg/agentcompose/adapters"
	"agent-compose/pkg/agentcompose/api"
	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
	sessionstream "agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
	"agent-compose/pkg/volumes"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func NewProjectController(di do.Injector) (*projects.Controller, error) {
	imageBackends := do.MustInvoke[*adapters.ImageBackends](di)
	sessionStore := do.MustInvoke[*sessionstore.Store](di)
	sessionDriver := do.MustInvoke[*adapters.SessionDriver](di)
	streams := do.MustInvoke[*sessionstream.StreamBroker](di)
	return projects.NewController(projects.ControllerDependencies{
		Config:   do.MustInvoke[*appconfig.Config](di),
		Store:    do.MustInvoke[*configstore.ConfigStore](di),
		Sessions: sessionStore,
		Images:   imageBackends.Auto,
		Loaders:  do.MustInvoke[*loaders.Controller](di),
		Volumes:  do.MustInvoke[*volumes.Manager](di),
		StopSession: func(ctx context.Context, session *domain.Session) error {
			return stopProjectSession(ctx, sessionStore, sessionDriver, streams, session)
		},
	}), nil
}

type projectSessionStore interface {
	GetSession(context.Context, string) (*domain.Session, error)
	UpdateSession(context.Context, *domain.Session) error
	AddEvent(context.Context, string, domain.SessionEvent) error
}

type projectSessionDriver interface {
	StopSessionVM(context.Context, *domain.Session) error
}

type projectSessionStreams interface {
	PublishSessionUpdated(*domain.SessionSummary)
	PublishEventAdded(string, domain.SessionEvent)
}

func stopProjectSession(ctx context.Context, store projectSessionStore, driver projectSessionDriver, streams projectSessionStreams, session *domain.Session) error {
	if session == nil {
		return nil
	}
	if store == nil {
		return fmt.Errorf("session store is required")
	}
	loaded, err := store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return err
	}
	if loaded.Summary.VMStatus != domain.VMStatusRunning {
		return nil
	}
	if driver == nil {
		return fmt.Errorf("session driver is required")
	}
	if err := driver.StopSessionVM(ctx, loaded); err != nil {
		return err
	}
	loaded.Summary.VMStatus = domain.VMStatusStopped
	if err := store.UpdateSession(ctx, loaded); err != nil {
		return err
	}
	event := domain.SessionEvent{
		ID:        uuid.NewString(),
		Type:      "session.stopped",
		Level:     "info",
		Message:   "session stopped",
		CreatedAt: time.Now().UTC(),
	}
	_ = store.AddEvent(ctx, loaded.Summary.ID, event)
	if streams != nil {
		streams.PublishSessionUpdated(&loaded.Summary)
		streams.PublishEventAdded(loaded.Summary.ID, event)
	}
	return nil
}

type projectControllerDelegate struct {
	controller *projects.Controller
}

func (d projectControllerDelegate) ValidateProject(ctx context.Context, req *connect.Request[agentcomposev2.ValidateProjectRequest]) (*connect.Response[agentcomposev2.ValidateProjectResponse], error) {
	normalized, issues, err := normalizeProjectRequest(req.Msg.GetSpec(), req.Msg.GetSource(), req.Msg.GetExpectedSpecHash())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	result, err := d.controller.ValidateProject(ctx, normalized, issues)
	if err != nil {
		return nil, projectConnectError(err)
	}
	return connect.NewResponse(&agentcomposev2.ValidateProjectResponse{
		Valid:    result.Valid,
		Issues:   validationIssuesToProto(result.Issues),
		SpecHash: result.SpecHash,
	}), nil
}

func (d projectControllerDelegate) ApplyProject(ctx context.Context, req *connect.Request[agentcomposev2.ApplyProjectRequest]) (*connect.Response[agentcomposev2.ApplyProjectResponse], error) {
	normalized, issues, err := normalizeProjectRequest(req.Msg.GetSpec(), req.Msg.GetSource(), req.Msg.GetExpectedSpecHash())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	result, err := d.controller.ApplyProject(ctx, projects.ApplyRequest{
		Normalized: normalized,
		Issues:     issues,
		DryRun:     req.Msg.GetDryRun(),
	})
	if err != nil {
		return nil, projectConnectError(err)
	}
	spec := normalizedSpecToProto(result.RevisionSpec)
	resp := &agentcomposev2.ApplyProjectResponse{
		Changes:   projectChangesToProto(result.Changes),
		Issues:    validationIssuesToProto(result.Issues),
		Applied:   result.Applied,
		Unchanged: result.Unchanged,
	}
	if strings.TrimSpace(result.Project.ID) != "" {
		resp.Project = api.ProjectToProto(result.Project, spec, result.Agents, result.Schedulers)
	}
	if strings.TrimSpace(result.Revision.ProjectID) != "" {
		resp.Revision = api.ProjectRevisionToProto(result.Revision, spec)
	}
	return connect.NewResponse(resp), nil
}

func (d projectControllerDelegate) RemoveProject(ctx context.Context, req *connect.Request[agentcomposev2.RemoveProjectRequest]) (*connect.Response[agentcomposev2.RemoveProjectResponse], error) {
	result, err := d.controller.RemoveProject(ctx, projects.RemoveRequest{
		Project:       projectRefFromProto(req.Msg.GetProject()),
		RemoveHistory: req.Msg.GetRemoveHistory(),
	})
	if err != nil {
		return nil, projectConnectError(err)
	}
	return connect.NewResponse(&agentcomposev2.RemoveProjectResponse{
		Project: api.ProjectToProto(result.Project, nil, result.Agents, result.Schedulers),
		Changes: projectChangesToProto(result.Changes),
	}), nil
}

func (d projectControllerDelegate) WatchProject(ctx context.Context, req *connect.Request[agentcomposev2.WatchProjectRequest], stream *connect.ServerStream[agentcomposev2.WatchProjectResponse]) error {
	_ = ctx
	_ = req
	_ = stream
	return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("project watch is not implemented"))
}

func normalizeProjectRequest(spec *agentcomposev2.ProjectSpec, source *agentcomposev2.ProjectSource, expectedHash string) (projects.NormalizedProject, []projects.ValidationIssue, error) {
	if spec == nil {
		return projects.NormalizedProject{}, []projects.ValidationIssue{{Path: "spec", Message: "project spec is required"}}, nil
	}
	raw, protoIssues := api.ProjectSpecYAMLShape(spec)
	if len(protoIssues) > 0 {
		return projects.NormalizedProject{}, validationIssuesFromProto(protoIssues), nil
	}
	data, err := yaml.Marshal(raw)
	if err != nil {
		return projects.NormalizedProject{}, nil, fmt.Errorf("marshal project spec: %w", err)
	}
	parsed, err := compose.Parse(data)
	if err != nil {
		return projects.NormalizedProject{}, []projects.ValidationIssue{validationIssueFromProto(api.IssueFromComposeError(err))}, nil
	}
	sourcePath := api.ProjectServiceSourcePath(source)
	projectDir := ""
	if source != nil {
		projectDir = strings.TrimSpace(source.GetProjectDir())
	}
	normalized, err := compose.Normalize(parsed, compose.NormalizeOptions{
		ComposePath: sourcePath,
		ProjectDir:  projectDir,
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
	expectedHash = strings.TrimSpace(expectedHash)
	if expectedHash != "" && expectedHash != hash {
		return result, []projects.ValidationIssue{{Path: "expected_spec_hash", Message: fmt.Sprintf("expected spec hash %s does not match normalized spec hash %s", expectedHash, hash)}}, nil
	}
	return result, nil, nil
}

func normalizedSpecToProto(spec *compose.NormalizedProjectSpec) *agentcomposev2.ProjectSpec {
	if spec == nil {
		return nil
	}
	return api.ProjectSpecToProto(spec)
}

func projectRefFromProto(ref *agentcomposev2.ProjectRef) projects.ProjectRef {
	if ref == nil {
		return projects.ProjectRef{}
	}
	return projects.ProjectRef{
		ProjectID:  ref.GetProjectId(),
		Name:       ref.GetName(),
		SourcePath: ref.GetSourcePath(),
	}
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
	return projects.ValidationIssue{Path: item.GetPath(), Message: item.GetMessage()}
}

func validationIssuesToProto(items []projects.ValidationIssue) []*agentcomposev2.ProjectValidationIssue {
	issues := make([]*agentcomposev2.ProjectValidationIssue, 0, len(items))
	for _, item := range items {
		issues = append(issues, api.ProjectValidationIssue(item.Path, item.Message))
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
	switch {
	case errors.Is(err, projects.ErrInvalidRequest), errors.Is(err, domain.ErrRequired), errors.Is(err, domain.ErrAmbiguous), errors.Is(err, domain.ErrInvalidArgument):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, projects.ErrUnavailable):
		return connect.NewError(connect.CodeUnavailable, err)
	case errors.Is(err, projects.ErrUnimplemented), errors.Is(err, domain.ErrUnsupported):
		return connect.NewError(connect.CodeUnimplemented, err)
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}
