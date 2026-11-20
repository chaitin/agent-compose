package chat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// uploadLimits is a daemon that takes both routes: files up to 256 KiB travel
// inline, larger ones are staged through UploadRunInput.
func uploadLimits() *agentcomposev2.EngineInputLimits {
	return &agentcomposev2.EngineInputLimits{
		MaxFiles:            10,
		MaxFileBytes:        256 << 10,
		MaxTurnBytes:        32 << 20,
		MaxUploadFileBytes:  100 << 20,
		MaxUploadTurnBytes:  200 << 20,
		MaxUploadChunkBytes: 256 << 10,
	}
}

// observeStart captures the start request of the run the conversation opens and
// lets it complete one turn.
func observeStart(daemon *fakeDaemon) <-chan *agentcomposev2.RunAgentRequest {
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
	return observed
}

func inputFile(id, name string, content []byte) (InputFile, *inputReadCloser) {
	sum := sha256.Sum256(content)
	reader := &inputReadCloser{Reader: bytes.NewReader(content)}
	return InputFile{ID: id, Name: name, Size: uint64(len(content)), SHA256: sum[:], Content: reader}, reader
}

// TestPlanInputFilesRoutesByBudget verifies that files stay in their given order
// while the inline budget is spent first and only the overflow is uploaded.
func TestPlanInputFilesRoutesByBudget(t *testing.T) {
	sum := sha256.Sum256([]byte("x"))
	file := func(id string, size uint64) InputFile {
		return InputFile{ID: id, Name: id + ".bin", Size: size, SHA256: sum[:], Content: io.NopCloser(bytes.NewReader(nil))}
	}
	limits := &agentcomposev2.EngineInputLimits{
		MaxFiles:            10,
		MaxFileBytes:        1 << 20,
		MaxTurnBytes:        2 << 20,
		MaxUploadFileBytes:  10 << 20,
		MaxUploadTurnBytes:  20 << 20,
		MaxUploadChunkBytes: 256 << 10,
	}
	placements, err := planInputFiles([]InputFile{file("a", 1<<20), file("b", 1<<20), file("c", 1<<20)}, limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(placements) != 3 || !placements[0].inline || !placements[1].inline || placements[2].inline {
		t.Fatalf("placements = %+v, want inline, inline, upload", placements)
	}
}

// TestPlanInputFilesRejectsWhenNoRouteFits verifies that a file overrunning
// both budgets is an error instead of a dropped part.
func TestPlanInputFilesRejectsWhenNoRouteFits(t *testing.T) {
	sum := sha256.Sum256([]byte("x"))
	file := func(id string, size uint64) InputFile {
		return InputFile{ID: id, Name: id + ".bin", Size: size, SHA256: sum[:], Content: io.NopCloser(bytes.NewReader(nil))}
	}
	// Inline only, with room for one file.
	limits := &agentcomposev2.EngineInputLimits{MaxFiles: 10, MaxFileBytes: 1 << 20, MaxTurnBytes: 1 << 20}
	if _, err := planInputFiles([]InputFile{file("a", 1<<20), file("b", 1<<10)}, limits); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
}

// TestFileInputLargeFileIsStagedThroughUpload verifies that a file above the
// inline limits is uploaded first and the Run references the returned upload id
// in its place.
func TestFileInputLargeFileIsStagedThroughUpload(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.inputLimits = uploadLimits()
	content := bytes.Repeat([]byte("f"), 600<<10)
	file, reader := inputFile("attachment", "a.txt", content)
	uploadInfo := make(chan *agentcomposev2.RunInputUploadInfo, 1)
	uploadData := make(chan []byte, 1)
	daemon.upload = func(info *agentcomposev2.RunInputUploadInfo, data []byte) (*agentcomposev2.UploadRunInputResponse, error) {
		uploadInfo <- info
		uploadData <- append([]byte(nil), data...)
		return &agentcomposev2.UploadRunInputResponse{UploadId: "upload-1"}, nil
	}
	observed := observeStart(daemon)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conversation := daemon.client(t).Agent("project", "worker").Start()
	defer func() { _ = conversation.Close(ctx) }()
	reply, err := conversation.Send(ctx, "read", WithFiles(file), WithRequestID("durable-request"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reply.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	info := <-uploadInfo
	if info.GetProjectId() != "project" || info.GetSizeBytes() != uint64(len(content)) || info.GetSha256() != hex.EncodeToString(file.SHA256) {
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
	input := request.GetInput()[0].GetFile()
	if input == nil || input.GetFilename() != "a.txt" || input.GetUpload().GetUploadId() != "upload-1" || input.GetBytes() != nil {
		t.Fatalf("input file = %+v", input)
	}
	if !reader.closed.Load() {
		t.Fatal("input reader not closed")
	}
	if uploadedCount := daemon.count("UploadRunInput"); uploadedCount != 1 {
		t.Fatalf("UploadRunInput calls = %d, want 1", uploadedCount)
	}
}

// TestFileInputSmallFileTravelsInline verifies that a file within the inline
// limits is sent with the start request instead of being uploaded. It drives
// the primary Send API, options included.
func TestFileInputSmallFileTravelsInline(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.inputLimits = uploadLimits()
	content := []byte("inline bytes")
	file, reader := inputFile("attachment", "a.txt", content)
	observed := observeStart(daemon)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conversation := daemon.client(t).Agent("project", "worker").Start()
	defer func() { _ = conversation.Close(ctx) }()
	reply, err := conversation.Send(ctx, "read", WithFiles(file), WithRequestID("inline"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reply.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	request := <-observed
	if got := request.GetClientRequestId(); got != "inline" {
		t.Fatalf("client request id = %q", got)
	}
	input := request.GetInput()[0].GetFile()
	if input.GetFilename() != "a.txt" || input.GetUpload() != nil {
		t.Fatalf("input file = %+v", input)
	}
	if data := input.GetBytes(); !bytes.Equal(data.GetData(), content) || data.GetSha256() != hex.EncodeToString(file.SHA256) {
		t.Fatalf("inline bytes = %+v", data)
	}
	if !reader.closed.Load() {
		t.Fatal("input reader not closed")
	}
	if uploadedCount := daemon.count("UploadRunInput"); uploadedCount != 0 {
		t.Fatalf("UploadRunInput calls = %d, want 0", uploadedCount)
	}
}

// TestFileInputMixedRoutesKeepOrder verifies that each file picks its own route
// and the ordered parts still match the caller's order.
func TestFileInputMixedRoutesKeepOrder(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.inputLimits = uploadLimits()
	smallContent := []byte("small")
	largeContent := bytes.Repeat([]byte("l"), 600<<10)
	small, _ := inputFile("small", "small.txt", smallContent)
	large, _ := inputFile("large", "large.bin", largeContent)
	daemon.upload = func(*agentcomposev2.RunInputUploadInfo, []byte) (*agentcomposev2.UploadRunInputResponse, error) {
		return &agentcomposev2.UploadRunInputResponse{UploadId: "upload-large"}, nil
	}
	observed := observeStart(daemon)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conversation := daemon.client(t).Agent("project", "worker").Start()
	defer func() { _ = conversation.Close(ctx) }()
	reply, err := conversation.Send(ctx, "read", WithFiles(small, large), WithRequestID("mixed"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reply.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	parts := (<-observed).GetInput()
	if len(parts) != 2 {
		t.Fatalf("input parts = %d, want 2", len(parts))
	}
	if first := parts[0].GetFile(); !bytes.Equal(first.GetBytes().GetData(), smallContent) {
		t.Fatalf("first part = %+v, want inline small file", first)
	}
	if second := parts[1].GetFile(); second.GetUpload().GetUploadId() != "upload-large" {
		t.Fatalf("second part = %+v, want the uploaded large file", second)
	}
	if uploadedCount := daemon.count("UploadRunInput"); uploadedCount != 1 {
		t.Fatalf("UploadRunInput calls = %d, want 1", uploadedCount)
	}
}

// TestFileInputWithoutUploadStaysInline verifies that a daemon without the
// upload source, such as one running on K8s, still accepts files inline.
func TestFileInputWithoutUploadStaysInline(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.inputLimits = &agentcomposev2.EngineInputLimits{MaxFiles: 10, MaxFileBytes: 1 << 20, MaxTurnBytes: 32 << 20}
	content := []byte("no upload here")
	file, reader := inputFile("attachment", "a.txt", content)
	observed := observeStart(daemon)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conversation := daemon.client(t).Agent("project", "worker").Start()
	defer func() { _ = conversation.Close(ctx) }()
	reply, err := conversation.Send(ctx, "read", WithFiles(file), WithRequestID("inline-only"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reply.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if data := (<-observed).GetInput()[0].GetFile().GetBytes(); !bytes.Equal(data.GetData(), content) {
		t.Fatalf("inline bytes = %q", data.GetData())
	}
	if !reader.closed.Load() {
		t.Fatal("input reader not closed")
	}
	if uploadedCount := daemon.count("UploadRunInput"); uploadedCount != 0 {
		t.Fatalf("UploadRunInput calls = %d, want 0", uploadedCount)
	}
}

// TestFileInputTooLargeForInlineWithoutUploadIsRejected verifies that a file
// no route can carry fails before the Run starts.
func TestFileInputTooLargeForInlineWithoutUploadIsRejected(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.inputLimits = &agentcomposev2.EngineInputLimits{MaxFiles: 10, MaxFileBytes: 1 << 10, MaxTurnBytes: 32 << 20}
	file, reader := inputFile("attachment", "a.bin", bytes.Repeat([]byte("x"), 2<<10))
	_, err := daemon.client(t).Agent("project", "worker").Start().Send(context.Background(), "read", WithFiles(file), WithRequestID("too-large"))
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("err = %v, want ErrInvalidArgument", err)
	}
	if daemon.count("AttachAgentRun") != 0 || daemon.count("UploadRunInput") != 0 || !reader.closed.Load() {
		t.Fatalf("attach=%d upload=%d closed=%v", daemon.count("AttachAgentRun"), daemon.count("UploadRunInput"), reader.closed.Load())
	}
}

// TestFileInputClosedOnEarlyRejection verifies that Send closes the readers
// even when it rejects the turn before doing any work: WithFiles promises the
// SDK owns Content from the moment it is passed.
func TestFileInputClosedOnEarlyRejection(t *testing.T) {
	daemon := newFakeDaemon(t)
	conversation := daemon.client(t).Agent("project", "worker").Start()

	file, reader := inputFile("file", "a.txt", []byte("x"))
	if _, err := conversation.Send(context.Background(), "  ", WithFiles(file)); err == nil {
		t.Fatal("empty text was accepted")
	}
	if !reader.closed.Load() {
		t.Fatal("reader not closed after an empty-text rejection")
	}

	file, reader = inputFile("file", "a.txt", []byte("x"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := conversation.Send(ctx, "hello", WithFiles(file)); err == nil {
		t.Fatal("canceled context was accepted")
	}
	if !reader.closed.Load() {
		t.Fatal("reader not closed after a canceled-context rejection")
	}

	if calls := daemon.count("AttachAgentRun") + daemon.count("GetCapabilities"); calls != 0 {
		t.Fatalf("rejected turns made %d daemon calls, want none", calls)
	}
}

// TestFileInputTravelsOnALaterTurn verifies that a turn after the opening one
// carries its files on the human message. Input is a turn property: a
// conversation that keeps one run across turns must still accept files on any
// of them.
func TestFileInputTravelsOnALaterTurn(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.inputLimits = uploadLimits()
	content := []byte("late file")
	file, reader := inputFile("file", "a.txt", content)
	messages := make(chan *agentcomposev2.AttachAgentRunRequest, 1)
	daemon.attach = func(stream *fakeStream) {
		if _, ok := stream.recv(); !ok {
			return
		}
		stream.send(started("run", "sandbox"))
		stream.send(turnCompleted(""))
		message, ok := stream.recv()
		if !ok {
			return
		}
		messages <- message
		stream.send(turnCompleted(""))
		<-stream.hold
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conversation := daemon.client(t).Agent("project", "worker").Start()
	defer func() { _ = conversation.Close(ctx) }()
	reply, err := conversation.Send(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reply.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	reply, err = conversation.Send(ctx, "again", WithFiles(file))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reply.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	frame := <-messages
	if frame.GetClientFrameId() == "" {
		t.Fatal("human message carries no frame id")
	}
	human := frame.GetHumanMessage()
	if human.GetText() != "again" {
		t.Fatalf("human message = %q", human.GetText())
	}
	if data := human.GetInput()[0].GetFile().GetBytes(); !bytes.Equal(data.GetData(), content) {
		t.Fatalf("inline bytes = %q, want the later turn's file", data.GetData())
	}
	if !reader.closed.Load() {
		t.Fatal("input reader not closed")
	}
	if uploaded := daemon.count("UploadRunInput"); uploaded != 0 {
		t.Fatalf("UploadRunInput calls = %d, want 0", uploaded)
	}
	if attaches := daemon.count("AttachAgentRun"); attaches != 1 {
		t.Fatalf("AttachAgentRun calls = %d, want 1", attaches)
	}
}

// TestFileInputResumesTheSandboxOfTheLastRun covers the per-turn pattern a
// caller uses when it ends the run after every reply: Open finds the ended
// run's sandbox, and the next file turn starts a new run that asks for it.
func TestFileInputResumesTheSandboxOfTheLastRun(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.inputLimits = uploadLimits()
	daemon.listRuns = listRunsReturning(runSummary("run-done", "RUN_STATUS_SUCCEEDED", "sandbox-a"))
	content := []byte("resume me")
	file, reader := inputFile("attachment", "a.txt", content)
	starts := make(chan *agentcomposev2.AttachAgentRunStart, 1)
	daemon.attach = func(stream *fakeStream) {
		frame, ok := stream.recv()
		if !ok {
			return
		}
		starts <- frame.GetStart()
		stream.send(started("run-2", "sandbox-a"))
		stream.send(turnCompleted(""))
		<-stream.hold
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conversation, err := daemon.client(t).Agent("project-1", "reviewer").Open(ctx, "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conversation.Close(ctx) }()
	reply, err := conversation.Send(ctx, "read", WithFiles(file), WithRequestID("turn-2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reply.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	request := (<-starts).GetRequest()
	if got := request.GetSandboxId(); got != "sandbox-a" {
		t.Fatalf("resumed sandbox = %q, want sandbox-a", got)
	}
	if got := request.GetClientRequestId(); got != "turn-2" {
		t.Fatalf("client request id = %q", got)
	}
	if data := request.GetInput()[0].GetFile().GetBytes(); !bytes.Equal(data.GetData(), content) {
		t.Fatalf("inline bytes = %q", data.GetData())
	}
	if got := conversation.Continuity(); got != Continuous {
		t.Fatalf("Continuity = %q, want %q", got, Continuous)
	}
	if !reader.closed.Load() {
		t.Fatal("input reader not closed")
	}
}

// TestFileInputDeduplicatedUploadStopsSending verifies that a daemon which
// answers before the last chunk, because the content already exists, is not
// mistaken for a truncated upload: the returned upload id is what the Run
// references.
func TestFileInputDeduplicatedUploadStopsSending(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.inputLimits = uploadLimits()
	// Larger than any transport window, so the daemon's answer cannot arrive
	// after every chunk was already accepted.
	content := bytes.Repeat([]byte("d"), 8<<20)
	file, reader := inputFile("file", "a.bin", content)
	uploadInfo := make(chan *agentcomposev2.RunInputUploadInfo, 1)
	daemon.uploadExisting = func(info *agentcomposev2.RunInputUploadInfo) (*agentcomposev2.UploadRunInputResponse, error) {
		uploadInfo <- info
		return &agentcomposev2.UploadRunInputResponse{UploadId: "upload-existing", Deduplicated: true}, nil
	}
	observed := observeStart(daemon)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conversation := daemon.client(t).Agent("project", "worker").Start()
	defer func() { _ = conversation.Close(ctx) }()
	reply, err := conversation.Send(ctx, "read", WithFiles(file), WithRequestID("dedup"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reply.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if info := <-uploadInfo; info.GetSha256() != hex.EncodeToString(file.SHA256) {
		t.Fatalf("sha256 = %q", info.GetSha256())
	}
	input := (<-observed).GetInput()[0].GetFile()
	if input.GetUpload().GetUploadId() != "upload-existing" {
		t.Fatalf("upload id = %q, want upload-existing", input.GetUpload().GetUploadId())
	}
	if !reader.closed.Load() {
		t.Fatal("input reader not closed")
	}
}

// TestFileInputUnsupportedDaemonIsRejectedBeforeStart verifies that a daemon
// with no file input at all is rejected before any Run is started or bytes
// uploaded.
func TestFileInputUnsupportedDaemonIsRejectedBeforeStart(t *testing.T) {
	daemon := newFakeDaemon(t)
	file, reader := inputFile("file", "a.txt", []byte("x"))
	_, err := daemon.client(t).Agent("p", "a").Start().Send(context.Background(), "read", WithFiles(file), WithRequestID("id"))
	if err == nil || daemon.count("AttachAgentRun") != 0 || daemon.count("UploadRunInput") != 0 || !reader.closed.Load() {
		t.Fatalf("err=%v attach=%d upload=%d closed=%v", err, daemon.count("AttachAgentRun"), daemon.count("UploadRunInput"), reader.closed.Load())
	}
}

// TestFileInputCancellationClosesBlockedReader verifies that canceling the
// request mid-upload closes a reader blocked in Read instead of leaking it.
func TestFileInputCancellationClosesBlockedReader(t *testing.T) {
	daemon := newFakeDaemon(t)
	limits := uploadLimits()
	// Force the upload route: the reader is the thing under test, and it never
	// produces bytes.
	limits.MaxFileBytes = 0
	daemon.inputLimits = limits
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
		_, err := conversation.Send(ctx, "read", WithFiles(InputFile{ID: "file", Name: "a", Size: 1, SHA256: sum[:], Content: reader}), WithRequestID("cancel"))
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
		t.Fatal("Send did not return after cancellation")
	}
	if _, err := reader.Read(make([]byte, 1)); err == nil {
		t.Fatal("canceled reader remained open")
	}
}
