package api

import (
	"context"
	"errors"
	"fmt"
	"io"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/workspaces"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

const (
	sandboxWorkspaceDownloadChunk = 64 << 10
)

func (h *SandboxHandler) ListSandboxWorkspace(
	ctx context.Context,
	req *connect.Request[agentcomposev2.ListSandboxWorkspaceRequest],
) (*connect.Response[agentcomposev2.ListSandboxWorkspaceResponse], error) {
	sandbox, err := h.loadSandbox(ctx, req.Msg.GetSandboxId())
	if err != nil {
		return nil, ConnectErrorForDomainKind(err, ResourceSandbox)
	}
	if h.workspaceFiles == nil {
		return nil, ErrorForCode(connect.CodeInternal, ResourceSandbox, fmt.Errorf("workspace file reader is not configured"))
	}
	offset, limit, err := listPagination(req.Msg.GetOffset(), req.Msg.GetLimit())
	if err != nil {
		return nil, err
	}
	entries, total, err := h.workspaceFiles.ListSandboxFiles(ctx, sandbox, req.Msg.GetPath(), offset, limit)
	if err != nil {
		return nil, sandboxWorkspaceConnectError(err)
	}
	response := &agentcomposev2.ListSandboxWorkspaceResponse{
		Entries: make([]*agentcomposev2.SandboxWorkspaceEntry, 0, len(entries)),
		Total:   uint32(total),
	}
	for _, entry := range entries {
		response.Entries = append(response.Entries, &agentcomposev2.SandboxWorkspaceEntry{
			Path:       entry.Path,
			Name:       entry.Name,
			Type:       sandboxWorkspaceEntryTypeToProto(entry.Kind),
			Size:       uint64(max(entry.Size, 0)),
			ModifiedAt: timestamppb.New(entry.ModTime),
		})
	}
	return connect.NewResponse(response), nil
}

func (h *SandboxHandler) DownloadSandboxWorkspaceFile(
	ctx context.Context,
	req *connect.Request[agentcomposev2.DownloadSandboxWorkspaceFileRequest],
	stream *connect.ServerStream[agentcomposev2.DownloadSandboxWorkspaceFileResponse],
) error {
	PrepareStreamingHeaders(stream.ResponseHeader())
	sandbox, err := h.loadSandbox(ctx, req.Msg.GetSandboxId())
	if err != nil {
		return ConnectErrorForDomainKind(err, ResourceSandbox)
	}
	if h.workspaceFiles == nil {
		return ErrorForCode(connect.CodeInternal, ResourceSandbox, fmt.Errorf("workspace file reader is not configured"))
	}
	file, info, err := h.workspaceFiles.OpenSandboxFile(ctx, sandbox, req.Msg.GetPath())
	if err != nil {
		return sandboxWorkspaceConnectError(err)
	}
	defer func() { _ = file.Close() }()

	if err := stream.Send(&agentcomposev2.DownloadSandboxWorkspaceFileResponse{
		Payload: &agentcomposev2.DownloadSandboxWorkspaceFileResponse_Info{
			Info: &agentcomposev2.SandboxWorkspaceFileInfo{
				Path:       info.Path,
				Size:       uint64(max(info.Size, 0)),
				ModifiedAt: timestamppb.New(info.ModTime),
			},
		},
	}); err != nil {
		return err
	}

	buffer := make([]byte, sandboxWorkspaceDownloadChunk)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			// ServerStream.Send marshals synchronously, so the buffer can be
			// reused after Send returns; no per-chunk copy is needed.
			if err := stream.Send(&agentcomposev2.DownloadSandboxWorkspaceFileResponse{
				Payload: &agentcomposev2.DownloadSandboxWorkspaceFileResponse_Chunk{Chunk: buffer[:n]},
			}); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return sandboxWorkspaceConnectError(fmt.Errorf("read workspace file: %w", readErr))
		}
	}
}

func sandboxWorkspaceEntryTypeToProto(kind workspaces.SandboxEntryKind) agentcomposev2.SandboxWorkspaceEntryType {
	switch kind {
	case workspaces.SandboxEntryFile:
		return agentcomposev2.SandboxWorkspaceEntryType_SANDBOX_WORKSPACE_ENTRY_TYPE_FILE
	case workspaces.SandboxEntryDirectory:
		return agentcomposev2.SandboxWorkspaceEntryType_SANDBOX_WORKSPACE_ENTRY_TYPE_DIRECTORY
	case workspaces.SandboxEntrySymlink:
		return agentcomposev2.SandboxWorkspaceEntryType_SANDBOX_WORKSPACE_ENTRY_TYPE_SYMLINK
	case workspaces.SandboxEntryOther:
		return agentcomposev2.SandboxWorkspaceEntryType_SANDBOX_WORKSPACE_ENTRY_TYPE_OTHER
	default:
		return agentcomposev2.SandboxWorkspaceEntryType_SANDBOX_WORKSPACE_ENTRY_TYPE_UNSPECIFIED
	}
}

func sandboxWorkspaceConnectError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		// The sandbox exists; only the requested workspace path is missing.
		// ResourceGeneric maps this to RESOURCE_NOT_FOUND instead of
		// SANDBOX_NOT_FOUND.
		return ErrorForCode(connect.CodeNotFound, ResourceGeneric, err)
	case errors.Is(err, context.Canceled):
		return ErrorForCode(connect.CodeCanceled, ResourceSandbox, err)
	case errors.Is(err, context.DeadlineExceeded):
		return ErrorForCode(connect.CodeDeadlineExceeded, ResourceSandbox, err)
	case errors.Is(err, domain.ErrFailedPrecondition):
		return ErrorForCode(connect.CodeFailedPrecondition, ResourceSandbox, err)
	case errors.Is(err, domain.ErrUnsupported):
		return ErrorForCode(connect.CodeUnimplemented, ResourceSandbox, err)
	case errors.Is(err, domain.ErrInvalidArgument), errors.Is(err, domain.ErrRequired):
		return ErrorForCode(connect.CodeInvalidArgument, ResourceSandbox, err)
	default:
		return ErrorForCode(connect.CodeInternal, ResourceSandbox, err)
	}
}
