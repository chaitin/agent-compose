package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/chaitin/agent-compose/pkg/apierror"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runinputs"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// MaxRunInputUploadChunkBytes bounds one UploadRunInputRequest.chunk frame. It
// keeps a single frame far below transport read limits while large files still
// stream at full speed.
const MaxRunInputUploadChunkBytes = 256 << 10

// MaxRunInputUploadMessageBytes is the per-message read limit for
// UploadRunInput. The limit is measured on the encoded message, and the Connect
// JSON codec base64-encodes bytes (4/3 inflation), so it allows twice the chunk
// size. Connect decodes a whole message before the handler sees it, so without
// this limit an oversized frame would be buffered in memory before the chunk
// check rejects it.
const MaxRunInputUploadMessageBytes = 2 * MaxRunInputUploadChunkBytes

// UploadRunInputHandlerOptions is the handler option set for the RunService
// instance that serves UploadRunInput. The shared error contract still applies;
// the read limit bounds each frame the client sends.
func UploadRunInputHandlerOptions() []connect.HandlerOption {
	return append(ErrorContractHandlerOptions(), connect.WithReadMaxBytes(MaxRunInputUploadMessageBytes))
}

// RunInputUploadStore is the staging area UploadRunInput writes to.
type RunInputUploadStore interface {
	Begin(projectID string, size int64, digest string) (*runinputs.Upload, *runinputs.Object, error)
}

// WithRunInputUploads enables UploadRunInput. Without it the RPC is
// unimplemented and the upload source reports zero limits elsewhere.
func (h *RunHandler) WithRunInputUploads(store RunInputUploadStore) *RunHandler {
	h.uploads = store
	return h
}

// UploadRunInput stages one file. Every failure aborts the upload, so an error
// never leaves a referenceable object behind.
func (h *RunHandler) UploadRunInput(ctx context.Context, stream *connect.ClientStream[agentcomposev2.UploadRunInputRequest]) (*connect.Response[agentcomposev2.UploadRunInputResponse], error) {
	if h.uploads == nil {
		return nil, WriteErrorNotAppliedForCode(connect.CodeUnimplemented, ResourceGeneric, fmt.Errorf("run input upload is not configured"))
	}
	if !stream.Receive() {
		if err := stream.Err(); err != nil {
			// The info frame never arrived, so nothing was staged.
			return nil, WriteErrorNotAppliedForCode(connect.CodeOf(err), ResourceGeneric, err)
		}
		return nil, uploadInvalid(fmt.Errorf("upload requires an info frame"))
	}
	info := stream.Msg().GetInfo()
	if info == nil {
		return nil, uploadInvalid(fmt.Errorf("the first upload frame must be info"))
	}
	projectID := strings.TrimSpace(info.GetProjectId())
	if projectID == "" {
		return nil, uploadInvalid(fmt.Errorf("project_id is required"))
	}
	project, err := h.store.GetProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, WriteErrorNotAppliedForCode(connect.CodeNotFound, ResourceProject, err)
		}
		return nil, ConnectErrorForDomainWrite(err, ResourceProject)
	}
	size := info.GetSizeBytes()
	if size > uint64(1<<62) {
		return nil, uploadInvalid(fmt.Errorf("size_bytes is out of range"))
	}
	upload, existing, err := h.uploads.Begin(project.ID, int64(size), info.GetSha256())
	if err != nil {
		return nil, uploadError(err)
	}
	if existing != nil {
		return uploadResponse(*existing, true), nil
	}
	defer upload.Abort()
	for stream.Receive() {
		switch frame := stream.Msg().GetFrame().(type) {
		case *agentcomposev2.UploadRunInputRequest_Chunk:
			if len(frame.Chunk) > MaxRunInputUploadChunkBytes {
				return nil, uploadInvalid(fmt.Errorf("chunk is %d bytes, limit is %d", len(frame.Chunk), MaxRunInputUploadChunkBytes))
			}
			if err := upload.Write(frame.Chunk); err != nil {
				return nil, uploadError(err)
			}
		case *agentcomposev2.UploadRunInputRequest_Info:
			return nil, uploadInvalid(fmt.Errorf("info may appear only as the first frame"))
		default:
			return nil, uploadInvalid(fmt.Errorf("upload frame is empty"))
		}
	}
	if err := stream.Err(); err != nil {
		// The client went away mid-upload or the transport cut the stream; the
		// deferred abort removes the partial file and releases its reservation.
		return nil, WriteErrorNotAppliedForCode(connect.CodeOf(err), ResourceGeneric, err)
	}
	object, deduplicated, err := upload.Commit()
	if err != nil {
		return nil, uploadError(err)
	}
	return uploadResponse(object, deduplicated), nil
}

func uploadResponse(object runinputs.Object, deduplicated bool) *connect.Response[agentcomposev2.UploadRunInputResponse] {
	return connect.NewResponse(&agentcomposev2.UploadRunInputResponse{
		UploadId:     object.UploadID,
		ExpiresAt:    timestamppb.New(object.ExpiresAt),
		Deduplicated: deduplicated,
	})
}

func uploadInvalid(err error) error {
	return WriteErrorNotAppliedForCode(connect.CodeInvalidArgument, ResourceGeneric, err)
}

// uploadError maps a staging failure. Validation, capacity, and I/O failures
// all leave nothing referenceable: an object becomes referenceable only when
// Commit indexes it, and the deferred Abort removes any partial file and
// releases the bytes charged so far.
func uploadError(err error) error {
	switch {
	case errors.Is(err, runinputs.ErrInvalid):
		return uploadInvalid(err)
	case errors.Is(err, runinputs.ErrExhausted):
		marked := apierror.NotApplied(err)
		return NewOperationError(connect.CodeResourceExhausted, apierror.ReasonStorageUnavailable, apierror.EffectFor(marked), marked)
	default:
		return WriteErrorNotAppliedForCode(connect.CodeInternal, ResourceGeneric, err)
	}
}
