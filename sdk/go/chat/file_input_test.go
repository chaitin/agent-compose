package chat

import (
	"bytes"
	"context"
	"crypto/sha256"
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

func TestFileInputConcurrentReceiveAndStableIdentity(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.inputCapabilities = &agentcomposev2.RunFileInputCapabilities{ProtocolVersion: 1, AttachAgentRun: true, MaxFiles: 10, MaxFileBytes: 100 << 20, MaxTotalBytes: 200 << 20, MaxChunkBytes: 256 << 10}
	content := bytes.Repeat([]byte("f"), 600<<10)
	sum := sha256.Sum256(content)
	reader := &inputReadCloser{Reader: bytes.NewReader(content)}
	observed := make(chan *agentcomposev2.RunAgentRequest, 1)
	received := make(chan []byte, 1)
	daemon.attach = func(stream *fakeStream) {
		start, ok := stream.recv()
		if !ok {
			return
		}
		observed <- start.GetStart().GetRequest()
		// Sending before consuming the complete upload exercises bidi receive.
		stream.send(started("run", "sandbox"))
		var data []byte
		for {
			frame, ok := stream.recv()
			if !ok {
				return
			}
			if chunk := frame.GetFileChunk(); chunk != nil {
				if chunk.GetOffset() != uint64(len(data)) {
					return
				}
				data = append(data, chunk.GetData()...)
			} else if frame.GetInputComplete() != nil {
				break
			} else {
				return
			}
		}
		received <- data
		stream.send(&agentcomposev2.AttachAgentRunResponse{Frame: &agentcomposev2.AttachAgentRunResponse_InputReady{InputReady: &agentcomposev2.RunInputPreparation{RunId: "run", SandboxId: "sandbox", State: agentcomposev2.RunInputState_RUN_INPUT_STATE_READY, Files: []*agentcomposev2.RunInputFileResult{{File: &agentcomposev2.RunInputFile{FileId: "attachment"}, RelativePath: "a (1).txt"}}}}})
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
	request := <-observed
	if request.GetClientRequestId() != "durable-request" || len(request.GetInputFiles()) != 1 {
		t.Fatalf("request=%v", request)
	}
	if !bytes.Equal(<-received, content) {
		t.Fatal("file bytes changed")
	}
	if !reader.closed.Load() {
		t.Fatal("input reader not closed")
	}
	if input := reply.Input(); input == nil || input.State != "ready" || input.Paths["attachment"] != "a (1).txt" {
		t.Fatalf("input=%+v", input)
	}
}

func TestFileInputOldDaemonIsRejectedBeforeStart(t *testing.T) {
	daemon := newFakeDaemon(t)
	reader := &inputReadCloser{Reader: bytes.NewReader(nil)}
	sum := sha256.Sum256(nil)
	_, err := daemon.client(t).Agent("p", "a").Start().SendWithInput(context.Background(), SendInput{Text: "read", ClientRequestID: "id", Files: []InputFile{{ID: "file", Name: "a", SHA256: sum[:], Content: reader}}})
	if err == nil || daemon.count("AttachAgentRun") != 0 || !reader.closed.Load() {
		t.Fatalf("err=%v starts=%d closed=%v", err, daemon.count("AttachAgentRun"), reader.closed.Load())
	}
}

func TestFileInputLookupUsesOriginalScope(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.getRun = func(request *agentcomposev2.GetRunRequest) (*agentcomposev2.GetRunResponse, error) {
		if request.GetRunId() != "" || request.GetProjectId() != "p" || request.GetAgentName() != "a" || request.GetClientRequestId() != "original" || request.GetSource() != agentcomposev2.RunSource_RUN_SOURCE_MANUAL {
			t.Error("request identity changed")
		}
		return &agentcomposev2.GetRunResponse{Run: &agentcomposev2.RunDetail{InputPreparation: &agentcomposev2.RunInputPreparation{State: agentcomposev2.RunInputState_RUN_INPUT_STATE_UNKNOWN}}}, nil
	}
	input, err := daemon.client(t).Agent("p", "a").LookupInput(context.Background(), "original")
	if err != nil || input.State != "unknown" {
		t.Fatalf("input=%+v err=%v", input, err)
	}
	if daemon.count("AttachAgentRun") != 0 {
		t.Fatal("lookup restarted execution")
	}
}

var _ io.ReadCloser = (*inputReadCloser)(nil)

func TestFileInputCancellationClosesBlockedReader(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.inputCapabilities = &agentcomposev2.RunFileInputCapabilities{ProtocolVersion: 1, AttachAgentRun: true, MaxFiles: 10, MaxFileBytes: 100 << 20, MaxTotalBytes: 200 << 20, MaxChunkBytes: 256 << 10}
	started := make(chan struct{})
	daemon.attach = func(stream *fakeStream) {
		if _, ok := stream.recv(); !ok {
			return
		}
		close(started)
		<-stream.hold
	}
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	sum := sha256.Sum256([]byte("a"))
	ctx, cancel := context.WithCancel(context.Background())
	conversation := daemon.client(t).Agent("p", "a").Start()
	reply, err := conversation.SendWithInput(ctx, SendInput{Text: "read", ClientRequestID: "cancel", Files: []InputFile{{ID: "file", Name: "a", Size: 1, SHA256: sum[:], Content: reader}}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	waitCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if _, err := reply.Wait(waitCtx); err == nil {
		t.Fatal("cancellation succeeded")
	}
	if _, err := reader.Read(make([]byte, 1)); err == nil {
		t.Fatal("canceled reader remained open")
	}
}
