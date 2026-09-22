package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func errorFromString(text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return errors.New(text)
}

// execPreparation is the exec context prepareExecCommand assembles before
// executeProjectCommand runs the command: the resolved cwd, VM state,
// runtime handle, and the host/guest artifact directories, with the
// command-request artifact already written to hostExecDir.
type execPreparation struct {
	Cwd          string
	VMState      domain.VMState
	Runtime      ExecRuntime
	HostExecDir  string
	GuestExecDir string
}

func (h *ExecHandler) prepareExecCommand(sandbox *domain.Sandbox, req *agentcomposev2.ExecRequest, execID string) (execPreparation, error) {
	appconfig.ApplyDefaultGuestPaths(h.config)
	cwd := strings.TrimSpace(req.GetCwd())
	if cwd == "" {
		cwd = h.config.GuestWorkspacePath
	}
	vmState, err := h.store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		return execPreparation{}, connect.NewError(connect.CodeInternal, err)
	}
	runtime, err := h.runtime(sandbox)
	if err != nil {
		return execPreparation{}, ConnectErrorForDomain(err)
	}
	hostExecDir := filepath.Join(execution.HostSandboxDir(sandbox), "state", "exec", execID)
	if err := os.MkdirAll(hostExecDir, 0o755); err != nil {
		return execPreparation{}, connect.NewError(connect.CodeInternal, fmt.Errorf("create exec artifact dir: %w", err))
	}
	guestExecDir := filepath.Join(h.config.GuestStateRoot, "exec", execID)
	runtimeRequest := execution.RuntimeCommandRequestPayloadFromCommand(h.config, execution.RuntimeCommandSpec{
		Mode:           "exec",
		Command:        strings.TrimSpace(req.GetCommand().GetCommand()),
		Args:           req.GetCommand().GetArgs(),
		Cwd:            cwd,
		Env:            ExecEnvMap(req.GetEnv()),
		TimeoutMs:      int64(req.GetTimeoutMs()),
		MaxOutputBytes: int64(req.GetMaxOutputBytes()),
	}, guestExecDir)
	if err := execution.WriteJSONArtifact(filepath.Join(hostExecDir, "command-request.json"), runtimeRequest); err != nil {
		return execPreparation{}, connect.NewError(connect.CodeInternal, fmt.Errorf("write exec command request artifact: %w", err))
	}
	return execPreparation{Cwd: cwd, VMState: vmState, Runtime: runtime, HostExecDir: hostExecDir, GuestExecDir: guestExecDir}, nil
}

// execStreamWriterRequest bundles the identifiers newExecStreamWriter needs
// to label each streamed output chunk.
type execStreamWriterRequest struct {
	TranscriptPath string
	ExecID         string
	SandboxID      string
	RunID          string
	Send           execStreamSender
}

// newExecStreamWriter builds the ExecStreamWriter that appends each output
// chunk to the on-disk transcript and, if req.Send is non-nil, streams it to
// the caller. The returned pointer holds the first streaming error
// encountered; the caller checks it after ExecStream returns.
func newExecStreamWriter(req execStreamWriterRequest) (domain.ExecStreamWriter, *error) {
	var sendErr error
	writer := func(chunk domain.ExecChunk) {
		if sendErr != nil {
			return
		}
		filtered, visible := execution.FilterCommandStreamChunk(chunk)
		if !visible {
			return
		}
		if err := appendExecTranscriptChunk(req.TranscriptPath, filtered); err != nil {
			sendErr = err
			return
		}
		if req.Send != nil {
			createdAt := time.Now().UTC()
			sendErr = req.Send(&agentcomposev2.StreamExecResponse{
				EventType:  agentcomposev2.StreamExecEventType_STREAM_EXEC_EVENT_TYPE_OUTPUT,
				ExecId:     req.ExecID,
				SandboxId:  req.SandboxID,
				RunId:      req.RunID,
				Chunk:      filtered.Text,
				Stream:     StdioStreamToProto(filtered.Stream),
				Transcript: TranscriptEventFromExecChunk(filtered, createdAt),
			})
		}
	}
	return writer, &sendErr
}

func (h *ExecHandler) executeProjectCommand(ctx context.Context, req *agentcomposev2.ExecRequest, execID string, send execStreamSender) (*agentcomposev2.ExecResult, error) {
	if h.store == nil || h.projects == nil || h.runtime == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("exec runtime dependencies are required"))
	}
	sandbox, _, err := h.resolveExecTargetSandbox(ctx, req)
	if err != nil {
		return nil, err
	}
	unlock := h.locks.Lock(sandbox.Summary.ID)
	defer unlock()
	sandbox, runID, err := h.resolveExecTargetSandbox(ctx, req)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetCommand().GetCommand()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec command is required"))
	}
	if send != nil {
		if err := send(&agentcomposev2.StreamExecResponse{
			EventType: agentcomposev2.StreamExecEventType_STREAM_EXEC_EVENT_TYPE_STARTED,
			ExecId:    execID,
			SandboxId: sandbox.Summary.ID,
			RunId:     runID,
		}); err != nil {
			return nil, connect.NewError(connect.CodeUnknown, err)
		}
	}
	prep, err := h.prepareExecCommand(sandbox, req, execID)
	if err != nil {
		return nil, err
	}
	writer, sendErr := newExecStreamWriter(execStreamWriterRequest{
		TranscriptPath: filepath.Join(prep.HostExecDir, "transcript.txt"),
		ExecID:         execID,
		SandboxID:      sandbox.Summary.ID,
		RunID:          runID,
		Send:           send,
	})
	execCtx, cancel := execution.ExecContext(ctx, req.GetTimeoutMs())
	defer cancel()
	result, execErr := prep.Runtime.ExecStream(execCtx, sandbox, prep.VMState, execution.BuildRuntimeCommandExecSpec(execCtx, h.config, sandbox, filepath.Join(prep.GuestExecDir, "command-request.json"), h.config.GuestHomePath), writer)
	if *sendErr != nil {
		return nil, connect.NewError(connect.CodeUnknown, *sendErr)
	}
	if execErr != nil {
		result.ExitCode = execution.FirstNonZeroInt(result.ExitCode, 1)
		result.Success = false
		if strings.TrimSpace(result.Output) == "" {
			result.Output = firstNonEmpty(result.Stderr, result.Stdout, execErr.Error())
		}
		return ExecResultToProto(ExecResultProtoRequest{
			ExecID:    execID,
			SandboxID: sandbox.Summary.ID,
			RunID:     runID,
			Request:   req,
			Cwd:       prep.Cwd,
			Result:    result,
			ExecErr:   execErr,
		}), nil
	}
	commandResult, err := execution.ParseCommandExecResult(result)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := execution.MirrorRuntimeCommandArtifacts(prep.HostExecDir, commandResult); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return ExecResultToProto(ExecResultProtoRequest{
		ExecID:    execID,
		SandboxID: sandbox.Summary.ID,
		RunID:     runID,
		Request:   req,
		Cwd:       prep.Cwd,
		Result:    execution.RuntimeCommandResultToExecResult(commandResult),
	}), nil
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
