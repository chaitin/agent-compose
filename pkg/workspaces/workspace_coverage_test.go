package workspaces

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sources"
)

func TestWorkspaceFileAndPathWorkflows(t *testing.T) {
	testWorkspaceFileAndPathWorkflows(t)
}

func TestIntegrationWorkspaceFileAndPathWorkflows(t *testing.T) {
	testWorkspaceFileAndPathWorkflows(t)
}

func TestE2EWorkspaceFileAndPathWorkflows(t *testing.T) {
	testWorkspaceFileAndPathWorkflows(t)
}

func testWorkspaceFileAndPathWorkflows(t *testing.T) {
	t.Helper()
	config := &appconfig.Config{DataRoot: t.TempDir()}
	workspaceID := "ws-file"
	contentRoot := mustDefaultFileWorkspaceContentRoot(t, config, workspaceID)
	if err := os.MkdirAll(filepath.Join(contentRoot, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir content root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(contentRoot, "README.md"), []byte("workspace\n"), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(contentRoot, "docs", "guide.md"), []byte("guide\n"), 0o644); err != nil {
		t.Fatalf("write guide.md: %v", err)
	}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-1", WorkspacePath: t.TempDir()}}
	workspace := domain.WorkspaceConfig{
		ID:         workspaceID,
		Name:       "File Workspace",
		Type:       "file",
		ConfigJSON: encodeFileWorkspaceConfigForTest(t, contentRoot),
	}
	if err := PrepareFileWorkspace(config, session, workspace); err != nil {
		t.Fatalf("PrepareFileWorkspace returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(session.Summary.WorkspacePath, "README.md"), "workspace\n")
	assertFileContent(t, filepath.Join(session.Summary.WorkspacePath, "docs", "guide.md"), "guide\n")

	if err := os.WriteFile(filepath.Join(contentRoot, "README.md"), []byte("updated\n"), 0o644); err != nil {
		t.Fatalf("overwrite README.md: %v", err)
	}
	if err := materializeSessionWorkspace(context.Background(), config, nil, &domain.Sandbox{
		Summary:     domain.SandboxSummary{ID: "session-snapshot", WorkspacePath: session.Summary.WorkspacePath},
		WorkspaceID: workspaceID,
		Workspace: &domain.SandboxWorkspace{
			ID:         workspaceID,
			Name:       "Snapshot File Workspace",
			Type:       "file",
			ConfigJSON: DefaultFileConfigJSON(config, workspaceID),
		},
	}); err != nil {
		t.Fatalf("materializeSessionWorkspace returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(session.Summary.WorkspacePath, "README.md"), "updated\n")

	if err := PrepareFileWorkspace(config, &domain.Sandbox{Summary: domain.SandboxSummary{ID: "missing-workspace"}}, workspace); err == nil {
		t.Fatalf("PrepareFileWorkspace missing workspace path returned nil error")
	}
	if _, err := FileWorkspaceContentRoot(config, domain.WorkspaceConfig{}); err == nil {
		t.Fatalf("FileWorkspaceContentRoot missing id returned nil error")
	}
	if _, err := FileWorkspaceContentRoot(config, domain.WorkspaceConfig{ID: "bad-json", ConfigJSON: `{bad json`}); err == nil {
		t.Fatalf("FileWorkspaceContentRoot invalid JSON returned nil error")
	}
	if _, err := FileWorkspaceContentRoot(config, domain.WorkspaceConfig{ID: "relative-root", ConfigJSON: `{"root":"relative"}`}); err == nil {
		t.Fatalf("FileWorkspaceContentRoot relative root returned nil error")
	}
	if _, err := FileWorkspaceContentRoot(config, domain.WorkspaceConfig{ID: "ws-file", Name: "File Workspace", Type: "file", ConfigJSON: encodeFileWorkspaceConfigForTest(t, t.TempDir())}); err == nil {
		t.Fatalf("expected outside data root to be rejected")
	}
	for _, workspaceID := range []string{"", ".", "..", "a/b", "/abs"} {
		if _, err := FileWorkspaceContentRelRoot(workspaceID); err == nil {
			t.Fatalf("FileWorkspaceContentRelRoot(%q) returned nil error", workspaceID)
		}
	}
	if _, err := ValidateFileWorkspaceConfig(config, "ws-ok", DefaultFileConfigJSON(config, "ws-ok")); err != nil {
		t.Fatalf("ValidateFileWorkspaceConfig returned error: %v", err)
	}
	legacyWorkspaceID := "workspace-coder-local-edeef51e1788"
	legacyConfig := encodeFileWorkspaceConfigForTest(t, legacyContainerFileWorkspaceContentRoot(legacyWorkspaceID))
	gotLegacyRoot, err := ValidateFileWorkspaceConfig(config, legacyWorkspaceID, legacyConfig)
	if err != nil {
		t.Fatalf("ValidateFileWorkspaceConfig legacy container root returned error: %v", err)
	}
	wantLegacyRoot := mustDefaultFileWorkspaceContentRoot(t, config, legacyWorkspaceID)
	if gotLegacyRoot != wantLegacyRoot {
		t.Fatalf("legacy container root = %q, want %q", gotLegacyRoot, wantLegacyRoot)
	}

	dataRoot, err := OpenFileWorkspaceDataRoot(config)
	if err != nil {
		t.Fatalf("OpenFileWorkspaceDataRoot returned error: %v", err)
	}
	defer func() { _ = dataRoot.Close() }()
	if err := EnsureRootParentDir(dataRoot, "nested/file.txt"); err != nil {
		t.Fatalf("EnsureRootParentDir returned error: %v", err)
	}
	if err := EnsureRootDir(dataRoot, "nested"); err != nil {
		t.Fatalf("EnsureRootDir returned error: %v", err)
	}
	file, err := dataRoot.OpenFile("not-dir", os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("create not-dir file: %v", err)
	}
	_ = file.Close()
	if err := EnsureRootDir(dataRoot, "not-dir"); err == nil {
		t.Fatalf("EnsureRootDir accepted file path")
	}
	for _, raw := range []string{"", ".", "/abs", "../x", "x/../../y"} {
		if _, err := CleanRelativePath(raw, false); err == nil {
			t.Fatalf("CleanRelativePath(%q) returned nil error", raw)
		}
	}
	if got, err := CleanRelativePath("", true); err != nil || got != "" {
		t.Fatalf("CleanRelativePath empty allowed = %q/%v", got, err)
	}
	if got, err := CleanRelativePath(" a/../b ", false); err != nil || got != "b" {
		t.Fatalf("CleanRelativePath clean = %q/%v", got, err)
	}

	files, err := ListFiles(dataRoot)
	if err != nil {
		t.Fatalf("ListFiles returned error: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("ListFiles returned no entries")
	}
	symlinkDirRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(symlinkDirRoot, "target"), 0o755); err != nil {
		t.Fatalf("mkdir symlink dir target: %v", err)
	}
	if err := os.Symlink("target", filepath.Join(symlinkDirRoot, "link-dir")); err != nil {
		t.Fatalf("create symlink dir: %v", err)
	}
	linkRoot, err := os.OpenRoot(symlinkDirRoot)
	if err != nil {
		t.Fatalf("OpenRoot symlinkDirRoot: %v", err)
	}
	defer func() { _ = linkRoot.Close() }()
	if err := EnsureRootDir(linkRoot, "link-dir"); err == nil {
		t.Fatalf("EnsureRootDir accepted symlink")
	}

	fileDataRoot := filepath.Join(t.TempDir(), "data-file")
	if err := os.WriteFile(fileDataRoot, []byte("not dir"), 0o644); err != nil {
		t.Fatalf("write file data root: %v", err)
	}
	if _, err := OpenFileWorkspaceDataRoot(&appconfig.Config{DataRoot: fileDataRoot}); err == nil {
		t.Fatalf("OpenFileWorkspaceDataRoot accepted file")
	}
	symlinkParent := t.TempDir()
	symlinkTarget := filepath.Join(symlinkParent, "target")
	if err := os.Mkdir(symlinkTarget, 0o755); err != nil {
		t.Fatalf("mkdir symlink target: %v", err)
	}
	symlinkRoot := filepath.Join(symlinkParent, "link")
	if err := os.Symlink(symlinkTarget, symlinkRoot); err != nil {
		t.Fatalf("symlink data root: %v", err)
	}
	if _, err := OpenFileWorkspaceDataRoot(&appconfig.Config{DataRoot: symlinkRoot}); err == nil {
		t.Fatalf("OpenFileWorkspaceDataRoot accepted symlink")
	}
}

func TestWorkspaceArchiveAndUploadWorkflows(t *testing.T) {
	testWorkspaceArchiveAndUploadWorkflows(t)
}

func TestIntegrationWorkspaceArchiveAndUploadWorkflows(t *testing.T) {
	testWorkspaceArchiveAndUploadWorkflows(t)
}

func TestE2EWorkspaceArchiveAndUploadWorkflows(t *testing.T) {
	testWorkspaceArchiveAndUploadWorkflows(t)
}

func TestIntegrationWorkspaceGitHelperWorkflow(t *testing.T) {
	TestWorkspaceGitHelpers(t)
}

func TestE2EWorkspaceGitHelperWorkflow(t *testing.T) {
	TestWorkspaceGitHelpers(t)
}

func testWorkspaceArchiveAndUploadWorkflows(t *testing.T) {
	t.Helper()
	contentRoot := t.TempDir()
	root, err := os.OpenRoot(contentRoot)
	if err != nil {
		t.Fatalf("OpenRoot contentRoot: %v", err)
	}
	defer func() { _ = root.Close() }()

	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	writeTarDir(t, tw, "dir")
	writeTarFile(t, tw, "dir/file.txt", "from archive\n")
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := ExtractWorkspaceTarArchive(&archive, root); err != nil {
		t.Fatalf("ExtractWorkspaceTarArchive returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(contentRoot, "dir", "file.txt"), "from archive\n")
	if err := ExtractWorkspaceTarArchive(errReader{}, root); err == nil || !strings.Contains(err.Error(), "read tar archive") {
		t.Fatalf("ExtractWorkspaceTarArchive reader error = %v", err)
	}

	var badArchive bytes.Buffer
	badTW := tar.NewWriter(&badArchive)
	if err := badTW.WriteHeader(&tar.Header{Name: "../escape.txt", Mode: 0o644, Size: 1}); err != nil {
		t.Fatalf("WriteHeader bad: %v", err)
	}
	if _, err := badTW.Write([]byte("x")); err != nil {
		t.Fatalf("write bad body: %v", err)
	}
	if err := badTW.Close(); err != nil {
		t.Fatalf("close bad tar: %v", err)
	}
	if err := ExtractWorkspaceTarArchive(&badArchive, root); err == nil {
		t.Fatalf("ExtractWorkspaceTarArchive accepted escaping path")
	}
	for _, tc := range []struct {
		name   string
		header tar.Header
	}{
		{name: "absolute", header: tar.Header{Name: "/abs.txt", Typeflag: tar.TypeReg, Mode: 0o644}},
		{name: "symlink", header: tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "dir/file.txt"}},
		{name: "fifo", header: tar.Header{Name: "pipe", Typeflag: tar.TypeFifo}},
	} {
		var archive bytes.Buffer
		writer := tar.NewWriter(&archive)
		if err := writer.WriteHeader(&tc.header); err != nil {
			t.Fatalf("WriteHeader %s: %v", tc.name, err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close %s archive: %v", tc.name, err)
		}
		if err := ExtractWorkspaceTarArchive(&archive, root); err == nil {
			t.Fatalf("ExtractWorkspaceTarArchive accepted %s entry", tc.name)
		}
	}

	srcRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcRoot, "plain.txt"), []byte("plain\n"), 0o644); err != nil {
		t.Fatalf("write plain source: %v", err)
	}
	if err := os.Symlink("plain.txt", filepath.Join(srcRoot, "plain-link")); err != nil {
		t.Fatalf("symlink plain source: %v", err)
	}
	src, err := os.OpenRoot(srcRoot)
	if err != nil {
		t.Fatalf("OpenRoot srcRoot: %v", err)
	}
	defer func() { _ = src.Close() }()
	if err := CopyRootDirectoryContents(src, t.TempDir()); err == nil {
		t.Fatalf("CopyRootDirectoryContents accepted symlink")
	}

	header := multipartFileHeader(t, "upload.txt", "uploaded\n")
	if err := StoreUploadedFile(header, root, "uploads/upload.txt"); err != nil {
		t.Fatalf("StoreUploadedFile returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(contentRoot, "uploads", "upload.txt"), "uploaded\n")

	archiveHeader := multipartArchiveHeader(t, "archive.tar")
	if err := ExtractUploadedArchive(archiveHeader, root); err != nil {
		t.Fatalf("ExtractUploadedArchive returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(contentRoot, "uploaded-archive.txt"), "archive body\n")
}

func TestWorkspaceGitHelpers(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{raw: "", want: "."},
		{raw: "repo/subdir", want: filepath.Clean("repo/subdir")},
		{raw: "repo/../src", want: "src"},
		{raw: "/tmp/repo", wantErr: true},
		{raw: "../repo", wantErr: true},
		{raw: "repo/../../escape", wantErr: true},
	} {
		got, err := NormalizeWorkspaceTarget("ws-1", tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("NormalizeWorkspaceTarget(%q) returned nil error", tc.raw)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("NormalizeWorkspaceTarget(%q) = %q/%v, want %q", tc.raw, got, err, tc.want)
		}
	}
	src := filepath.Join(t.TempDir(), "src")
	dst := filepath.Join(t.TempDir(), "dst")
	if err := os.MkdirAll(filepath.Join(src, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "nested", "file.txt"), []byte("moved\n"), 0o644); err != nil {
		t.Fatalf("write src file: %v", err)
	}
	if err := MoveWorkspaceEntry(src, dst); err != nil {
		t.Fatalf("MoveWorkspaceEntry merge returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(dst, "nested", "file.txt"), "moved\n")
}

func TestWorkspaceGitPrepareWorkflow(t *testing.T) {
	testWorkspaceGitPrepareWorkflow(t)
}

func TestIntegrationWorkspaceGitPrepareWorkflow(t *testing.T) {
	testWorkspaceGitPrepareWorkflow(t)
}

func TestE2EWorkspaceGitPrepareWorkflow(t *testing.T) {
	testWorkspaceGitPrepareWorkflow(t)
}

func testWorkspaceGitPrepareWorkflow(t *testing.T) {
	t.Helper()
	sourceRepo := createGitWorkspaceSourceRepo(t)
	cloneURL := "file://" + filepath.ToSlash(sourceRepo.path)

	rootWorkspace := t.TempDir()
	rootWorkspaceConfig := encodeGitWorkspaceConfigForTest(t, GitWorkspaceConfig{
		Source: sources.Source{Provider: sources.ProviderGit, URL: cloneURL, Ref: sourceRepo.firstCommit},
	})
	if err := PrepareGitWorkspace(context.Background(), &domain.Sandbox{
		Summary: domain.SandboxSummary{ID: "session-git-root", WorkspacePath: rootWorkspace},
	}, domain.WorkspaceConfig{
		ID:         "ws-git-root",
		Name:       "Git Root Workspace",
		Type:       "git",
		ConfigJSON: rootWorkspaceConfig,
	}); err != nil {
		t.Fatalf("PrepareGitWorkspace root returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(rootWorkspace, "README.md"), "first\n")
	if _, err := os.Stat(filepath.Join(rootWorkspace, GitWorkspaceTempDirName)); !os.IsNotExist(err) {
		t.Fatalf("temporary git clone directory still exists: %v", err)
	}

	nestedWorkspace := t.TempDir()
	if err := PrepareGitWorkspace(context.Background(), &domain.Sandbox{
		Summary: domain.SandboxSummary{ID: "session-git-nested", WorkspacePath: nestedWorkspace},
	}, domain.WorkspaceConfig{
		ID:   "ws-git-nested",
		Name: "Git Nested Workspace",
		Type: "git",
		ConfigJSON: encodeGitWorkspaceConfigForTest(t, GitWorkspaceConfig{
			Source: sources.Source{Provider: sources.ProviderGit, URL: cloneURL, Ref: "main"},
			Target: "nested/repo",
		}),
	}); err != nil {
		t.Fatalf("PrepareGitWorkspace nested returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(nestedWorkspace, "nested", "repo", "README.md"), "second\n")
}

type gitWorkspaceSourceRepo struct {
	path        string
	firstCommit string
}

func createGitWorkspaceSourceRepo(t *testing.T) gitWorkspaceSourceRepo {
	t.Helper()
	repo := t.TempDir()
	runGitForTest(t, "", "init", "-b", "main", repo)
	runGitForTest(t, repo, "config", "user.email", "agent-compose@example.test")
	runGitForTest(t, repo, "config", "user.name", "Agent Compose")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("first\n"), 0o644); err != nil {
		t.Fatalf("write first README.md: %v", err)
	}
	runGitForTest(t, repo, "add", "README.md")
	runGitForTest(t, repo, "commit", "-m", "first")
	firstCommit := strings.TrimSpace(runGitForTest(t, repo, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("second\n"), 0o644); err != nil {
		t.Fatalf("write second README.md: %v", err)
	}
	runGitForTest(t, repo, "commit", "-am", "second")
	return gitWorkspaceSourceRepo{path: repo, firstCommit: firstCommit}
}

func runGitForTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output)
}

func encodeGitWorkspaceConfigForTest(t *testing.T, cfg GitWorkspaceConfig) string {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal git workspace config: %v", err)
	}
	return string(data)
}

func encodeFileWorkspaceConfigForTest(t *testing.T, root string) string {
	t.Helper()
	data, err := json.Marshal(FileWorkspaceConfig{Root: root})
	if err != nil {
		t.Fatalf("marshal file workspace config: %v", err)
	}
	return string(data)
}

func mustDefaultFileWorkspaceContentRoot(t *testing.T, config *appconfig.Config, workspaceID string) string {
	t.Helper()
	root, err := DefaultFileWorkspaceContentRoot(config, workspaceID)
	if err != nil {
		t.Fatalf("DefaultFileWorkspaceContentRoot returned error: %v", err)
	}
	return root
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("ReadFile(%s) = %q, want %q", path, string(got), want)
	}
}

func writeTarDir(t *testing.T, tw *tar.Writer, name string) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatalf("WriteHeader dir %s: %v", name, err)
	}
}

func writeTarFile(t *testing.T, tw *tar.Writer, name, body string) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatalf("WriteHeader file %s: %v", name, err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatalf("write file %s: %v", name, err)
	}
}

func multipartFileHeader(t *testing.T, filename, body string) *multipart.FileHeader {
	t.Helper()
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write([]byte(body)); err != nil {
		t.Fatalf("write multipart body: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	reader := multipart.NewReader(&payload, writer.Boundary())
	form, err := reader.ReadForm(1 << 20)
	if err != nil {
		t.Fatalf("ReadForm: %v", err)
	}
	return form.File["file"][0]
}

func multipartArchiveHeader(t *testing.T, filename string) *multipart.FileHeader {
	t.Helper()
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	writeTarFile(t, tw, "uploaded-archive.txt", "archive body\n")
	if err := tw.Close(); err != nil {
		t.Fatalf("close archive tar: %v", err)
	}
	return multipartFileHeader(t, filename, archive.String())
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}
