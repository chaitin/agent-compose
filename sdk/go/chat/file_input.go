package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"strings"
	"sync"

	"connectrpc.com/connect"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// uploadChunkBytes caps one UploadRunInput chunk. The daemon may advertise a
// smaller max_upload_chunk_bytes, and an upload uses whichever is smaller.
const uploadChunkBytes = 256 << 10

// InputFile supplies one immutable file. The SDK closes Content on every exit.
// Content must support concurrent Close to interrupt a blocked Read.
//
// ID is optional and caller-chosen: the SDK uses it only to reject two files
// that share an ID in one turn. MediaType is optional and informational.
type InputFile struct {
	ID        string
	Name      string
	MediaType string
	Size      uint64
	SHA256    []byte
	Content   io.ReadCloser
}

// SendOption configures one turn.
type SendOption func(*sendOptions)

type sendOptions struct {
	files     []InputFile
	requestID string
}

func newSendOptions(opts []SendOption) sendOptions {
	var resolved sendOptions
	for _, option := range opts {
		if option != nil {
			option(&resolved)
		}
	}
	return resolved
}

// WithFiles attaches files to this turn. Each file travels the cheaper way the
// daemon allows: one within its inline limits is sent with the request, and a
// larger one is staged through UploadRunInput first and referenced by upload id.
// The SDK closes each Content on every exit, and files can ride any turn, not
// only a run's opening one.
func WithFiles(files ...InputFile) SendOption {
	return func(options *sendOptions) { options.files = append(options.files, files...) }
}

// WithRequestID pins the idempotency key of a turn that starts a new run:
// retrying with the same key resolves to the same persistent run. It is ignored
// when the turn continues an existing run, where the message's client frame ID
// is the identity. A caller that persists the key before sending can retry
// after a lost outcome without creating a second run.
func WithRequestID(id string) SendOption {
	return func(options *sendOptions) { options.requestID = id }
}

// inputFiles closes every caller-supplied reader exactly once.
type inputFiles struct {
	once  sync.Once
	files []InputFile
	err   error
}

func (f *inputFiles) Close() error {
	if f == nil {
		return nil
	}
	f.once.Do(func() {
		for _, file := range f.files {
			if file.Content != nil {
				f.err = errors.Join(f.err, file.Content.Close())
			}
		}
	})
	return f.err
}

// prepareFiles decides how each file travels, checks the daemon's limits and
// assembles the ordered input parts for this turn. Small files ride inline with
// the request; the rest are staged through UploadRunInput. A daemon without
// file input at all is rejected, never silently reduced to a text-only turn.
func (c *Conversation) prepareFiles(ctx context.Context, files []InputFile) ([]*agentcomposev2.AgentInputPart, error) {
	if len(files) == 0 {
		return nil, nil
	}
	limits, err := c.inputLimits(ctx)
	if err != nil {
		return nil, err
	}
	if limits.GetMaxFiles() == 0 {
		return nil, invalidArgument("Send", "daemon does not support file input")
	}
	if uint32(len(files)) > limits.GetMaxFiles() {
		return nil, invalidArgument("Send", "%d files exceed the daemon limit of %d", len(files), limits.GetMaxFiles())
	}
	// Validate and route every declaration before moving any bytes: a bad file
	// at the end must not leave the earlier ones uploaded and unreferenced.
	placements, err := planInputFiles(files, limits)
	if err != nil {
		return nil, err
	}
	chunkBytes := int(min(uint64(uploadChunkBytes), uint64(limits.GetMaxUploadChunkBytes())))
	parts := make([]*agentcomposev2.AgentInputPart, 0, len(placements))
	for _, placement := range placements {
		file, err := c.buildInputFile(ctx, placement, chunkBytes)
		if err != nil {
			return nil, err
		}
		parts = append(parts, &agentcomposev2.AgentInputPart{Part: &agentcomposev2.AgentInputPart_File{File: file}})
	}
	return parts, nil
}

// inputPlacement is one file's route: inline with the start request, or staged
// through UploadRunInput first.
type inputPlacement struct {
	file   InputFile
	inline bool
}

// planInputFiles admits every declaration, then routes each file in the given
// order to inline bytes or an upload. The inline and upload budgets are
// separate on the daemon, so a small file goes inline whenever it fits and only
// the rest is staged; a file that fits neither is an error rather than a
// silently dropped part.
func planInputFiles(files []InputFile, limits *agentcomposev2.EngineInputLimits) ([]inputPlacement, error) {
	seen := make(map[string]bool, len(files))
	placements := make([]inputPlacement, 0, len(files))
	var inlineTotal, uploadTotal uint64
	for _, file := range files {
		if file.Content == nil || file.Name == "" || strings.ContainsAny(file.Name, "/\\\x00") || len(file.SHA256) != sha256.Size || file.Size == 0 {
			return nil, invalidArgument("Send", "invalid file declaration")
		}
		if file.ID != "" {
			if seen[file.ID] {
				return nil, invalidArgument("Send", "two files share the id %q", file.ID)
			}
			seen[file.ID] = true
		}
		switch {
		case file.Size <= limits.GetMaxFileBytes() && withinBudget(file.Size, limits.GetMaxTurnBytes(), inlineTotal):
			inlineTotal += file.Size
			placements = append(placements, inputPlacement{file: file, inline: true})
		case limits.GetMaxUploadChunkBytes() > 0 && file.Size <= limits.GetMaxUploadFileBytes() && withinBudget(file.Size, limits.GetMaxUploadTurnBytes(), uploadTotal):
			uploadTotal += file.Size
			placements = append(placements, inputPlacement{file: file})
		default:
			return nil, invalidArgument("Send", "file %q (%d bytes) fits neither the inline nor the upload limits", file.Name, file.Size)
		}
	}
	return placements, nil
}

// withinBudget reports whether size still fits a per-turn budget with used
// already spent. A zero budget or zero per-turn limit means the route is
// unavailable.
func withinBudget(size, budget, used uint64) bool {
	if budget == 0 || used > budget {
		return false
	}
	return size <= budget-used
}

// buildInputFile produces the part source for one planned file.
func (c *Conversation) buildInputFile(ctx context.Context, placement inputPlacement, chunkBytes int) (*agentcomposev2.AgentInputFile, error) {
	if !placement.inline {
		uploadID, err := c.uploadFile(ctx, placement.file, chunkBytes)
		if err != nil {
			return nil, err
		}
		return &agentcomposev2.AgentInputFile{
			Filename:  placement.file.Name,
			MediaType: placement.file.MediaType,
			Source:    &agentcomposev2.AgentInputFile_Upload{Upload: &agentcomposev2.AgentInputFileUpload{UploadId: uploadID}},
		}, nil
	}
	data, err := readInputFile(placement.file)
	if err != nil {
		return nil, err
	}
	return &agentcomposev2.AgentInputFile{
		Filename:  placement.file.Name,
		MediaType: placement.file.MediaType,
		Source: &agentcomposev2.AgentInputFile_Bytes{Bytes: &agentcomposev2.AgentInputFileBytes{
			Data:   data,
			Sha256: hex.EncodeToString(placement.file.SHA256),
		}},
	}, nil
}

// readInputFile reads exactly the declared bytes into memory. Inline is chosen
// only for files within the daemon's max_file_bytes, so the allocation is
// bounded by that limit. Overshoot and a short file are both caller errors,
// caught before the Run exists.
func readInputFile(file InputFile) ([]byte, error) {
	// int64(file.Size)+1 below must not wrap; a daemon advertising an
	// astronomically large inline limit must not turn into a negative read.
	if file.Size > math.MaxInt64-1 {
		return nil, invalidArgument("Send", "file %q is too large to read inline", file.Name)
	}
	data, err := io.ReadAll(io.LimitReader(file.Content, int64(file.Size)+1))
	if err != nil {
		return nil, err
	}
	switch {
	case uint64(len(data)) > file.Size:
		return nil, invalidArgument("Send", "file %q content exceeds declared size", file.Name)
	case uint64(len(data)) < file.Size:
		return nil, invalidArgument("Send", "file %q ended after %d of %d bytes", file.Name, len(data), file.Size)
	}
	return data, nil
}

// inputLimits reads the daemon's engine input limits. The upload fields are
// zero on a daemon without the upload source; such a daemon can still take
// files inline, up to its inline limits.
func (c *Conversation) inputLimits(ctx context.Context) (*agentcomposev2.EngineInputLimits, error) {
	response, err := c.agent.client.transport.engine.GetCapabilities(ctx, connect.NewRequest(&agentcomposev2.GetEngineCapabilitiesRequest{}))
	if err != nil {
		return nil, fromConnect("Send", err)
	}
	return response.Msg.GetInputLimits(), nil
}

// uploadFile streams one file into the staging area and returns the opaque
// upload id to reference.
//
// The daemon answers as soon as it can: content it already has returns its
// upload id before the last chunk is sent, and a rejected declaration fails
// before the bytes end. A failed Send is therefore not the upload's outcome —
// CloseAndReceive reports the authoritative one.
func (c *Conversation) uploadFile(ctx context.Context, file InputFile, chunkBytes int) (string, error) {
	stream := c.agent.client.transport.runs.UploadRunInput(ctx)
	// Closing the send side is what makes the daemon finish: its handler stays
	// blocked on Receive - holding the partial upload's reservation - until the
	// request ends. CloseAndReceive does this itself; a local failure before it
	// must not leave the stream half open.
	closed := false
	defer func() {
		if closed {
			return
		}
		if conn, err := stream.Conn(); err == nil {
			_ = conn.CloseRequest()
			_ = conn.CloseResponse()
		}
	}()
	if err := stream.Send(&agentcomposev2.UploadRunInputRequest{Frame: &agentcomposev2.UploadRunInputRequest_Info{Info: &agentcomposev2.RunInputUploadInfo{
		ProjectId: c.agent.projectID,
		SizeBytes: file.Size,
		Sha256:    hex.EncodeToString(file.SHA256),
	}}}); err != nil {
		return "", fromConnect("UploadRunInput", err)
	}
	buffer := make([]byte, chunkBytes)
	var offset uint64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := file.Content.Read(buffer)
		if uint64(n) > file.Size-offset {
			return "", invalidArgument("Send", "file content exceeds declared size")
		}
		if n > 0 {
			if err := stream.Send(&agentcomposev2.UploadRunInputRequest{Frame: &agentcomposev2.UploadRunInputRequest_Chunk{Chunk: buffer[:n]}}); err != nil {
				break
			}
			offset += uint64(n)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
		if n == 0 {
			return "", invalidArgument("Send", "file %q reader returned no data", file.Name)
		}
	}
	// A short read is not judged here: CloseRequest makes the daemon verify the
	// declared size, and a successful answer is the only proof of a complete
	// object.
	response, err := stream.CloseAndReceive()
	closed = true
	if err != nil {
		return "", fromConnect("UploadRunInput", err)
	}
	if response.Msg.GetUploadId() == "" {
		return "", invalidArgument("Send", "upload returned no id")
	}
	return response.Msg.GetUploadId(), nil
}
