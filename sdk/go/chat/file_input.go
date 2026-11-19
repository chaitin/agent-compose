package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"

	"connectrpc.com/connect"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// uploadChunkBytes is the fixed upload chunk size. It must never exceed the
// daemon's advertised max_upload_chunk_bytes; prepareInput rejects a daemon
// that advertises less, so the buffer and the server limit can never drift
// apart.
const uploadChunkBytes = 256 << 10

// InputFile supplies one immutable file. The SDK closes Content on every exit.
// Content must support concurrent Close to interrupt a blocked Read.
type InputFile struct {
	ID      string
	Name    string
	Size    uint64
	SHA256  []byte
	Content io.ReadCloser
}

// SendInput starts a new Run. Persist ClientRequestID before calling and reuse
// it for idempotency; this method never appends files to a live Run.
type SendInput struct {
	Text            string
	ClientRequestID string
	Files           []InputFile
}

// SendWithInput stages every file through UploadRunInput and then starts the
// Run referencing the staged objects. Staging completes before the Run exists,
// so a failed upload never leaves a half-created Run behind and the caller can
// retry with the same ClientRequestID. No automatic retry or resume is
// performed.
func (c *Conversation) SendWithInput(ctx context.Context, input SendInput) (*Reply, error) {
	var once sync.Once
	var closeErr error
	closeFiles := func() error {
		once.Do(func() {
			for _, file := range input.Files {
				if file.Content != nil {
					closeErr = errors.Join(closeErr, file.Content.Close())
				}
			}
		})
		return closeErr
	}
	// A blocked Content.Read is interrupted only by Close, so tie the readers'
	// lifetime to the request context: canceling mid-upload closes them and
	// unblocks the read.
	stopClose := context.AfterFunc(ctx, func() { _ = closeFiles() })
	defer stopClose()
	parts, err := c.prepareInput(ctx, input)
	if err != nil {
		return nil, errors.Join(err, closeFiles())
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.Join(ErrClosed, closeFiles())
	}
	if c.stream != nil || c.runID != "" || (c.current != nil && !c.current.Done()) {
		c.mu.Unlock()
		return nil, errors.Join(ErrBusy, closeFiles())
	}
	c.openingRequestID, c.openingInput = input.ClientRequestID, parts
	err = c.attach(ctx, "SendWithInput", input.Text)
	c.openingRequestID, c.openingInput = "", nil
	if err != nil {
		c.mu.Unlock()
		return nil, errors.Join(err, closeFiles())
	}
	reply := newReply(c)
	c.current = reply
	c.mu.Unlock()
	_ = closeFiles()
	return reply, nil
}

// prepareInput validates the request, checks the daemon's upload capability and
// stages every file, returning the ordered AgentInputPart list the Run
// references. A missing capability never permits a file turn to fall back to a
// text-only request.
func (c *Conversation) prepareInput(ctx context.Context, input SendInput) ([]*agentcomposev2.AgentInputPart, error) {
	if strings.TrimSpace(input.Text) == "" || strings.TrimSpace(input.ClientRequestID) == "" {
		return nil, invalidArgument("SendWithInput", "text and stable client request ID are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(input.Files) == 0 {
		return nil, nil
	}
	limits, err := c.uploadLimits(ctx)
	if err != nil {
		return nil, err
	}
	if uint32(len(input.Files)) > limits.GetMaxFiles() || limits.GetMaxUploadChunkBytes() < uploadChunkBytes {
		return nil, invalidArgument("SendWithInput", "file input exceeds daemon limits")
	}
	seen := make(map[string]bool)
	var total uint64
	parts := make([]*agentcomposev2.AgentInputPart, 0, len(input.Files))
	for _, file := range input.Files {
		if file.Content == nil || file.ID == "" || seen[file.ID] || file.Name == "" || strings.ContainsAny(file.Name, "/\\\x00") || len(file.SHA256) != sha256.Size || file.Size == 0 || file.Size > limits.GetMaxUploadFileBytes() {
			return nil, invalidArgument("SendWithInput", "invalid file declaration")
		}
		seen[file.ID] = true
		if file.Size > limits.GetMaxUploadTurnBytes()-total {
			return nil, invalidArgument("SendWithInput", "file total exceeds daemon limit")
		}
		total += file.Size
		uploadID, err := c.uploadFile(ctx, file)
		if err != nil {
			return nil, err
		}
		parts = append(parts, &agentcomposev2.AgentInputPart{Part: &agentcomposev2.AgentInputPart_File{File: &agentcomposev2.AgentInputFile{
			Filename: file.Name,
			Source:   &agentcomposev2.AgentInputFile_Upload{Upload: &agentcomposev2.AgentInputFileUpload{UploadId: uploadID}},
		}}})
	}
	return parts, nil
}

// uploadLimits reads the daemon's EngineInputLimits and rejects a daemon that
// does not offer the upload source.
func (c *Conversation) uploadLimits(ctx context.Context) (*agentcomposev2.EngineInputLimits, error) {
	response, err := c.agent.client.transport.engine.GetCapabilities(ctx, connect.NewRequest(&agentcomposev2.GetEngineCapabilitiesRequest{}))
	if err != nil {
		return nil, fromConnect("FileInputCapabilities", err)
	}
	limits := response.Msg.GetInputLimits()
	if limits.GetMaxUploadFileBytes() == 0 {
		return nil, invalidArgument("SendWithInput", "daemon does not support staged file input")
	}
	return limits, nil
}

// uploadFile streams one file into the staging area and returns the opaque
// upload id to reference. The server answers immediately when the same content
// already exists in the project; a Send that then fails against the closed
// stream is expected, and CloseAndReceive returns the deduplicated object.
func (c *Conversation) uploadFile(ctx context.Context, file InputFile) (string, error) {
	stream := c.agent.client.transport.runs.UploadRunInput(ctx)
	if err := stream.Send(&agentcomposev2.UploadRunInputRequest{Frame: &agentcomposev2.UploadRunInputRequest_Info{Info: &agentcomposev2.RunInputUploadInfo{
		ProjectId: c.agent.projectID,
		SizeBytes: file.Size,
		Sha256:    hex.EncodeToString(file.SHA256),
	}}}); err != nil {
		return "", fromConnect("UploadRunInput", err)
	}
	buffer := make([]byte, uploadChunkBytes)
	var offset uint64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := file.Content.Read(buffer)
		if uint64(n) > file.Size-offset {
			return "", invalidArgument("SendWithInput", "file content exceeds declared size")
		}
		if n > 0 {
			// A deduplicated upload answers before the client stops sending, so
			// a Send failure here is not an upload failure; CloseAndReceive
			// below reports the authoritative result.
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
			return "", io.ErrNoProgress
		}
	}
	if offset != file.Size {
		return "", io.ErrUnexpectedEOF
	}
	response, err := stream.CloseAndReceive()
	if err != nil {
		return "", fromConnect("UploadRunInput", err)
	}
	if response.Msg.GetUploadId() == "" {
		return "", invalidArgument("SendWithInput", "upload returned no id")
	}
	return response.Msg.GetUploadId(), nil
}
