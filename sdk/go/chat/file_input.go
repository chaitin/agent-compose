package chat

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"strings"
	"sync"

	"connectrpc.com/connect"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

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
// it for reconciliation; this method never appends files to a live Run.
type SendInput struct {
	Text            string
	ClientRequestID string
	Files           []InputFile
}

// InputPreparation is delivery evidence, separate from the agent's reply.
// State is receiving, publishing, ready, failed, or unknown.
type InputPreparation struct {
	RunID          string
	SandboxID      string
	State          string
	ManifestSHA256 []byte
	Paths          map[string]string
	FailureReason  int32
}

// SendWithInput transfers files concurrently with receiving server frames. It
// starts a Run only; it does not stop or replace an existing Run automatically.
// After an uncertain outcome, use Agent.LookupInput with the original identity
// instead of resending bytes. No automatic retry or upload resume is performed.
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
	declarations, err := c.validateInput(ctx, input)
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
	c.openingRequestID, c.openingFiles = input.ClientRequestID, declarations
	err = c.attach(ctx, "SendWithInput", input.Text)
	c.openingRequestID, c.openingFiles = "", nil
	if err != nil {
		c.mu.Unlock()
		return nil, errors.Join(err, closeFiles())
	}
	reply := newReply(c)
	c.current = reply
	stream := c.stream
	uploadCtx, cancelUpload := context.WithCancel(ctx)
	cancelStream := c.cancel
	c.cancel = func() { cancelUpload(); cancelStream() }
	c.mu.Unlock()
	go func() {
		stop := context.AfterFunc(uploadCtx, func() {
			c.endSession(stream, uploadCtx.Err())
			_ = closeFiles()
		})
		err := uploadInput(uploadCtx, stream, input.Files)
		stop()
		cancelUpload()
		err = errors.Join(err, closeFiles())
		if err != nil {
			c.endSession(stream, err)
		}
	}()
	return reply, nil
}

func (c *Conversation) validateInput(ctx context.Context, input SendInput) ([]*agentcomposev2.RunInputFile, error) {
	if strings.TrimSpace(input.Text) == "" || strings.TrimSpace(input.ClientRequestID) == "" {
		return nil, invalidArgument("SendWithInput", "text and stable client request ID are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(input.Files) == 0 {
		return nil, nil
	}
	response, err := c.agent.client.transport.engine.GetCapabilities(ctx, connect.NewRequest(&agentcomposev2.GetEngineCapabilitiesRequest{}))
	if err != nil {
		return nil, fromConnect("FileInputCapabilities", err)
	}
	capability := response.Msg.GetRunFileInput()
	if capability.GetProtocolVersion() != 1 || !capability.GetAttachAgentRun() {
		return nil, invalidArgument("SendWithInput", "daemon does not support streamed file input")
	}
	if uint32(len(input.Files)) > capability.GetMaxFiles() || capability.GetMaxChunkBytes() < 256<<10 {
		return nil, invalidArgument("SendWithInput", "file input exceeds daemon limits")
	}
	var total uint64
	seen := make(map[string]bool)
	result := make([]*agentcomposev2.RunInputFile, 0, len(input.Files))
	for _, file := range input.Files {
		if file.Content == nil || file.ID == "" || seen[file.ID] || file.Name == "" || strings.ContainsAny(file.Name, "/\\\x00") || len(file.SHA256) != sha256.Size || file.Size > capability.GetMaxFileBytes() {
			return nil, invalidArgument("SendWithInput", "invalid file declaration")
		}
		seen[file.ID] = true
		if file.Size > capability.GetMaxTotalBytes()-total {
			return nil, invalidArgument("SendWithInput", "file total exceeds daemon limit")
		}
		total += file.Size
		result = append(result, &agentcomposev2.RunInputFile{FileId: file.ID, DisplayName: file.Name, SizeBytes: file.Size, Sha256: append([]byte(nil), file.SHA256...)})
	}
	return result, nil
}

func uploadInput(ctx context.Context, stream *attachStream, files []InputFile) error {
	if len(files) == 0 {
		return nil
	}
	buffer := make([]byte, 256<<10)
	for _, file := range files {
		var offset uint64
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, readErr := file.Content.Read(buffer)
			if uint64(n) > file.Size-offset {
				return invalidArgument("SendWithInput", "file content exceeds declared size")
			}
			if n > 0 {
				err := stream.Send(&agentcomposev2.AttachAgentRunRequest{Frame: &agentcomposev2.AttachAgentRunRequest_FileChunk{FileChunk: &agentcomposev2.RunInputFileChunk{FileId: file.ID, Offset: offset, Data: buffer[:n]}}})
				if err != nil {
					return fromConnect("FileInput", err)
				}
				offset += uint64(n)
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				return readErr
			}
			if n == 0 {
				return io.ErrNoProgress
			}
		}
		if offset != file.Size {
			return io.ErrUnexpectedEOF
		}
	}
	return fromConnect("FileInput", stream.Send(&agentcomposev2.AttachAgentRunRequest{Frame: &agentcomposev2.AttachAgentRunRequest_InputComplete{InputComplete: &agentcomposev2.RunInputComplete{}}}))
}

// Input reports the latest server delivery evidence independently of Wait.
func (r *Reply) Input() *InputPreparation {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.input == nil {
		return nil
	}
	copy := *r.input
	copy.ManifestSHA256 = append([]byte(nil), copy.ManifestSHA256...)
	copy.Paths = make(map[string]string, len(r.input.Paths))
	for id, path := range r.input.Paths {
		copy.Paths[id] = path
	}
	return &copy
}

func inputPreparationFromProto(input *agentcomposev2.RunInputPreparation) *InputPreparation {
	if input == nil {
		return nil
	}
	state := "unknown"
	switch input.GetState() {
	case agentcomposev2.RunInputState_RUN_INPUT_STATE_RECEIVING:
		state = "receiving"
	case agentcomposev2.RunInputState_RUN_INPUT_STATE_PUBLISHING:
		state = "publishing"
	case agentcomposev2.RunInputState_RUN_INPUT_STATE_READY:
		state = "ready"
	case agentcomposev2.RunInputState_RUN_INPUT_STATE_FAILED:
		state = "failed"
	}
	result := &InputPreparation{RunID: input.GetRunId(), SandboxID: input.GetSandboxId(), State: state, ManifestSHA256: append([]byte(nil), input.GetManifestSha256()...), Paths: make(map[string]string), FailureReason: int32(input.GetFailure().GetReason())}
	if state == "ready" {
		for _, file := range input.GetFiles() {
			result.Paths[file.GetFile().GetFileId()] = file.GetRelativePath()
		}
	}
	return result
}

// LookupInput reconciles by the original request identity, without resending
// file bytes or depending on an observed started frame. Chat runs use MANUAL.
func (a *Agent) LookupInput(ctx context.Context, clientRequestID string) (*InputPreparation, error) {
	response, err := a.client.transport.runs.GetRun(ctx, connect.NewRequest(&agentcomposev2.GetRunRequest{ProjectId: a.projectID, AgentName: a.name, Source: agentcomposev2.RunSource_RUN_SOURCE_MANUAL, ClientRequestId: clientRequestID}))
	if err != nil {
		return nil, fromConnect("LookupInput", err)
	}
	return inputPreparationFromProto(response.Msg.GetRun().GetInputPreparation()), nil
}
