package projects

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"

	"agent-compose/pkg/executor"
	"agent-compose/pkg/model"
	"agent-compose/pkg/storage"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func NewExecTargetResolver(configDB *ConfigStore, store *Store) executor.TargetResolver {
	return func(ctx context.Context, req *agentcomposev2.ExecRequest) (*model.Session, string, error) {
		return ResolveExecTargetSession(ctx, configDB, store, req)
	}
}

func ResolveExecTargetSession(ctx context.Context, configDB *ConfigStore, store *Store, req *agentcomposev2.ExecRequest) (*model.Session, string, error) {
	if req == nil {
		return nil, "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec request is required"))
	}
	if sessionID := strings.TrimSpace(req.GetSessionId()); sessionID != "" {
		return resolveExecSessionTarget(ctx, store, sessionID)
	}
	if runID := strings.TrimSpace(req.GetRunId()); runID != "" {
		return resolveExecRunTarget(ctx, configDB, store, runID)
	}
	selector := req.GetSelector()
	if selector == nil {
		return nil, "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec target is required"))
	}
	return resolveExecSelectorTarget(ctx, configDB, store, selector)
}

func resolveExecSessionTarget(ctx context.Context, store *Store, sessionID string) (*model.Session, string, error) {
	if store == nil {
		return nil, "", connect.NewError(connect.CodeInternal, fmt.Errorf("session store is required"))
	}
	session, err := store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s not found: %w", sessionID, err))
	}
	if session.Summary.VMStatus != model.VMStatusRunning {
		return nil, "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s is not running", sessionID))
	}
	return session, "", nil
}

func resolveExecRunTarget(ctx context.Context, configDB *ConfigStore, store *Store, runID string) (*model.Session, string, error) {
	if configDB == nil {
		return nil, "", connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	run, err := configDB.GetProjectRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("run %s not found: %w", runID, err))
		}
		return nil, "", connect.NewError(connect.CodeInternal, err)
	}
	session, err := sessionForExecProjectRun(ctx, store, run)
	if err != nil {
		return nil, "", err
	}
	return session, run.RunID, nil
}

func resolveExecSelectorTarget(ctx context.Context, configDB *ConfigStore, store *Store, selector *agentcomposev2.ExecSessionSelector) (*model.Session, string, error) {
	project, err := resolveExecProjectRef(ctx, configDB, &agentcomposev2.ProjectRef{
		ProjectId: selector.GetProjectId(),
		Name:      selector.GetProjectName(),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", connect.NewError(connect.CodeNotFound, err)
		}
		if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "ambiguous") {
			return nil, "", connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, "", connect.NewError(connect.CodeInternal, err)
	}
	statuses, err := storage.ListProjectSessionStatuses(ctx, configDB, store, storage.ProjectSessionRelationFilter{
		ProjectID: project.ID,
		AgentName: selector.GetAgentName(),
	})
	if err != nil {
		return nil, "", connect.NewError(connect.CodeInternal, err)
	}
	type candidate struct {
		session *model.Session
		run     ProjectRunRecord
	}
	var candidates []candidate
	for _, status := range statuses {
		if status.Session == nil || status.Session.Summary.VMStatus != model.VMStatusRunning {
			continue
		}
		candidates = append(candidates, candidate{session: status.Session, run: status.Run})
	}
	contextParts := []string{fmt.Sprintf("project %s", project.Name)}
	if agentName := strings.TrimSpace(selector.GetAgentName()); agentName != "" {
		contextParts = append(contextParts, fmt.Sprintf("agent %s", agentName))
	}
	contextText := strings.Join(contextParts, " ")
	if len(candidates) == 0 {
		return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("no running session found for %s", contextText))
	}
	if len(candidates) > 1 {
		ids := make([]string, 0, len(candidates))
		for _, item := range candidates {
			ids = append(ids, item.session.Summary.ID)
		}
		slices.Sort(ids)
		return nil, "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("multiple running sessions found for %s: %s", contextText, strings.Join(ids, ", ")))
	}
	return candidates[0].session, candidates[0].run.RunID, nil
}

func sessionForExecProjectRun(ctx context.Context, store *Store, run ProjectRunRecord) (*model.Session, error) {
	if store == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("session store is required"))
	}
	sessionID := strings.TrimSpace(run.SessionID)
	if sessionID == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("run %s has no session", run.RunID))
	}
	session, err := store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s for run %s not found: %w", sessionID, run.RunID, err))
	}
	if session.Summary.VMStatus != model.VMStatusRunning {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s for run %s is not running", sessionID, run.RunID))
	}
	return session, nil
}

func resolveExecProjectRef(ctx context.Context, store *ConfigStore, ref *agentcomposev2.ProjectRef) (ProjectRecord, error) {
	if store == nil {
		return ProjectRecord{}, fmt.Errorf("config store is required")
	}
	if ref == nil {
		return ProjectRecord{}, fmt.Errorf("project ref is required")
	}
	if projectID := strings.TrimSpace(ref.GetProjectId()); projectID != "" {
		return store.GetProject(ctx, projectID)
	}
	name := strings.TrimSpace(ref.GetName())
	sourcePath := strings.TrimSpace(ref.GetSourcePath())
	if name != "" && sourcePath != "" {
		projectID, err := StableProjectID(name, sourcePath)
		if err != nil {
			return ProjectRecord{}, err
		}
		return store.GetProject(ctx, projectID)
	}
	if name == "" {
		return ProjectRecord{}, fmt.Errorf("project id or name is required")
	}
	result, err := store.ListProjects(ctx, ProjectListOptions{Query: name, Limit: 200})
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
