package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/workspaces"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type workspaceFilesTestReader struct {
	entries    []workspaces.SandboxEntry
	total      int
	content    []byte
	listErr    error
	openErr    error
	lastPath   string
	lastOffset int
	lastLimit  int
}

func (r *workspaceFilesTestReader) ListSandboxFiles(_ context.Context, _ *domain.Sandbox, path string, offset, limit int) ([]workspaces.SandboxEntry, int, error) {
	r.lastPath, r.lastOffset, r.lastLimit = path, offset, limit
	if r.listErr != nil {
		return nil, 0, r.listErr
	}
	return r.entries, r.total, nil
}

func (r *workspaceFilesTestReader) OpenSandboxFile(_ context.Context, _ *domain.Sandbox, path string) (io.ReadCloser, workspaces.SandboxFileInfo, error) {
	r.lastPath = path
	if r.openErr != nil {
		return nil, workspaces.SandboxFileInfo{}, r.openErr
	}
	return io.NopCloser(bytes.NewReader(r.content)), workspaces.SandboxFileInfo{
		Path: path, Size: int64(len(r.content)),
	}, nil
}

func newSandboxWorkspaceTestClient(t *testing.T, reader workspaces.SandboxFiles) agentcomposev2connect.SandboxServiceClient {
	t.Helper()
	sandboxID := uuid.NewString()
	handler := NewSandboxHandler(SandboxHandlerDeps{
		Delegate: &characterizationSessionDelegate{},
		Store:    &characterizationSandboxStore{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID}}},
	}).WithWorkspaceFiles(reader)
	path, serviceHandler := agentcomposev2connect.NewSandboxServiceHandler(handler)
	mux := http.NewServeMux()
	mux.Handle(path, serviceHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return agentcomposev2connect.NewSandboxServiceClient(server.Client(), server.URL)
}

func TestListSandboxWorkspaceMapsEntriesAndPagination(t *testing.T) {
	reader := &workspaceFilesTestReader{
		entries: []workspaces.SandboxEntry{
			{Path: "report.md", Name: "report.md", Kind: workspaces.SandboxEntryFile, Size: 5},
			{Path: "reports", Name: "reports", Kind: workspaces.SandboxEntryDirectory},
			{Path: "link", Name: "link", Kind: workspaces.SandboxEntrySymlink},
		},
		total: 3,
	}
	client := newSandboxWorkspaceTestClient(t, reader)
	response, err := client.ListSandboxWorkspace(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxWorkspaceRequest{
		SandboxId: uuid.NewString(), Path: "reports", Offset: 1, Limit: 2,
	}))
	if err != nil {
		t.Fatalf("ListSandboxWorkspace: %v", err)
	}
	if response.Msg.GetTotal() != 3 || len(response.Msg.GetEntries()) != 3 {
		t.Fatalf("response = %#v", response.Msg)
	}
	if got := response.Msg.GetEntries()[0].GetType(); got != agentcomposev2.SandboxWorkspaceEntryType_SANDBOX_WORKSPACE_ENTRY_TYPE_FILE {
		t.Errorf("file type = %v", got)
	}
	if got := response.Msg.GetEntries()[1].GetType(); got != agentcomposev2.SandboxWorkspaceEntryType_SANDBOX_WORKSPACE_ENTRY_TYPE_DIRECTORY {
		t.Errorf("directory type = %v", got)
	}
	if got := response.Msg.GetEntries()[2].GetType(); got != agentcomposev2.SandboxWorkspaceEntryType_SANDBOX_WORKSPACE_ENTRY_TYPE_SYMLINK {
		t.Errorf("symlink type = %v", got)
	}
	if reader.lastPath != "reports" || reader.lastOffset != 1 || reader.lastLimit != 2 {
		t.Fatalf("reader arguments = %q/%d/%d", reader.lastPath, reader.lastOffset, reader.lastLimit)
	}
}

func TestDownloadSandboxWorkspaceFileStreamsInfoThenChunks(t *testing.T) {
	content := bytes.Repeat([]byte("report"), 1024)
	reader := &workspaceFilesTestReader{content: content}
	client := newSandboxWorkspaceTestClient(t, reader)
	stream, err := client.DownloadSandboxWorkspaceFile(context.Background(), connect.NewRequest(&agentcomposev2.DownloadSandboxWorkspaceFileRequest{
		SandboxId: uuid.NewString(), Path: "report.md",
	}))
	if err != nil {
		t.Fatalf("DownloadSandboxWorkspaceFile: %v", err)
	}
	var info *agentcomposev2.SandboxWorkspaceFileInfo
	var got []byte
	for stream.Receive() {
		switch payload := stream.Msg().GetPayload().(type) {
		case *agentcomposev2.DownloadSandboxWorkspaceFileResponse_Info:
			if info != nil || len(got) != 0 {
				t.Fatalf("info frame arrived after content")
			}
			info = payload.Info
		case *agentcomposev2.DownloadSandboxWorkspaceFileResponse_Chunk:
			if info == nil {
				t.Fatalf("chunk arrived before info")
			}
			got = append(got, payload.Chunk...)
		default:
			t.Fatalf("unexpected payload %T", payload)
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if got := stream.ResponseHeader().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}
	if info == nil || info.GetPath() != "report.md" || info.GetSize() != uint64(len(content)) {
		t.Fatalf("info = %#v", info)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("content length = %d, want %d", len(got), len(content))
	}
	if reader.lastPath != "report.md" {
		t.Fatalf("path = %q", reader.lastPath)
	}
}

func TestDownloadSandboxWorkspaceFileEmptyFileSendsOnlyInfo(t *testing.T) {
	reader := &workspaceFilesTestReader{content: nil}
	client := newSandboxWorkspaceTestClient(t, reader)
	stream, err := client.DownloadSandboxWorkspaceFile(context.Background(), connect.NewRequest(&agentcomposev2.DownloadSandboxWorkspaceFileRequest{
		SandboxId: uuid.NewString(), Path: "empty.md",
	}))
	if err != nil {
		t.Fatalf("DownloadSandboxWorkspaceFile: %v", err)
	}
	var info *agentcomposev2.SandboxWorkspaceFileInfo
	chunks := 0
	for stream.Receive() {
		switch payload := stream.Msg().GetPayload().(type) {
		case *agentcomposev2.DownloadSandboxWorkspaceFileResponse_Info:
			if info != nil {
				t.Fatalf("duplicate info frame")
			}
			info = payload.Info
		case *agentcomposev2.DownloadSandboxWorkspaceFileResponse_Chunk:
			chunks++
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if info == nil || info.GetSize() != 0 || chunks != 0 {
		t.Fatalf("empty download info=%#v chunks=%d", info, chunks)
	}
}

func TestSandboxWorkspaceRPCMapsErrors(t *testing.T) {
	notFound := &workspaceFilesTestReader{listErr: domain.ClassifyError(domain.ErrNotFound, "missing", nil)}
	client := newSandboxWorkspaceTestClient(t, notFound)
	_, err := client.ListSandboxWorkspace(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxWorkspaceRequest{
		SandboxId: uuid.NewString(), Path: "missing",
	}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("not found code = %s", connect.CodeOf(err))
	}

	unsupported := &workspaceFilesTestReader{openErr: domain.ClassifyError(domain.ErrUnsupported, "k8s", nil)}
	client = newSandboxWorkspaceTestClient(t, unsupported)
	stream, err := client.DownloadSandboxWorkspaceFile(context.Background(), connect.NewRequest(&agentcomposev2.DownloadSandboxWorkspaceFileRequest{
		SandboxId: uuid.NewString(), Path: "report.md",
	}))
	if err != nil {
		t.Fatalf("DownloadSandboxWorkspaceFile setup error: %v", err)
	}
	for stream.Receive() {
	}
	if connect.CodeOf(stream.Err()) != connect.CodeUnimplemented {
		t.Fatalf("unsupported code = %s (err=%v)", connect.CodeOf(stream.Err()), stream.Err())
	}

	invalid := &workspaceFilesTestReader{listErr: domain.ClassifyError(domain.ErrRequired, "sandbox id", nil)}
	client = newSandboxWorkspaceTestClient(t, invalid)
	_, err = client.ListSandboxWorkspace(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxWorkspaceRequest{
		SandboxId: uuid.NewString(),
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid code = %s", connect.CodeOf(err))
	}

	internal := &workspaceFilesTestReader{listErr: errors.New("disk failure")}
	client = newSandboxWorkspaceTestClient(t, internal)
	_, err = client.ListSandboxWorkspace(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxWorkspaceRequest{
		SandboxId: uuid.NewString(),
	}))
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("internal code = %s", connect.CodeOf(err))
	}

	precondition := &workspaceFilesTestReader{listErr: domain.ClassifyError(domain.ErrFailedPrecondition, "reclaimed", nil)}
	client = newSandboxWorkspaceTestClient(t, precondition)
	_, err = client.ListSandboxWorkspace(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxWorkspaceRequest{
		SandboxId: uuid.NewString(),
	}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("precondition code = %s", connect.CodeOf(err))
	}

	canceled := &workspaceFilesTestReader{listErr: context.Canceled}
	client = newSandboxWorkspaceTestClient(t, canceled)
	_, err = client.ListSandboxWorkspace(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxWorkspaceRequest{
		SandboxId: uuid.NewString(),
	}))
	if connect.CodeOf(err) != connect.CodeCanceled {
		t.Fatalf("canceled code = %s", connect.CodeOf(err))
	}

	deadline := &workspaceFilesTestReader{listErr: context.DeadlineExceeded}
	client = newSandboxWorkspaceTestClient(t, deadline)
	_, err = client.ListSandboxWorkspace(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxWorkspaceRequest{
		SandboxId: uuid.NewString(),
	}))
	if connect.CodeOf(err) != connect.CodeDeadlineExceeded {
		t.Fatalf("deadline code = %s", connect.CodeOf(err))
	}
}
