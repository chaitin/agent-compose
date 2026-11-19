package chat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sync/atomic"
	"testing"
	"time"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

type inputReadCloser struct {
	*bytes.Reader
	closed atomic.Bool
}

func (r *inputReadCloser) Close() error { r.closed.Store(true); return nil }

var _ io.ReadCloser = (*inputReadCloser)(nil)

func uploadLimits() *agentcomposev2.EngineInputLimits {
	return &agentcomposev2.EngineInputLimits{MaxFiles: 10, MaxUploadFileBytes: 100 << 20, MaxUploadTurnBytes: 200 << 20, MaxUploadChunkBytes: 256 << 10}
}

// TestFileInputStagesUploadAndReferences verifies that SendWithInput stages the
// bytes through UploadRunInput first and then starts the Run referencing the
// returned upload id, in order, without sending bytes inline.
func TestFileInputStagesUploadAndReferences(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.inputLimits = uploadLimits()
	content := bytes.Repeat([]byte("f"), 600<<10)
	sum := sha256.Sum256(content)
	reader := &inputReadCloser{Reader: bytes.NewReader(content)}
	uploadInfo := make(chan *agentcomposev2.RunInputUploadInfo, 1)
	uploadData := make(chan []byte, 1)
	daemon.upload = func(info *agentcomposev2.RunInputUploadInfo, data []byte) (*agentcomposev2.UploadRunInputResponse, error) {
		uploadInfo <- info
		uploadData <- append([]byte(nil), data...)
		return &agentcomposev2.UploadRunInputResponse{UploadId: "upload-1"}, nil
	}
	observed := make(chan *agentcomposev2.RunAgentRequest, 1)
	daemon.attach = func(stream *fakeStream) {
		start, ok := stream.recv()
		if !ok {
			return
		}
		observed <- start.GetStart().GetRequest()
		stream.send(started("run", "sandbox"))
		stream.send(turnCompleted(""))
		<-stream.hold
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conversation := daemon.client(t).Agent("project", "worker").Start()
	defer func() { _ = conversation.Close(ctx) }()
	reply, err := conversation.SendWithInput(ctx, SendInput{Text: "read", ClientRequestID: "durable-request", Files: []InputFile{{ID: "attachment", Name: "a.txt", Size: uint64(len(content)), SHA256: sum[:], Content: reader}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reply.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	info := <-uploadInfo
	if info.GetProjectId() != "project" || info.GetSizeBytes() != uint64(len(content)) || info.GetSha256() != hex.EncodeToString(sum[:]) {
		t.Fatalf("upload info = %+v", info)
	}
	if !bytes.Equal(<-uploadData, content) {
		t.Fatal("uploaded bytes changed")
	}
	request := <-observed
	if request.GetClientRequestId() != "durable-request" {
		t.Fatalf("request id = %q", request.GetClientRequestId())
	}
	if len(request.GetInput()) != 1 {
		t.Fatalf("input parts = %d, want 1", len(request.GetInput()))
	}
	file := request.GetInput()[0].GetFile()
	if file == nil || file.GetFilename() != "a.txt" || file.GetUpload().GetUploadId() != "upload-1" {
		t.Fatalf("input file = %+v", file)
	}
	if !reader.closed.Load() {
		t.Fatal("input reader not closed")
	}
	if uploadedCount := daemon.count("UploadRunInput"); uploadedCount != 1 {
		t.Fatalf("UploadRunInput calls = %d, want 1", uploadedCount)
	}
}

// TestFileInputOldDaemonIsRejectedBeforeStart verifies that a daemon without
// the upload source is rejected before any Run is started or bytes uploaded.
func TestFileInputOldDaemonIsRejectedBeforeStart(t *testing.T) {
	daemon := newFakeDaemon(t)
	reader := &inputReadCloser{Reader: bytes.NewReader(nil)}
	sum := sha256.Sum256(nil)
	_, err := daemon.client(t).Agent("p", "a").Start().SendWithInput(context.Background(), SendInput{Text: "read", ClientRequestID: "id", Files: []InputFile{{ID: "file", Name: "a", Size: 1, SHA256: sum[:], Content: reader}}})
	if err == nil || daemon.count("AttachAgentRun") != 0 || daemon.count("UploadRunInput") != 0 || !reader.closed.Load() {
		t.Fatalf("err=%v attach=%d upload=%d closed=%v", err, daemon.count("AttachAgentRun"), daemon.count("UploadRunInput"), reader.closed.Load())
	}
}

// TestFileInputCancellationClosesBlockedReader verifies that canceling the
// request mid-upload closes a reader blocked in Read instead of leaking it.
func TestFileInputCancellationClosesBlockedReader(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.inputLimits = uploadLimits()
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	daemon.upload = func(_ *agentcomposev2.RunInputUploadInfo, _ []byte) (*agentcomposev2.UploadRunInputResponse, error) {
		return &agentcomposev2.UploadRunInputResponse{UploadId: "upload-1"}, nil
	}
	sum := sha256.Sum256([]byte("a"))
	ctx, cancel := context.WithCancel(context.Background())
	conversation := daemon.client(t).Agent("p", "a").Start()
	errs := make(chan error, 1)
	go func() {
		_, err := conversation.SendWithInput(ctx, SendInput{Text: "read", ClientRequestID: "cancel", Files: []InputFile{{ID: "file", Name: "a", Size: 1, SHA256: sum[:], Content: reader}}})
		errs <- err
	}()
	// Give the upload a chance to block on the empty pipe.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("cancellation succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SendWithInput did not return after cancellation")
	}
	if _, err := reader.Read(make([]byte, 1)); err == nil {
		t.Fatal("canceled reader remained open")
	}
}
