package e2e

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const e2eWorkspaceHTTPBodyLimit = 8 << 20

type e2eWorkspaceUploadFile struct {
	Path    string
	Content []byte
	Mode    fs.FileMode
}

type e2eWorkspaceFileEntry struct {
	Path      string `json:"path"`
	Dir       bool   `json:"dir"`
	Size      int64  `json:"size"`
	UpdatedAt string `json:"updated_at"`
}

type e2eWorkspaceFilesResponse struct {
	WorkspaceID string                  `json:"workspace_id"`
	Files       []e2eWorkspaceFileEntry `json:"files"`
}

type e2eWorkspaceFileExpectation struct {
	Path string
	Dir  bool
	Size int64
}

type e2eWorkspaceDownload struct {
	Bytes              []byte
	ContentType        string
	ContentDisposition string
	Filename           string
	ContentLength      int64
}

func TestWorkspaceHTTPHelpersUsePublicRouteContracts(t *testing.T) {
	workspaceID := "workspace/id with space"
	uploads := []e2eWorkspaceUploadFile{
		{Path: "modified.txt", Content: []byte("source-v1"), Mode: 0o600},
		{Path: "deleted.txt", Content: []byte("delete-me"), Mode: 0o640},
	}
	entries := []e2eWorkspaceFileEntry{
		{Path: "deleted.txt", Size: int64(len("delete-me")), UpdatedAt: "2026-07-13T01:02:03Z"},
		{Path: "modified.txt", Size: int64(len("source-v1")), UpdatedAt: "2026-07-13T01:02:04Z"},
	}
	wantEntries := []e2eWorkspaceFileExpectation{
		{Path: "modified.txt", Size: int64(len("source-v1"))},
		{Path: "deleted.txt", Size: int64(len("delete-me"))},
	}
	downloadPath := "nested/file & name.txt"
	downloadContent := []byte("template-content")
	var uploadSeen atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPrefix := "/api/agent-compose/workspaces/" + url.PathEscape(workspaceID) + "/"
		if !strings.HasPrefix(r.URL.EscapedPath(), wantPrefix) {
			t.Errorf("workspace request escaped path = %q, want prefix %q", r.URL.EscapedPath(), wantPrefix)
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.EscapedPath(), "/upload"):
			if err := assertE2EWorkspaceArchiveRequest(r, uploads); err != nil {
				t.Errorf("workspace archive request: %v", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			uploadSeen.Store(true)
			writeE2EWorkspaceFilesResponse(t, w, workspaceID, entries)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.EscapedPath(), "/files"):
			writeE2EWorkspaceFilesResponse(t, w, workspaceID, entries)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.EscapedPath(), "/download"):
			if got := r.URL.Query().Get("path"); got != downloadPath {
				t.Errorf("workspace download path = %q, want %q", got, downloadPath)
				http.Error(w, "wrong download path", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="file & name.txt"`)
			_, _ = w.Write(downloadContent)
		default:
			http.Error(w, "wrong method or operation", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	ctx := context.Background()
	uploadResponse := uploadE2EWorkspaceFiles(t, ctx, server.Client(), server.URL+"/", workspaceID, uploads)
	assertE2EWorkspaceFiles(t, uploadResponse, workspaceID, wantEntries)
	if !uploadSeen.Load() {
		t.Fatal("workspace upload route was not called")
	}
	listResponse := listE2EWorkspaceFiles(t, ctx, server.Client(), server.URL, workspaceID)
	assertE2EWorkspaceFiles(t, listResponse, workspaceID, wantEntries)
	download := downloadE2EWorkspaceFile(t, ctx, server.Client(), server.URL, workspaceID, downloadPath)
	if !bytes.Equal(download.Bytes, downloadContent) ||
		download.ContentType != "application/octet-stream" ||
		download.ContentDisposition != `attachment; filename="file & name.txt"` ||
		download.Filename != "file & name.txt" ||
		download.ContentLength != int64(len(downloadContent)) {
		t.Fatalf("workspace download = %+v, want content=%q and exact response metadata", download, downloadContent)
	}
}

func assertE2EWorkspaceArchiveRequest(r *http.Request, want []e2eWorkspaceUploadFile) error {
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		return fmt.Errorf("parse multipart form: %w", err)
	}
	if got := r.FormValue("upload_type"); got != "archive" {
		return fmt.Errorf("upload_type = %q, want archive", got)
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		return fmt.Errorf("read multipart file: %w", err)
	}
	defer func() { _ = file.Close() }()

	wantByPath := make(map[string]e2eWorkspaceUploadFile, len(want))
	for _, item := range want {
		wantByPath[item.Path] = item
	}
	tarReader := tar.NewReader(file)
	seen := make(map[string]struct{}, len(want))
	for {
		header, nextErr := tarReader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return fmt.Errorf("read archive header: %w", nextErr)
		}
		expected, ok := wantByPath[header.Name]
		if !ok {
			return fmt.Errorf("unexpected archive path %q", header.Name)
		}
		if header.Typeflag != tar.TypeReg || header.Mode != int64(expected.Mode.Perm()) {
			return fmt.Errorf("archive header %q type/mode = %d/%#o, want %d/%#o", header.Name, header.Typeflag, header.Mode, tar.TypeReg, expected.Mode.Perm())
		}
		content, readErr := io.ReadAll(tarReader)
		if readErr != nil {
			return fmt.Errorf("read archive content %q: %w", header.Name, readErr)
		}
		if !bytes.Equal(content, expected.Content) {
			return fmt.Errorf("archive content %q = %q, want %q", header.Name, content, expected.Content)
		}
		seen[header.Name] = struct{}{}
	}
	if len(seen) != len(wantByPath) {
		return fmt.Errorf("archive paths = %v, want %v", seen, wantByPath)
	}
	return nil
}

func writeE2EWorkspaceFilesResponse(t *testing.T, w http.ResponseWriter, workspaceID string, files []e2eWorkspaceFileEntry) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(e2eWorkspaceFilesResponse{WorkspaceID: workspaceID, Files: files}); err != nil {
		t.Errorf("encode workspace files response: %v", err)
	}
}

func uploadE2EWorkspaceFiles(
	t *testing.T,
	ctx context.Context,
	client *http.Client,
	baseURL string,
	workspaceID string,
	files []e2eWorkspaceUploadFile,
) e2eWorkspaceFilesResponse {
	t.Helper()
	if len(files) == 0 {
		t.Fatal("workspace archive upload requires at least one file")
	}

	ordered := append([]e2eWorkspaceUploadFile(nil), files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	seen := make(map[string]struct{}, len(ordered))
	var archive bytes.Buffer
	tarWriter := tar.NewWriter(&archive)
	for _, file := range ordered {
		cleanPath, err := cleanE2EWorkspaceUploadPath(file.Path)
		if err != nil {
			t.Fatalf("validate workspace upload path %q: %v", file.Path, err)
		}
		if _, exists := seen[cleanPath]; exists {
			t.Fatalf("workspace archive upload has duplicate path %q", cleanPath)
		}
		seen[cleanPath] = struct{}{}
		mode := file.Mode.Perm()
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{
			Name:     cleanPath,
			Mode:     int64(mode),
			Size:     int64(len(file.Content)),
			Typeflag: tar.TypeReg,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("write workspace archive header for %q: %v", cleanPath, err)
		}
		if _, err := tarWriter.Write(file.Content); err != nil {
			t.Fatalf("write workspace archive content for %q: %v", cleanPath, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close workspace upload archive: %v", err)
	}

	var body bytes.Buffer
	multipartWriter := multipart.NewWriter(&body)
	part, err := multipartWriter.CreateFormFile("file", "workspace-template.tar")
	if err != nil {
		t.Fatalf("create workspace archive multipart part: %v", err)
	}
	if _, err := part.Write(archive.Bytes()); err != nil {
		t.Fatalf("write workspace archive multipart part: %v", err)
	}
	if err := multipartWriter.WriteField("upload_type", "archive"); err != nil {
		t.Fatalf("write workspace upload type: %v", err)
	}
	if err := multipartWriter.Close(); err != nil {
		t.Fatalf("close workspace upload multipart body: %v", err)
	}

	endpoint := e2eWorkspaceHTTPEndpoint(baseURL, workspaceID, "upload", nil)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body.Bytes()))
	if err != nil {
		t.Fatalf("create workspace upload request: %v", err)
	}
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST workspace upload: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes := readE2EWorkspaceHTTPBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST workspace upload status = %d, want 200; body=%q", resp.StatusCode, bodyBytes)
	}
	return decodeE2EWorkspaceFilesResponse(t, bodyBytes, workspaceID)
}

func listE2EWorkspaceFiles(
	t *testing.T,
	ctx context.Context,
	client *http.Client,
	baseURL string,
	workspaceID string,
) e2eWorkspaceFilesResponse {
	t.Helper()
	endpoint := e2eWorkspaceHTTPEndpoint(baseURL, workspaceID, "files", nil)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatalf("create workspace list request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET workspace files: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes := readE2EWorkspaceHTTPBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET workspace files status = %d, want 200; body=%q", resp.StatusCode, bodyBytes)
	}
	return decodeE2EWorkspaceFilesResponse(t, bodyBytes, workspaceID)
}

func assertE2EWorkspaceFiles(
	t *testing.T,
	got e2eWorkspaceFilesResponse,
	workspaceID string,
	want []e2eWorkspaceFileExpectation,
) {
	t.Helper()
	if got.WorkspaceID != workspaceID {
		t.Fatalf("workspace files workspace_id = %q, want %q", got.WorkspaceID, workspaceID)
	}
	wantOrdered := append([]e2eWorkspaceFileExpectation(nil), want...)
	sort.Slice(wantOrdered, func(i, j int) bool { return wantOrdered[i].Path < wantOrdered[j].Path })
	if len(got.Files) != len(wantOrdered) {
		t.Fatalf("workspace file count = %d, want %d; files=%+v", len(got.Files), len(wantOrdered), got.Files)
	}
	for i, expected := range wantOrdered {
		entry := got.Files[i]
		if entry.Path != expected.Path || entry.Dir != expected.Dir || entry.Size != expected.Size {
			t.Fatalf("workspace file[%d] = {path:%q dir:%t size:%d}, want {path:%q dir:%t size:%d}",
				i, entry.Path, entry.Dir, entry.Size, expected.Path, expected.Dir, expected.Size)
		}
		updatedAt, err := time.Parse(time.RFC3339Nano, entry.UpdatedAt)
		if err != nil || updatedAt.IsZero() {
			t.Fatalf("workspace file %q updated_at = %q, want non-zero RFC3339Nano timestamp: %v", entry.Path, entry.UpdatedAt, err)
		}
	}
}

func downloadE2EWorkspaceFile(
	t *testing.T,
	ctx context.Context,
	client *http.Client,
	baseURL string,
	workspaceID string,
	filePath string,
) e2eWorkspaceDownload {
	t.Helper()
	query := url.Values{"path": []string{filePath}}
	endpoint := e2eWorkspaceHTTPEndpoint(baseURL, workspaceID, "download", query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatalf("create workspace download request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET workspace download %q: %v", filePath, err)
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes := readE2EWorkspaceHTTPBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET workspace download %q status = %d, want 200; body=%q", filePath, resp.StatusCode, bodyBytes)
	}

	contentType := resp.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/octet-stream" {
		t.Fatalf("workspace download %q Content-Type = %q, want application/octet-stream: %v", filePath, contentType, err)
	}
	contentDisposition := resp.Header.Get("Content-Disposition")
	disposition, params, err := mime.ParseMediaType(contentDisposition)
	if err != nil || disposition != "attachment" {
		t.Fatalf("workspace download %q Content-Disposition = %q, want attachment: %v", filePath, contentDisposition, err)
	}
	filename := params["filename"]
	if wantFilename := path.Base(filePath); filename != wantFilename {
		t.Fatalf("workspace download %q filename = %q, want %q", filePath, filename, wantFilename)
	}
	return e2eWorkspaceDownload{
		Bytes:              bodyBytes,
		ContentType:        contentType,
		ContentDisposition: contentDisposition,
		Filename:           filename,
		ContentLength:      resp.ContentLength,
	}
}

func e2eWorkspaceHTTPEndpoint(baseURL, workspaceID, operation string, query url.Values) string {
	endpoint := strings.TrimRight(baseURL, "/") + "/api/agent-compose/workspaces/" + url.PathEscape(workspaceID) + "/" + operation
	if len(query) != 0 {
		endpoint += "?" + query.Encode()
	}
	return endpoint
}

func cleanE2EWorkspaceUploadPath(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("path is empty")
	}
	if strings.Contains(raw, "\\") {
		return "", fmt.Errorf("path must use forward slashes")
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return "", fmt.Errorf("path must be a relative file path")
	}
	if clean != raw {
		return "", fmt.Errorf("path is not clean (want %q)", clean)
	}
	return clean, nil
}

func decodeE2EWorkspaceFilesResponse(t *testing.T, data []byte, workspaceID string) e2eWorkspaceFilesResponse {
	t.Helper()
	var response e2eWorkspaceFilesResponse
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		t.Fatalf("decode workspace files response: %v; body=%q", err, data)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("decode workspace files response trailing data: %v; body=%q", err, data)
	}
	if response.WorkspaceID != workspaceID {
		t.Fatalf("workspace files response workspace_id = %q, want %q", response.WorkspaceID, workspaceID)
	}
	for i, entry := range response.Files {
		if entry.Path == "" || entry.Size < 0 {
			t.Fatalf("workspace files response entry[%d] is invalid: %+v", i, entry)
		}
		if updatedAt, err := time.Parse(time.RFC3339Nano, entry.UpdatedAt); err != nil || updatedAt.IsZero() {
			t.Fatalf("workspace files response entry[%d] updated_at = %q, want non-zero RFC3339Nano timestamp: %v", i, entry.UpdatedAt, err)
		}
		if i > 0 && response.Files[i-1].Path >= entry.Path {
			t.Fatalf("workspace files response is not strictly path-sorted at entries %d and %d: %+v", i-1, i, response.Files)
		}
	}
	return response
}

func readE2EWorkspaceHTTPBody(t *testing.T, body io.Reader) []byte {
	t.Helper()
	data, err := io.ReadAll(io.LimitReader(body, e2eWorkspaceHTTPBodyLimit+1))
	if err != nil {
		t.Fatalf("read workspace HTTP response body: %v", err)
	}
	if len(data) > e2eWorkspaceHTTPBodyLimit {
		t.Fatalf("workspace HTTP response body exceeds %d bytes", e2eWorkspaceHTTPBodyLimit)
	}
	return data
}
