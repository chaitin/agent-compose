package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"connectrpc.com/connect"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/runs"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type ProjectDelegate interface {
	ValidateProject(context.Context, *connect.Request[agentcomposev2.ValidateProjectRequest]) (*connect.Response[agentcomposev2.ValidateProjectResponse], error)
	ApplyProject(context.Context, *connect.Request[agentcomposev2.ApplyProjectRequest]) (*connect.Response[agentcomposev2.ApplyProjectResponse], error)
	RemoveProject(context.Context, *connect.Request[agentcomposev2.RemoveProjectRequest]) (*connect.Response[agentcomposev2.RemoveProjectResponse], error)
	WatchProject(context.Context, *connect.Request[agentcomposev2.WatchProjectRequest], *connect.ServerStream[agentcomposev2.WatchProjectResponse]) error
}

type ProjectStore interface {
	GetProject(context.Context, string) (domain.ProjectRecord, error)
	ListProjects(context.Context, domain.ProjectListOptions) (domain.ProjectListResult, error)
	ListProjectSummaryCounts(context.Context, []string) (map[string]domain.ProjectSummaryCounts, error)
	ListProjectAgents(context.Context, string) ([]domain.ProjectAgentRecord, error)
	ListProjectSchedulers(context.Context, string) ([]domain.ProjectSchedulerRecord, error)
	GetProjectRevision(context.Context, string, int64) (domain.ProjectRevisionRecord, error)
}

type ProjectHandler struct {
	delegate ProjectDelegate
	store    ProjectStore
}

func NewProjectHandler(delegate ProjectDelegate, store ProjectStore) *ProjectHandler {
	return &ProjectHandler{delegate: delegate, store: store}
}

func (h *ProjectHandler) ValidateProject(ctx context.Context, req *connect.Request[agentcomposev2.ValidateProjectRequest]) (*connect.Response[agentcomposev2.ValidateProjectResponse], error) {
	return h.delegate.ValidateProject(ctx, req)
}

func (h *ProjectHandler) ApplyProject(ctx context.Context, req *connect.Request[agentcomposev2.ApplyProjectRequest]) (*connect.Response[agentcomposev2.ApplyProjectResponse], error) {
	return h.delegate.ApplyProject(ctx, req)
}

func (h *ProjectHandler) RemoveProject(ctx context.Context, req *connect.Request[agentcomposev2.RemoveProjectRequest]) (*connect.Response[agentcomposev2.RemoveProjectResponse], error) {
	return h.delegate.RemoveProject(ctx, req)
}

func (h *ProjectHandler) WatchProject(ctx context.Context, req *connect.Request[agentcomposev2.WatchProjectRequest], stream *connect.ServerStream[agentcomposev2.WatchProjectResponse]) error {
	return h.delegate.WatchProject(ctx, req, stream)
}

func (h *ProjectHandler) GetProject(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
	if h.store == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	project, err := h.resolveProjectRef(ctx, req.Msg.GetProject())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		if errors.Is(err, domain.ErrRequired) || errors.Is(err, domain.ErrAmbiguous) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	agents, err := h.store.ListProjectAgents(ctx, project.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	schedulers, err := h.store.ListProjectSchedulers(ctx, project.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var spec *agentcomposev2.ProjectSpec
	if req.Msg.GetIncludeSpec() && project.CurrentRevision > 0 {
		revision, err := h.store.GetProjectRevision(ctx, project.ID, project.CurrentRevision)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		spec, err = runs.DecodeRevisionSpec(revision.SpecJSON)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decode project %s revision %d: %w", project.Name, project.CurrentRevision, err))
		}
	}
	return connect.NewResponse(&agentcomposev2.GetProjectResponse{
		Project: ProjectToProto(project, spec, agents, schedulers),
	}), nil
}

func (h *ProjectHandler) ListProjects(ctx context.Context, req *connect.Request[agentcomposev2.ListProjectsRequest]) (*connect.Response[agentcomposev2.ListProjectsResponse], error) {
	if h.store == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	result, err := h.store.ListProjects(ctx, domain.ProjectListOptions{
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
	projectIDs := make([]string, 0, len(result.Projects))
	for _, project := range result.Projects {
		projectIDs = append(projectIDs, project.ID)
	}
	countsByProject, err := h.store.ListProjectSummaryCounts(ctx, projectIDs)
	if err != nil {
		slog.Warn("failed to load project summary counts", "error", err)
		countsByProject = map[string]domain.ProjectSummaryCounts{}
	}
	for _, project := range result.Projects {
		resp.Projects = append(resp.Projects, ProjectSummaryWithCountsToProto(project, countsByProject[project.ID]))
	}
	return connect.NewResponse(resp), nil
}

func (h *ProjectHandler) resolveProjectRef(ctx context.Context, ref *agentcomposev2.ProjectRef) (domain.ProjectRecord, error) {
	if ref == nil {
		return domain.ProjectRecord{}, domain.ClassifyError(domain.ErrRequired, "project ref is required", nil)
	}
	if projectID := strings.TrimSpace(ref.GetProjectId()); projectID != "" {
		return h.store.GetProject(ctx, projectID)
	}
	name := strings.TrimSpace(ref.GetName())
	sourcePath := strings.TrimSpace(ref.GetSourcePath())
	if name != "" && sourcePath != "" {
		projectID, err := domain.StableProjectID(name, sourcePath)
		if err != nil {
			return domain.ProjectRecord{}, err
		}
		return h.store.GetProject(ctx, projectID)
	}
	if name == "" {
		return domain.ProjectRecord{}, domain.ClassifyError(domain.ErrRequired, "project id or name is required", nil)
	}
	result, err := h.store.ListProjects(ctx, domain.ProjectListOptions{Query: name, Limit: 200})
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
