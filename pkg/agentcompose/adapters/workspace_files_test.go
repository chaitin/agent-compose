package adapters

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

func TestWorkspaceFilesListAndOpen(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "sandbox", "workspace")
	if err := os.MkdirAll(filepath.Join(workspacePath, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "report.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "reports", "q1.txt"), []byte("q1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("report.md", filepath.Join(workspacePath, "link.md")); err != nil {
		t.Fatal(err)
	}
	files := NewWorkspaceFiles(&appconfig.Config{RuntimeDriver: "docker"})
	sandbox := workspaceFilesSandbox(workspacePath)

	entries, total, err := files.ListSandboxFiles(context.Background(), sandbox, "", 0, 10)
	if err != nil {
		t.Fatalf("ListSandboxFiles root: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if got := entryNames(entries); !equalStrings(got, []string{"link.md", "report.md", "reports"}) {
		t.Fatalf("root names = %v", got)
	}
	if entries[0].Kind != workspaces.SandboxEntrySymlink || entries[0].Size != 0 {
		t.Fatalf("symlink entry = %+v", entries[0])
	}
	if entries[2].Kind != workspaces.SandboxEntryDirectory || entries[2].Size != 0 {
		t.Fatalf("directory entry = %+v", entries[2])
	}

	entries, total, err = files.ListSandboxFiles(context.Background(), sandbox, "", 1, 1)
	if err != nil {
		t.Fatalf("ListSandboxFiles page: %v", err)
	}
	if total != 3 || len(entries) != 1 || entries[0].Name != "report.md" {
		t.Fatalf("page = %+v total=%d", entries, total)
	}

	entries, total, err = files.ListSandboxFiles(context.Background(), sandbox, "reports", 0, 10)
	if err != nil {
		t.Fatalf("ListSandboxFiles subdir: %v", err)
	}
	if total != 1 || len(entries) != 1 || entries[0].Path != "reports/q1.txt" {
		t.Fatalf("subdir = %+v total=%d", entries, total)
	}

	entries, total, err = files.ListSandboxFiles(context.Background(), sandbox, "", 10, 10)
	if err != nil {
		t.Fatalf("ListSandboxFiles out of range: %v", err)
	}
	if total != 3 || len(entries) != 0 {
		t.Fatalf("out of range = %+v total=%d", entries, total)
	}

	file, info, err := files.OpenSandboxFile(context.Background(), sandbox, "report.md")
	if err != nil {
		t.Fatalf("OpenSandboxFile: %v", err)
	}
	content, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || string(content) != "hello" {
		t.Fatalf("content = %q err=%v", content, err)
	}
	if info.Path != "report.md" || info.Size != 5 {
		t.Fatalf("info = %+v", info)
	}
}

func TestWorkspaceFilesListSelectsGlobalSortedPage(t *testing.T) {
	workspacePath := filepath.Join(t.TempDir(), "sandbox", "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"c.txt", "a.txt", "e.txt", "b.txt", "d.txt"} {
		if err := os.WriteFile(filepath.Join(workspacePath, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := NewWorkspaceFiles(&appconfig.Config{RuntimeDriver: "docker"})
	entries, total, err := files.ListSandboxFiles(context.Background(), workspaceFilesSandbox(workspacePath), "", 1, 2)
	if err != nil {
		t.Fatalf("ListSandboxFiles: %v", err)
	}
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	if got := entryNames(entries); !equalStrings(got, []string{"b.txt", "c.txt"}) {
		t.Fatalf("page names = %v, want [b.txt c.txt]", got)
	}
}

func TestWorkspaceFilesPathRoundTrip(t *testing.T) {
	workspacePath := filepath.Join(t.TempDir(), "sandbox", "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	names := []string{" report.md ", "back\\slash.txt", "plain.md"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(workspacePath, name), []byte(name), 0o644); err != nil {
			t.Skipf("filesystem does not allow %q: %v", name, err)
		}
	}
	files := NewWorkspaceFiles(&appconfig.Config{RuntimeDriver: "docker"})
	sandbox := workspaceFilesSandbox(workspacePath)
	entries, total, err := files.ListSandboxFiles(context.Background(), sandbox, "", 0, 10)
	if err != nil {
		t.Fatalf("ListSandboxFiles: %v", err)
	}
	if total != len(names) {
		t.Fatalf("total = %d, want %d", total, len(names))
	}
	for _, entry := range entries {
		file, _, err := files.OpenSandboxFile(context.Background(), sandbox, entry.Path)
		if err != nil {
			t.Fatalf("OpenSandboxFile(%q): %v", entry.Path, err)
		}
		content, readErr := io.ReadAll(file)
		_ = file.Close()
		if readErr != nil || string(content) != entry.Name {
			t.Fatalf("OpenSandboxFile(%q) content = %q err=%v, want %q", entry.Path, content, readErr, entry.Name)
		}
	}
}

func TestWorkspaceFilesListHasNoArbitraryPageDepthCap(t *testing.T) {
	workspacePath := filepath.Join(t.TempDir(), "sandbox", "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "report.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := NewWorkspaceFiles(&appconfig.Config{RuntimeDriver: "docker"})
	entries, total, err := files.ListSandboxFiles(context.Background(), workspaceFilesSandbox(workspacePath), "", 50_001, 1)
	if err != nil {
		t.Fatalf("deep offset must return an empty page, got error: %v", err)
	}
	if total != 1 || len(entries) != 0 {
		t.Fatalf("deep offset = %+v total=%d, want empty page total=1", entries, total)
	}
}

func TestWorkspaceFilesReadErrorClassification(t *testing.T) {
	if err := sandboxWorkspaceReadError(os.ErrPermission, "read workspace"); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("permission error = %v, want failed precondition", err)
	}
	if err := sandboxWorkspaceReadError(os.ErrNotExist, "read workspace"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing error = %v, want not found", err)
	}
	files := NewWorkspaceFiles(&appconfig.Config{RuntimeDriver: "docker"})
	sandbox := workspaceFilesSandbox(filepath.Join(t.TempDir(), "sandbox", "workspace"))
	sandbox.Summary.WorkspacePath = ""
	if _, _, err := files.ListSandboxFiles(context.Background(), sandbox, "", 0, 10); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("empty workspace path error = %v, want failed precondition", err)
	}
}

func TestWorkspaceFilesWorkspaceTypeGuards(t *testing.T) {
	workspacePath := filepath.Join(t.TempDir(), "sandbox", "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "report.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := NewWorkspaceFiles(&appconfig.Config{RuntimeDriver: "docker"})

	gitWorkspace := workspaceFilesSandbox(workspacePath)
	gitWorkspace.Workspace = &domain.SandboxWorkspace{
		Type:       "git",
		ConfigJSON: `{"provider":"git","url":"https://example.com/repo.git","target":"."}`,
	}
	entries, total, err := files.ListSandboxFiles(context.Background(), gitWorkspace, "", 0, 10)
	if err != nil {
		t.Fatalf("git workspace listing: %v", err)
	}
	if total != 1 || len(entries) != 1 || entries[0].Name != "report.md" {
		t.Fatalf("git workspace entries = %+v total=%d", entries, total)
	}

	unknown := workspaceFilesSandbox(workspacePath)
	unknown.Workspace = &domain.SandboxWorkspace{Type: "future-provider", ConfigJSON: `{}`}
	if _, _, err := files.ListSandboxFiles(context.Background(), unknown, "", 0, 10); !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("unknown workspace type error = %v, want unsupported", err)
	}
}

func TestWorkspaceFilesRejectsUnsafePathsAndStates(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "sandbox", "workspace")
	if err := os.MkdirAll(filepath.Join(workspacePath, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "report.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("reports", filepath.Join(workspacePath, "reports-link")); err != nil {
		t.Fatal(err)
	}
	files := NewWorkspaceFiles(&appconfig.Config{RuntimeDriver: "docker"})
	sandbox := workspaceFilesSandbox(workspacePath)

	for _, path := range []string{"../outside", "/etc/passwd", "reports/../../outside", "bad\x00path"} {
		if _, _, err := files.ListSandboxFiles(context.Background(), sandbox, path, 0, 10); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Errorf("ListSandboxFiles(%q) error = %v, want invalid argument", path, err)
		}
	}
	if _, _, err := files.ListSandboxFiles(context.Background(), sandbox, "reports-link", 0, 10); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("symlink directory error = %v, want invalid argument", err)
	}
	if _, _, err := files.ListSandboxFiles(context.Background(), sandbox, "reports-link/nested", 0, 10); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("intermediate symlink listing error = %v, want invalid argument", err)
	}
	if _, _, err := files.OpenSandboxFile(context.Background(), sandbox, "reports-link/nested"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("intermediate symlink download error = %v, want invalid argument", err)
	}
	if _, _, err := files.ListSandboxFiles(context.Background(), sandbox, "missing", 0, 10); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing directory error = %v, want not found", err)
	}
	if _, _, err := files.OpenSandboxFile(context.Background(), sandbox, "reports"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("directory download error = %v, want invalid argument", err)
	}
	if _, _, err := files.OpenSandboxFile(context.Background(), sandbox, "reports-link"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Errorf("symlink directory download error = %v, want invalid argument", err)
	}

	reclaimed := workspaceFilesSandbox(workspacePath)
	reclaimed.WorkspaceReclamation = &domain.SandboxWorkspaceReclamation{State: domain.SandboxWorkspaceReclamationStateReclaimed}
	if _, _, err := files.ListSandboxFiles(context.Background(), reclaimed, "", 0, 10); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Errorf("reclaimed error = %v, want failed precondition", err)
	}

	k8s := workspaceFilesSandbox(workspacePath)
	k8s.Summary.Driver = "k8s"
	if _, _, err := files.ListSandboxFiles(context.Background(), k8s, "", 0, 10); !errors.Is(err, domain.ErrUnsupported) {
		t.Errorf("k8s error = %v, want unsupported", err)
	}

	mounted := workspaceFilesSandbox(workspacePath)
	mounted.Workspace = &domain.SandboxWorkspace{
		Type:       "file",
		ConfigJSON: `{"mode":"mount","source_path":"/tmp/source","project_root":"/tmp","target":"inputs"}`,
	}
	if _, _, err := files.ListSandboxFiles(context.Background(), mounted, "", 0, 10); !errors.Is(err, domain.ErrUnsupported) {
		t.Errorf("mount mode error = %v, want unsupported", err)
	}

	pending := workspaceFilesSandbox(workspacePath)
	pending.Workspace = &domain.SandboxWorkspace{ID: "ws", Type: "file"}
	pending.WorkspaceProvisioning = &domain.SandboxWorkspaceProvisioning{Status: domain.SandboxWorkspaceProvisioningStatusPending}
	if _, _, err := files.ListSandboxFiles(context.Background(), pending, "", 0, 10); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Errorf("pending workspace error = %v, want failed precondition", err)
	}
}

func workspaceFilesSandbox(workspacePath string) *domain.Sandbox {
	return &domain.Sandbox{Summary: domain.SandboxSummary{
		ID:            "sandbox-files",
		Driver:        "docker",
		WorkspacePath: workspacePath,
	}}
}

func entryNames(entries []workspaces.SandboxEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return names
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func TestWorkspaceFilesRejectsSymlinkFileDownload(t *testing.T) {
	workspacePath := filepath.Join(t.TempDir(), "sandbox", "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "report.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("report.md", filepath.Join(workspacePath, "file-link")); err != nil {
		t.Skipf("filesystem does not support symlinks: %v", err)
	}
	files := NewWorkspaceFiles(&appconfig.Config{RuntimeDriver: "docker"})
	// The final component is a symlink to a regular file: it must be rejected
	// like a symlinked directory, not followed.
	if _, _, err := files.OpenSandboxFile(context.Background(), workspaceFilesSandbox(workspacePath), "file-link"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("symlink file download error = %v, want invalid argument", err)
	}
}

func TestWorkspaceFilesOmitsNonUTF8Names(t *testing.T) {
	workspacePath := filepath.Join(t.TempDir(), "sandbox", "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "good.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	badName := string([]byte{'b', 'a', 'd', 0xff, '.', 't', 'x', 't'})
	if err := os.WriteFile(filepath.Join(workspacePath, badName), []byte("bad"), 0o644); err != nil {
		t.Skipf("filesystem does not allow a non-UTF-8 name: %v", err)
	}
	files := NewWorkspaceFiles(&appconfig.Config{RuntimeDriver: "docker"})
	entries, total, err := files.ListSandboxFiles(context.Background(), workspaceFilesSandbox(workspacePath), "", 0, 10)
	if err != nil {
		t.Fatalf("ListSandboxFiles: %v", err)
	}
	if total != 1 || len(entries) != 1 || entries[0].Name != "good.txt" {
		t.Fatalf("entries = %+v total=%d, want only good.txt total=1", entries, total)
	}
}

func TestWorkspaceFilesClassifiesFIFOAsOther(t *testing.T) {
	workspacePath := filepath.Join(t.TempDir(), "sandbox", "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(workspacePath, "pipe"), 0o600); err != nil {
		t.Skipf("filesystem does not support FIFO: %v", err)
	}
	files := NewWorkspaceFiles(&appconfig.Config{RuntimeDriver: "docker"})
	sandbox := workspaceFilesSandbox(workspacePath)

	entries, total, err := files.ListSandboxFiles(context.Background(), sandbox, "", 0, 10)
	if err != nil {
		t.Fatalf("ListSandboxFiles: %v", err)
	}
	if total != 1 || len(entries) != 1 || entries[0].Kind != workspaces.SandboxEntryOther {
		t.Fatalf("entries = %+v total=%d, want one OTHER entry", entries, total)
	}
	if _, _, err := files.OpenSandboxFile(context.Background(), sandbox, "pipe"); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("FIFO download error = %v, want invalid argument", err)
	}
}

func TestWorkspaceFilesListPaginationBoundary(t *testing.T) {
	workspacePath := filepath.Join(t.TempDir(), "sandbox", "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(workspacePath, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := NewWorkspaceFiles(&appconfig.Config{RuntimeDriver: "docker"})
	sandbox := workspaceFilesSandbox(workspacePath)

	entries, total, err := files.ListSandboxFiles(context.Background(), sandbox, "", 2, 1)
	if err != nil {
		t.Fatalf("ListSandboxFiles: %v", err)
	}
	if total != 3 || len(entries) != 1 || entries[0].Name != "c" {
		t.Fatalf("last page = %+v total=%d, want [c] total=3", entries, total)
	}

	entries, total, err = files.ListSandboxFiles(context.Background(), sandbox, "", 3, 1)
	if err != nil {
		t.Fatalf("ListSandboxFiles: %v", err)
	}
	if total != 3 || len(entries) != 0 {
		t.Fatalf("past-end page = %+v total=%d, want empty total=3", entries, total)
	}
}

func TestWorkspaceFilesRejectsUnsafeRoots(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "sandbox", "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	files := NewWorkspaceFiles(&appconfig.Config{RuntimeDriver: "docker"})

	t.Run("basename mismatch", func(t *testing.T) {
		sandbox := workspaceFilesSandbox(filepath.Join(root, "sandbox", "not-workspace"))
		if _, _, err := files.ListSandboxFiles(context.Background(), sandbox, "", 0, 10); !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("basename mismatch error = %v, want failed precondition", err)
		}
	})

	t.Run("root missing", func(t *testing.T) {
		sandbox := workspaceFilesSandbox(filepath.Join(root, "sandbox", "workspace-missing"))
		if _, _, err := files.ListSandboxFiles(context.Background(), sandbox, "", 0, 10); !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("missing root error = %v, want failed precondition", err)
		}
	})

	t.Run("root is symlink", func(t *testing.T) {
		alias := filepath.Join(root, "alias")
		if err := os.MkdirAll(alias, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(alias, "workspace")
		if err := os.Symlink(workspacePath, link); err != nil {
			t.Skipf("filesystem does not support symlinks: %v", err)
		}
		sandbox := workspaceFilesSandbox(link)
		if _, _, err := files.ListSandboxFiles(context.Background(), sandbox, "", 0, 10); !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("symlink root error = %v, want failed precondition", err)
		}
	})
}

func TestWorkspaceFilesVolumeMountInterference(t *testing.T) {
	files := NewWorkspaceFiles(&appconfig.Config{RuntimeDriver: "docker", GuestWorkspacePath: "/workspace"})
	cases := []struct {
		target string
		want   bool
	}{
		{"/workspace/cache", true},
		{"/workspace", true},
		{"/workspace/", true},
		{"/cache", false},
		{"/workspace-other", false},
	}
	for _, tc := range cases {
		sandbox := &domain.Sandbox{VolumeMounts: []domain.SandboxVolumeMount{{Target: tc.target}}}
		if got := files.sandboxWorkspaceVolumeMountInterferes(sandbox); got != tc.want {
			t.Errorf("target %q = %v, want %v", tc.target, got, tc.want)
		}
	}
	if files.sandboxWorkspaceVolumeMountInterferes(&domain.Sandbox{}) {
		t.Errorf("empty volume mounts must not interfere")
	}
}

func TestWorkspaceFilesRejectsVolumeMountInWorkspace(t *testing.T) {
	workspacePath := filepath.Join(t.TempDir(), "sandbox", "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "report.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := NewWorkspaceFiles(&appconfig.Config{RuntimeDriver: "docker", GuestWorkspacePath: "/workspace"})

	interfering := workspaceFilesSandbox(workspacePath)
	interfering.VolumeMounts = []domain.SandboxVolumeMount{{Type: "bind", Source: "cache", Target: "/workspace/cache"}}
	if _, _, err := files.ListSandboxFiles(context.Background(), interfering, "", 0, 10); !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("interfering volume mount error = %v, want unsupported", err)
	}

	outside := workspaceFilesSandbox(workspacePath)
	outside.VolumeMounts = []domain.SandboxVolumeMount{{Type: "bind", Source: "cache", Target: "/cache"}}
	if _, _, err := files.ListSandboxFiles(context.Background(), outside, "", 0, 10); err != nil {
		t.Fatalf("non-interfering volume mount error = %v, want nil", err)
	}
}
