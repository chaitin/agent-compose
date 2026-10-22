package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

type execAttachState struct {
	execID  string
	runID   string
	cwd     string
	request *agentcomposev2.ExecRequest
	sandbox *domain.Sandbox
	vmState domain.VMState
	runtime ExecRuntime
	spec    driverpkg.RuntimeStartSpec
	tty     bool
}

func (h *ExecHandler) prepareExecAttach(ctx context.Context, start *agentcomposev2.AttachExecStart) (*execAttachState, error) {
	req := start.GetRequest()
	if req == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec attach request is required"))
	}
	sandbox, runID, err := h.resolveExecTargetSandbox(ctx, req)
	if err != nil {
		return nil, err
	}
	command := strings.TrimSpace(req.GetCommand().GetCommand())
	if command == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec command is required"))
	}
	if err := runs.ValidateCommandEnv(req.GetEnv()); err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	appconfig.ApplyDefaultGuestPaths(h.config)
	cwd := strings.TrimSpace(req.GetCwd())
	if cwd == "" {
		cwd = h.config.GuestWorkspacePath
	}
	vmState, err := h.store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	runtime, err := h.runtime(sandbox)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	env := ExecEnvMap(req.GetEnv())
	execID := uuid.NewString()
	size := start.GetTerminalSize()
	spec := driverpkg.RuntimeStartSpec{
		OperationID: execID,
		Kind:        driverpkg.RuntimeOperationCommand,
		Origin:      "exec_attach",
		Command: &driverpkg.RuntimeCommandSpec{
			Command: command,
			Args:    append([]string(nil), req.GetCommand().GetArgs()...),
			Env:     env,
			Cwd:     cwd,
		},
		Cwd:         cwd,
		Env:         env,
		AttachStdin: start.GetAttachStdin(),
		TTY:         start.GetTty(),
		Rows:        size.GetRows(),
		Cols:        size.GetCols(),
		TimeoutMs:   int64(req.GetTimeoutMs()),
	}
	return &execAttachState{
		execID:  execID,
		runID:   runID,
		cwd:     cwd,
		request: req,
		sandbox: sandbox,
		vmState: vmState,
		runtime: runtime,
		spec:    spec,
		tty:     start.GetTty(),
	}, nil
}

func (h *ExecHandler) resolveExecTargetSandbox(ctx context.Context, req *agentcomposev2.ExecRequest) (*domain.Sandbox, string, error) {
	if req == nil {
		return nil, "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec request is required"))
	}
	if sandboxID := strings.TrimSpace(req.GetSandboxId()); sandboxID != "" {
		sandbox, err := h.store.GetSandbox(ctx, sandboxID)
		if err != nil {
			return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("sandbox %s not found: %w", sandboxID, err))
		}
		if sandbox.Summary.VMStatus != domain.VMStatusRunning {
			return nil, "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sandbox %s is not running", sandboxID))
		}
		return sandbox, "", nil
	}
	if runID := strings.TrimSpace(req.GetRunId()); runID != "" {
		run, err := h.projects.GetProjectRun(ctx, runID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("run %s not found: %w", runID, err))
			}
			return nil, "", connect.NewError(connect.CodeInternal, err)
		}
		sandbox, err := h.sandboxForProjectRun(ctx, run)
		if err != nil {
			return nil, "", err
		}
		return sandbox, run.RunID, nil
	}
	selector := req.GetSelector()
	if selector == nil {
		return nil, "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec target is required"))
	}
	return h.resolveExecTargetBySelector(ctx, selector)
}

// resolveExecTargetBySelector resolves the single running sandbox matching a
// project (and optionally agent) selector, erroring if none or more than one
// candidate is found.
func (h *ExecHandler) resolveExecTargetBySelector(ctx context.Context, selector *agentcomposev2.ExecSandboxSelector) (*domain.Sandbox, string, error) {
	projectRef := &agentcomposev2.ProjectRef{}
	switch project := selector.GetProject().(type) {
	case *agentcomposev2.ExecSandboxSelector_ProjectId:
		projectRef.Selector = &agentcomposev2.ProjectRef_ProjectId{ProjectId: strings.TrimSpace(project.ProjectId)}
	case *agentcomposev2.ExecSandboxSelector_ProjectName:
		projectRef.Selector = &agentcomposev2.ProjectRef_Name{Name: strings.TrimSpace(project.ProjectName)}
	}
	project, err := h.resolveProjectRef(ctx, projectRef)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", connect.NewError(connect.CodeNotFound, err)
		}
		if errors.Is(err, domain.ErrRequired) || errors.Is(err, domain.ErrAmbiguous) {
			return nil, "", connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, "", connect.NewError(connect.CodeInternal, err)
	}
	projectRuns, err := h.projects.ListProjectSandboxRuns(ctx, domain.ProjectSandboxRelationFilter{
		ProjectID: project.ID,
		AgentName: selector.GetAgentName(),
	})
	if err != nil {
		return nil, "", connect.NewError(connect.CodeInternal, err)
	}
	type candidate struct {
		sandboxID string
		run       domain.ProjectRunRecord
	}
	runBySandbox := make(map[string]domain.ProjectRunRecord, len(projectRuns))
	sandboxIDs := make([]string, 0, len(projectRuns))
	for _, run := range projectRuns {
		sandboxID := strings.TrimSpace(run.SandboxID)
		if sandboxID == "" {
			continue
		}
		if _, seen := runBySandbox[sandboxID]; seen {
			continue
		}
		runBySandbox[sandboxID] = run
		sandboxIDs = append(sandboxIDs, sandboxID)
	}
	summaries, err := h.store.ListSandboxSummaries(ctx, sandboxIDs)
	if err != nil {
		return nil, "", connect.NewError(connect.CodeInternal, err)
	}
	var candidates []candidate
	for _, sandboxID := range sandboxIDs {
		if summaries[sandboxID].VMStatus != domain.VMStatusRunning {
			continue
		}
		candidates = append(candidates, candidate{sandboxID: sandboxID, run: runBySandbox[sandboxID]})
	}
	contextParts := []string{fmt.Sprintf("project %s", project.Name)}
	if agentName := strings.TrimSpace(selector.GetAgentName()); agentName != "" {
		contextParts = append(contextParts, fmt.Sprintf("agent %s", agentName))
	}
	contextText := strings.Join(contextParts, " ")
	if len(candidates) == 0 {
		return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("no running sandbox found for %s", contextText))
	}
	if len(candidates) > 1 {
		ids := make([]string, 0, len(candidates))
		for _, item := range candidates {
			ids = append(ids, item.sandboxID)
		}
		slices.Sort(ids)
		return nil, "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("multiple runningsandboxs found for %s: %s", contextText, strings.Join(ids, ", ")))
	}
	sandbox, err := h.store.GetSandbox(ctx, candidates[0].sandboxID)
	if err != nil {
		return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("sandbox %s not found: %w", candidates[0].sandboxID, err))
	}
	if sandbox.Summary.VMStatus != domain.VMStatusRunning {
		return nil, "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sandbox %s is not running", candidates[0].sandboxID))
	}
	return sandbox, candidates[0].run.RunID, nil
}

func (h *ExecHandler) sandboxForProjectRun(ctx context.Context, run domain.ProjectRunRecord) (*domain.Sandbox, error) {
	sandboxID := strings.TrimSpace(run.SandboxID)
	if sandboxID == "" {
		sandboxID = strings.TrimSpace(run.SandboxID)
	}
	if sandboxID == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("run %s has no sandbox", run.RunID))
	}
	sandbox, err := h.store.GetSandbox(ctx, sandboxID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("sandbox %s for run %s not found: %w", sandboxID, run.RunID, err))
	}
	if sandbox.Summary.VMStatus != domain.VMStatusRunning {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sandbox %s for run %s is not running", sandboxID, run.RunID))
	}
	return sandbox, nil
}

func (h *ExecHandler) resolveProjectRef(ctx context.Context, ref *agentcomposev2.ProjectRef) (domain.ProjectRecord, error) {
	return resolveProjectReference(ctx, h.projects, ref)
}
