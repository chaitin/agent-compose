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

	"github.com/chaitin/agent-compose/pkg/apierror"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/workspaces"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/errdetail"
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
	store := &characterizationSandboxStore{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: uuid.NewString()}}}
	return newSandboxWorkspaceTestClientWithStore(t, reader, store)
}

func newSandboxWorkspaceTestClientWithStore(t *testing.T, reader workspaces.SandboxFiles, store SandboxStore) agentcomposev2connect.SandboxServiceClient {
	t.Helper()
	handler := NewSandboxHandler(SandboxHandlerDeps{
		Delegate: &characterizationSessionDelegate{},
		Store:    store,
	}).WithWorkspaceFiles(reader)
	path, serviceHandler := agentcomposev2connect.NewSandboxServiceHandler(handler)
	mux := http.NewServeMux()
	mux.Handle(path, serviceHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return agentcomposev2connect.NewSandboxServiceClient(server.Client(), server.URL)
}

func TestIntegrationListSandboxWorkspaceMapsEntriesAndPagination(t *testing.T) {
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

func TestIntegrationDownloadSandboxWorkspaceFileStreamsInfoThenChunks(t *testing.T) {
	// Span three 64 KiB chunks so the buffer-reuse path is exercised.
	content := bytes.Repeat([]byte("report"), 32768)
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
	chunks := 0
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
			chunks++
			got = append(got, payload.Chunk...)
		default:
			t.Fatalf("unexpected payload %T", payload)
		}
	}
	if chunks < 2 {
		t.Fatalf("chunk count = %d, want >= 2 to cover the multi-chunk path", chunks)
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

func TestIntegrationDownloadSandboxWorkspaceFileEmptyFileSendsOnlyInfo(t *testing.T) {
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

func TestIntegrationSandboxWorkspaceRPCMapsErrors(t *testing.T) {
	cases := []struct {
		name    string
		listErr error
		code    connect.Code
		reason  apierror.Reason
	}{
		{"path not found", domain.ClassifyError(domain.ErrNotFound, "missing", nil), connect.CodeNotFound, apierror.ReasonResourceNotFound},
		{"invalid argument", domain.ClassifyError(domain.ErrRequired, "sandbox id", nil), connect.CodeInvalidArgument, apierror.ReasonInvalidRequest},
		{"internal", errors.New("disk failure"), connect.CodeInternal, apierror.ReasonInternal},
		{"precondition", domain.ClassifyError(domain.ErrFailedPrecondition, "reclaimed", nil), connect.CodeFailedPrecondition, apierror.ReasonPreconditionFailed},
		{"canceled", context.Canceled, connect.CodeCanceled, apierror.ReasonOperationCanceled},
		{"deadline", context.DeadlineExceeded, connect.CodeDeadlineExceeded, apierror.ReasonOperationTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newSandboxWorkspaceTestClient(t, &workspaceFilesTestReader{listErr: tc.listErr})
			_, err := client.ListSandboxWorkspace(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxWorkspaceRequest{
				SandboxId: uuid.NewString(),
			}))
			assertSandboxWorkspaceError(t, err, tc.code, tc.reason)
		})
	}

	t.Run("unsupported download", func(t *testing.T) {
		client := newSandboxWorkspaceTestClient(t, &workspaceFilesTestReader{openErr: domain.ClassifyError(domain.ErrUnsupported, "k8s", nil)})
		stream, err := client.DownloadSandboxWorkspaceFile(context.Background(), connect.NewRequest(&agentcomposev2.DownloadSandboxWorkspaceFileRequest{
			SandboxId: uuid.NewString(), Path: "report.md",
		}))
		if err != nil {
			t.Fatalf("DownloadSandboxWorkspaceFile setup error: %v", err)
		}
		for stream.Receive() {
		}
		assertSandboxWorkspaceError(t, stream.Err(), connect.CodeUnimplemented, apierror.ReasonUnsupported)
	})
}

func TestIntegrationSandboxWorkspaceSandboxNotFoundMapsReason(t *testing.T) {
	store := &characterizationSandboxStore{
		session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: uuid.NewString()}},
		getErr:  errors.New("sandbox gone"),
	}
	client := newSandboxWorkspaceTestClientWithStore(t, &workspaceFilesTestReader{}, store)
	_, err := client.ListSandboxWorkspace(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxWorkspaceRequest{
		SandboxId: uuid.NewString(),
	}))
	assertSandboxWorkspaceError(t, err, connect.CodeNotFound, apierror.ReasonSandboxNotFound)
}

func assertSandboxWorkspaceError(t *testing.T, err error, code connect.Code, reason apierror.Reason) {
	t.Helper()
	if got := connect.CodeOf(err); got != code {
		t.Fatalf("code = %s, want %s (err=%v)", got, code, err)
	}
	detail, ok := errdetail.From(err)
	if !ok {
		t.Fatalf("error has no operation detail: %v", err)
	}
	if got := ReasonFromProto(detail.Reason); got != reason {
		t.Fatalf("reason = %v, want %v", got, reason)
	}
}
