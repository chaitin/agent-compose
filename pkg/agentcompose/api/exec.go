package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/runs"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type ExecSessionStore interface {
	GetSession(context.Context, string) (*domain.Session, error)
	GetVMState(string) (domain.VMState, error)
}

type ExecProjectStore interface {
	GetProject(context.Context, string) (domain.ProjectRecord, error)
	GetProjectRun(context.Context, string) (domain.ProjectRunRecord, error)
	ListProjects(context.Context, domain.ProjectListOptions) (domain.ProjectListResult, error)
	ListProjectSessionRuns(context.Context, domain.ProjectSessionRelationFilter) ([]domain.ProjectRunRecord, error)
}

type ExecRuntime interface {
	ExecStream(context.Context, *domain.Session, domain.VMState, domain.ExecSpec, domain.ExecStreamWriter) (domain.ExecResult, error)
}

type ExecRuntimeResolver func(*domain.Session) (ExecRuntime, error)

type ExecHandler struct {
	config   *appconfig.Config
	store    ExecSessionStore
	projects ExecProjectStore
	runtime  ExecRuntimeResolver
}

func NewExecHandler(config *appconfig.Config, store ExecSessionStore, projects ExecProjectStore, runtime ExecRuntimeResolver) *ExecHandler {
	return &ExecHandler{
		config:   config,
		store:    store,
		projects: projects,
		runtime:  runtime,
	}
}

func (h *ExecHandler) Exec(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest]) (*connect.Response[agentcomposev2.ExecResponse], error) {
	result, err := h.executeProjectCommand(ctx, req.Msg, uuid.NewString(), nil)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentcomposev2.ExecResponse{Result: result}), nil
}

func (h *ExecHandler) ExecStream(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest], stream *connect.ServerStream[agentcomposev2.ExecStreamResponse]) error {
	execID := uuid.NewString()
	result, err := h.executeProjectCommand(ctx, req.Msg, execID, func(resp *agentcomposev2.ExecStreamResponse) error {
		return stream.Send(resp)
	})
	if err != nil {
		return err
	}
	return stream.Send(&agentcomposev2.ExecStreamResponse{
		EventType: agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_COMPLETED,
		ExecId:    execID,
		SessionId: result.GetSessionId(),
		RunId:     result.GetRunId(),
		Result:    result,
	})
}

type execStreamSender func(*agentcomposev2.ExecStreamResponse) error

func (h *ExecHandler) executeProjectCommand(ctx context.Context, req *agentcomposev2.ExecRequest, execID string, send execStreamSender) (*agentcomposev2.ExecResult, error) {
	if h.store == nil || h.projects == nil || h.runtime == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("exec runtime dependencies are required"))
	}
	session, runID, err := h.resolveExecTargetSession(ctx, req)
	if err != nil {
		return nil, err
	}
	command := strings.TrimSpace(req.GetCommand().GetCommand())
	if command == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec command is required"))
	}
	if send != nil {
		if err := send(&agentcomposev2.ExecStreamResponse{
			EventType: agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_STARTED,
			ExecId:    execID,
			SessionId: session.Summary.ID,
			RunId:     runID,
		}); err != nil {
			return nil, connect.NewError(connect.CodeUnknown, err)
		}
	}
	appconfig.ApplyDefaultGuestPaths(h.config)
	cwd := strings.TrimSpace(req.GetCwd())
	if cwd == "" {
		cwd = h.config.GuestWorkspacePath
	}
	vmState, err := h.store.GetVMState(session.Summary.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	runtime, err := h.runtime(session)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	hostExecDir := filepath.Join(execution.HostSessionDir(session), "state", "exec", execID)
	if err := os.MkdirAll(hostExecDir, 0o755); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create exec artifact dir: %w", err))
	}
	guestExecDir := filepath.Join(h.config.GuestStateRoot, "exec", execID)
	runtimeRequest := execution.RuntimeCommandRequestPayloadFromCommand(
		h.config,
		"exec",
		command,
		req.GetCommand().GetArgs(),
		"",
		cwd,
		ExecEnvMap(req.GetEnv()),
		int64(req.GetTimeoutMs()),
		int64(req.GetMaxOutputBytes()),
		guestExecDir,
	)
	if err := execution.WriteJSONArtifact(filepath.Join(hostExecDir, "command-request.json"), runtimeRequest); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("write exec command request artifact: %w", err))
	}
	transcriptPath := filepath.Join(hostExecDir, "transcript.txt")
	var sendErr error
	writer := func(chunk domain.ExecChunk) {
		if sendErr != nil {
			return
		}
		if err := appendExecTranscriptChunk(transcriptPath, chunk); err != nil {
			sendErr = err
			return
		}
		if send != nil {
			createdAt := time.Now().UTC()
			sendErr = send(&agentcomposev2.ExecStreamResponse{
				EventType:  agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_OUTPUT,
				ExecId:     execID,
				SessionId:  session.Summary.ID,
				RunId:      runID,
				Chunk:      chunk.Text,
				IsStderr:   chunk.IsStderr,
				Transcript: TranscriptEventFromExecChunk(chunk, createdAt),
			})
		}
	}
	execCtx, cancel := execution.ExecContext(ctx, req.GetTimeoutMs())
	defer cancel()
	result, execErr := runtime.ExecStream(execCtx, session, vmState, execution.BuildRuntimeCommandExecSpec(h.config, session, filepath.Join(guestExecDir, "command-request.json"), h.config.GuestHomePath), writer)
	if sendErr != nil {
		return nil, connect.NewError(connect.CodeUnknown, sendErr)
	}
	if execErr != nil {
		result.ExitCode = execution.FirstNonZeroInt(result.ExitCode, 1)
		result.Success = false
		if strings.TrimSpace(result.Output) == "" {
			result.Output = firstNonEmpty(result.Stderr, result.Stdout, execErr.Error())
		}
		return ExecResultToProto(execID, session.Summary.ID, runID, req, cwd, result, execErr), nil
	}
	commandResult, err := execution.ParseCommandExecResult(result)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := execution.MirrorRuntimeCommandArtifacts(hostExecDir, commandResult); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return ExecResultToProto(execID, session.Summary.ID, runID, req, cwd, execution.RuntimeCommandResultToExecResult(commandResult), nil), nil
}

func (h *ExecHandler) resolveExecTargetSession(ctx context.Context, req *agentcomposev2.ExecRequest) (*domain.Session, string, error) {
	if req == nil {
		return nil, "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec request is required"))
	}
	if sessionID := strings.TrimSpace(req.GetSessionId()); sessionID != "" {
		session, err := h.store.GetSession(ctx, sessionID)
		if err != nil {
			return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s not found: %w", sessionID, err))
		}
		if session.Summary.VMStatus != domain.VMStatusRunning {
			return nil, "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s is not running", sessionID))
		}
		return session, "", nil
	}
	if runID := strings.TrimSpace(req.GetRunId()); runID != "" {
		run, err := h.projects.GetProjectRun(ctx, runID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("run %s not found: %w", runID, err))
			}
			return nil, "", connect.NewError(connect.CodeInternal, err)
		}
		session, err := h.sessionForProjectRun(ctx, run)
		if err != nil {
			return nil, "", err
		}
		return session, run.RunID, nil
	}
	selector := req.GetSelector()
	if selector == nil {
		return nil, "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec target is required"))
	}
	project, err := h.resolveProjectRef(ctx, &agentcomposev2.ProjectRef{
		ProjectId: selector.GetProjectId(),
		Name:      selector.GetProjectName(),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", connect.NewError(connect.CodeNotFound, err)
		}
		if errors.Is(err, domain.ErrRequired) || errors.Is(err, domain.ErrAmbiguous) {
			return nil, "", connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, "", connect.NewError(connect.CodeInternal, err)
	}
	statuses, err := runs.ListProjectSessionStatuses(ctx, h.projects, h.store, domain.ProjectSessionRelationFilter{
		ProjectID: project.ID,
		AgentName: selector.GetAgentName(),
	})
	if err != nil {
		return nil, "", connect.NewError(connect.CodeInternal, err)
	}
	type candidate struct {
		session *domain.Session
		run     domain.ProjectRunRecord
	}
	var candidates []candidate
	for _, status := range statuses {
		if status.Session == nil || status.Session.Summary.VMStatus != domain.VMStatusRunning {
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

func (h *ExecHandler) sessionForProjectRun(ctx context.Context, run domain.ProjectRunRecord) (*domain.Session, error) {
	sessionID := strings.TrimSpace(run.SessionID)
	if sessionID == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("run %s has no session", run.RunID))
	}
	session, err := h.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s for run %s not found: %w", sessionID, run.RunID, err))
	}
	if session.Summary.VMStatus != domain.VMStatusRunning {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s for run %s is not running", sessionID, run.RunID))
	}
	return session, nil
}

func appendExecTranscriptChunk(path string, chunk domain.ExecChunk) error {
	path = strings.TrimSpace(path)
	if path == "" || chunk.Text == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create exec transcript dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open exec transcript %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(chunk.Text); err != nil {
		return fmt.Errorf("append exec transcript %s: %w", path, err)
	}
	return nil
}

func (h *ExecHandler) resolveProjectRef(ctx context.Context, ref *agentcomposev2.ProjectRef) (domain.ProjectRecord, error) {
	if ref == nil {
		return domain.ProjectRecord{}, domain.ClassifyError(domain.ErrRequired, "project ref is required", nil)
	}
	if projectID := strings.TrimSpace(ref.GetProjectId()); projectID != "" {
		return h.projects.GetProject(ctx, projectID)
	}
	name := strings.TrimSpace(ref.GetName())
	sourcePath := strings.TrimSpace(ref.GetSourcePath())
	if name != "" && sourcePath != "" {
		projectID, err := domain.StableProjectID(name, sourcePath)
		if err != nil {
			return domain.ProjectRecord{}, err
		}
		return h.projects.GetProject(ctx, projectID)
	}
	if name == "" {
		return domain.ProjectRecord{}, domain.ClassifyError(domain.ErrRequired, "project id or name is required", nil)
	}
	result, err := h.projects.ListProjects(ctx, domain.ProjectListOptions{Query: name, Limit: 200})
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

func ExecEnvMap(items []*agentcomposev2.EnvVarSpec) map[string]string {
	if len(items) == 0 {
		return nil
	}
	result := make(map[string]string, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.GetName())
		if name == "" {
			continue
		}
		result[name] = item.GetValue()
	}
	return result
}

func ExecResultToProto(execID, sessionID, runID string, req *agentcomposev2.ExecRequest, cwd string, result domain.ExecResult, execErr error) *agentcomposev2.ExecResult {
	errorText := ""
	if execErr != nil {
		errorText = execErr.Error()
	}
	return &agentcomposev2.ExecResult{
		ExecId:    execID,
		SessionId: sessionID,
		RunId:     runID,
		Command: &agentcomposev2.ExecCommand{
			Command: req.GetCommand().GetCommand(),
			Args:    append([]string(nil), req.GetCommand().GetArgs()...),
		},
		Cwd:      cwd,
		ExitCode: int32(result.ExitCode),
		Success:  result.Success,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		Output:   result.Output,
		Error:    errorText,
	}
}
