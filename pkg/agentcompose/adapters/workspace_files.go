package adapters

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

const (
	maxSandboxWorkspacePathBytes = 4096
	sandboxWorkspaceRootName     = "workspace"
	sandboxWorkspaceListBatch    = 4096

	// sandboxWorkspaceOpenFlags opens read-only without blocking. O_NONBLOCK
	// makes opening a FIFO or device return immediately instead of blocking the
	// handler, since a guest can race a path into one between the Lstat check
	// and the open. The descriptor type is re-verified after every open.
	sandboxWorkspaceOpenFlags = os.O_RDONLY | syscall.O_NONBLOCK
)

// WorkspaceFiles is the shared-filesystem implementation of
// workspaces.SandboxFiles. It reads the sandbox-owned workspace directory that
// the daemon already has on disk. It never starts or resumes a runtime.
//
// Only drivers whose sandbox workspace is a host directory readable by the
// daemon are supported. K8s and every other driver fail closed until an
// implementation reads the guest filesystem instead; deployed workspace mounts
// are rejected explicitly as well.
type WorkspaceFiles struct {
	config *appconfig.Config
}

func NewWorkspaceFiles(config *appconfig.Config) *WorkspaceFiles {
	return &WorkspaceFiles{config: config}
}

func (w *WorkspaceFiles) ListSandboxFiles(
	ctx context.Context,
	sandbox *domain.Sandbox,
	rawPath string,
	offset, limit int,
) ([]workspaces.SandboxEntry, int, error) {
	if limit <= 0 {
		return nil, 0, domain.ClassifyError(domain.ErrInvalidArgument, "workspace list limit must be positive", nil)
	}
	if offset < 0 {
		return nil, 0, domain.ClassifyError(domain.ErrInvalidArgument, "workspace list offset must not be negative", nil)
	}
	pageDepth := offset + limit
	if pageDepth < limit {
		return nil, 0, domain.ClassifyError(domain.ErrInvalidArgument, "workspace list offset+limit overflows", nil)
	}
	rootPath, err := w.sandboxWorkspaceRoot(sandbox)
	if err != nil {
		return nil, 0, err
	}
	relPath, err := cleanSandboxWorkspacePath(rawPath, true)
	if err != nil {
		return nil, 0, err
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, 0, sandboxWorkspaceReadError(err, "open workspace root")
	}
	defer func() { _ = root.Close() }()

	var lstatInfo os.FileInfo
	if relPath != "" {
		if err := sandboxWorkspaceRejectSymlinkComponents(root, relPath); err != nil {
			return nil, 0, err
		}
		info, err := root.Lstat(relPath)
		if err != nil {
			return nil, 0, sandboxWorkspaceReadError(err, "inspect workspace directory")
		}
		if !info.IsDir() {
			return nil, 0, domain.ClassifyError(domain.ErrInvalidArgument, fmt.Sprintf("workspace path %q is not a directory", relPath), nil)
		}
		lstatInfo = info
	}

	openPath := relPath
	if openPath == "" {
		openPath = "."
	}
	dir, err := root.OpenFile(openPath, sandboxWorkspaceOpenFlags, 0)
	if err != nil {
		return nil, 0, sandboxWorkspaceReadError(err, "open workspace directory")
	}
	defer func() { _ = dir.Close() }()

	// The guest writes the workspace concurrently, so the Lstat above can be
	// stale by the time the directory is opened. Re-verify the opened handle is
	// the same directory we inspected: a swap to a symlink would otherwise be
	// followed inside the root, and a swap to a FIFO would have blocked without
	// O_NONBLOCK.
	dirInfo, err := dir.Stat()
	if err != nil {
		return nil, 0, sandboxWorkspaceReadError(err, "stat workspace directory")
	}
	if !dirInfo.IsDir() || (relPath != "" && !os.SameFile(dirInfo, lstatInfo)) {
		return nil, 0, domain.ClassifyError(domain.ErrInvalidArgument, fmt.Sprintf("workspace path %q changed while opening", relPath), nil)
	}

	// Count the directory while retaining only the smallest offset+limit
	// names. Memory is bounded by min(directory entries, offset+limit): a
	// typical page keeps only offset+limit names, but a very deep offset can
	// still retain the whole directory. This matches the offset/limit/total
	// contract the other list RPCs share, which already materialize the full
	// result in memory.
	selected := make(sandboxDirEntryHeap, 0)
	total := 0
	for {
		batch, readErr := dir.ReadDir(sandboxWorkspaceListBatch)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, 0, sandboxWorkspaceReadError(readErr, "read workspace directory")
		}
		if len(batch) == 0 && readErr == nil {
			// A reader that returns no progress would otherwise loop forever.
			break
		}
		for _, entry := range batch {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
			// protobuf strings are UTF-8. A filesystem can contain names that
			// are not; they are omitted rather than sent as a corrupted path.
			if !utf8.ValidString(entry.Name()) {
				continue
			}
			total++
			if len(selected) < pageDepth {
				heap.Push(&selected, entry)
				continue
			}
			if entry.Name() < selected[0].Name() {
				heap.Pop(&selected)
				heap.Push(&selected, entry)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if offset >= len(selected) {
		return nil, total, nil
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name() < selected[j].Name() })
	end := min(offset+limit, len(selected))
	page := selected[offset:end]
	entries := make([]workspaces.SandboxEntry, 0, len(page))
	for _, entry := range page {
		info, err := entry.Info()
		if err != nil {
			// A directory opened through os.Root fills each entry's info at
			// readdir time, so Info normally returns that snapshot and cannot
			// fail here. Keep the NotFound skip as a guard for a future
			// non-Root directory handle.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, 0, sandboxWorkspaceReadError(err, "inspect workspace entry")
		}
		entryPath := entry.Name()
		if relPath != "" {
			entryPath = path.Join(filepath.ToSlash(relPath), entry.Name())
		}
		kind, size := sandboxWorkspaceEntryKind(info)
		entries = append(entries, workspaces.SandboxEntry{
			Path:    entryPath,
			Name:    entry.Name(),
			Kind:    kind,
			Size:    size,
			ModTime: info.ModTime().UTC(),
		})
	}
	return entries, total, nil
}

// sandboxDirEntryHeap is a max-heap by name. Keeping the largest name at the
// root lets the scan replace it with a smaller name until only the smallest
// offset+limit entries remain.
type sandboxDirEntryHeap []os.DirEntry

func (h sandboxDirEntryHeap) Len() int           { return len(h) }
func (h sandboxDirEntryHeap) Less(i, j int) bool { return h[i].Name() > h[j].Name() }
func (h sandboxDirEntryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *sandboxDirEntryHeap) Push(value any)    { *h = append(*h, value.(os.DirEntry)) }
func (h *sandboxDirEntryHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	*h = old[:last]
	return value
}

// sandboxWorkspaceRejectSymlinkComponents rejects a path that traverses a
// symlink at any component. Checking only the final component would let a
// caller reach a regular file through a symlinked directory, which the wire
// contract says is never followed.
//
// This Lstat walk is the pre-open half of the defense. It is inherently racy
// because the guest can mutate the workspace concurrently, so every open is
// followed by an fstat + os.SameFile check that verifies the opened object is
// the one that was inspected here.
func sandboxWorkspaceRejectSymlinkComponents(root *os.Root, relPath string) error {
	current := ""
	for _, part := range strings.Split(relPath, "/") {
		if part == "" {
			continue
		}
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		info, err := root.Lstat(current)
		if err != nil {
			return sandboxWorkspaceReadError(err, "inspect workspace path")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return domain.ClassifyError(domain.ErrInvalidArgument, fmt.Sprintf("workspace path %q traverses a symlink", current), nil)
		}
		if current != relPath && !info.IsDir() {
			return domain.ClassifyError(domain.ErrInvalidArgument, fmt.Sprintf("workspace path %q is not a directory", current), nil)
		}
	}
	return nil
}

func (w *WorkspaceFiles) OpenSandboxFile(
	ctx context.Context,
	sandbox *domain.Sandbox,
	rawPath string,
) (io.ReadCloser, workspaces.SandboxFileInfo, error) {
	rootPath, err := w.sandboxWorkspaceRoot(sandbox)
	if err != nil {
		return nil, workspaces.SandboxFileInfo{}, err
	}
	relPath, err := cleanSandboxWorkspacePath(rawPath, false)
	if err != nil {
		return nil, workspaces.SandboxFileInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, workspaces.SandboxFileInfo{}, err
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, workspaces.SandboxFileInfo{}, sandboxWorkspaceReadError(err, "open workspace root")
	}
	if err := sandboxWorkspaceRejectSymlinkComponents(root, relPath); err != nil {
		_ = root.Close()
		return nil, workspaces.SandboxFileInfo{}, err
	}
	info, err := root.Lstat(relPath)
	if err != nil {
		_ = root.Close()
		return nil, workspaces.SandboxFileInfo{}, sandboxWorkspaceReadError(err, "inspect workspace file")
	}
	if info.IsDir() {
		_ = root.Close()
		return nil, workspaces.SandboxFileInfo{}, domain.ClassifyError(domain.ErrInvalidArgument, fmt.Sprintf("workspace path %q is a directory", relPath), nil)
	}
	if !info.Mode().IsRegular() {
		_ = root.Close()
		return nil, workspaces.SandboxFileInfo{}, domain.ClassifyError(domain.ErrInvalidArgument, fmt.Sprintf("workspace path %q is not a regular file", relPath), nil)
	}
	file, err := root.OpenFile(relPath, sandboxWorkspaceOpenFlags, 0)
	if err != nil {
		_ = root.Close()
		return nil, workspaces.SandboxFileInfo{}, sandboxWorkspaceReadError(err, "open workspace file")
	}
	// The guest writes the workspace concurrently, so the Lstat above can be
	// stale by the time the file is opened. Re-verify the opened descriptor is
	// the same regular file we inspected: a swap to a symlink would otherwise be
	// followed inside the root and return bytes that disagree with info, and a
	// swap to a FIFO would have blocked without O_NONBLOCK.
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		_ = root.Close()
		return nil, workspaces.SandboxFileInfo{}, sandboxWorkspaceReadError(err, "stat workspace file")
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(openedInfo, info) {
		_ = file.Close()
		_ = root.Close()
		return nil, workspaces.SandboxFileInfo{}, domain.ClassifyError(domain.ErrInvalidArgument, fmt.Sprintf("workspace path %q changed while opening", relPath), nil)
	}
	// The file owns the root handle: closing the file must also release the
	// root, and the directory entry remains valid after an unlink on Unix.
	return &sandboxWorkspaceFile{File: file, root: root}, workspaces.SandboxFileInfo{
		Path:    filepath.ToSlash(relPath),
		Size:    openedInfo.Size(),
		ModTime: openedInfo.ModTime().UTC(),
	}, nil
}

type sandboxWorkspaceFile struct {
	*os.File
	root *os.Root
}

func (f *sandboxWorkspaceFile) Close() error {
	fileErr := f.File.Close()
	rootErr := f.root.Close()
	if fileErr != nil {
		return fileErr
	}
	return rootErr
}

func (w *WorkspaceFiles) sandboxWorkspaceRoot(sandbox *domain.Sandbox) (string, error) {
	if sandbox == nil {
		return "", fmt.Errorf("sandbox is nil")
	}
	if domain.SandboxWorkspaceUnavailable(sandbox) {
		return "", domain.ClassifyError(domain.ErrFailedPrecondition, "sandbox workspace is unavailable", nil)
	}
	if sandbox.WorkspaceProvisioning != nil &&
		sandbox.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
		return "", domain.ClassifyError(domain.ErrFailedPrecondition, "sandbox workspace is not ready", nil)
	}
	if sandbox.Workspace != nil {
		switch strings.ToLower(strings.TrimSpace(sandbox.Workspace.Type)) {
		case "git", "http":
			// Git and HTTP providers materialize into the owned workspace;
			// their ConfigJSON describes a source, not a file mount.
		case "file", "":
			mount, err := workspaces.DecodeFileWorkspaceMount(sandbox.Workspace.ConfigJSON)
			if err != nil {
				return "", domain.ClassifyError(domain.ErrFailedPrecondition, "decode sandbox workspace delivery", err)
			}
			if mount != nil {
				return "", domain.ClassifyError(domain.ErrUnsupported, "workspace mount mode is not supported by the workspace file API", nil)
			}
		default:
			return "", domain.ClassifyError(domain.ErrUnsupported, fmt.Sprintf("workspace type %q is not supported by the workspace file API", sandbox.Workspace.Type), nil)
		}
	}
	driver, err := w.resolveDriver(sandbox)
	if err != nil {
		return "", err
	}
	if !sandboxWorkspaceDriverSupported(driver) {
		return "", domain.ClassifyError(domain.ErrUnsupported, fmt.Sprintf("workspace file reads are not supported by driver %q", driver), nil)
	}
	if w.sandboxWorkspaceVolumeMountInterferes(sandbox) {
		return "", domain.ClassifyError(domain.ErrUnsupported, "sandbox volume mounts overlap the workspace and are not readable by the workspace file API", nil)
	}
	workspacePath := strings.TrimSpace(sandbox.Summary.WorkspacePath)
	if workspacePath == "" {
		return "", domain.ClassifyError(domain.ErrFailedPrecondition, "sandbox workspace path is required", nil)
	}
	if filepath.Base(workspacePath) != sandboxWorkspaceRootName {
		return "", domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("sandbox workspace path %q is not a workspace root", workspacePath), nil)
	}
	absolute, err := filepath.Abs(workspacePath)
	if err != nil {
		return "", domain.ClassifyError(domain.ErrFailedPrecondition, "resolve sandbox workspace path", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", domain.ClassifyError(domain.ErrFailedPrecondition, "sandbox workspace is not available", err)
		}
		return "", sandboxWorkspaceReadError(err, "inspect sandbox workspace")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", domain.ClassifyError(domain.ErrFailedPrecondition, "sandbox workspace is a symlink", nil)
	}
	if !info.IsDir() {
		return "", domain.ClassifyError(domain.ErrFailedPrecondition, "sandbox workspace is not a directory", nil)
	}
	return absolute, nil
}

func (w *WorkspaceFiles) resolveDriver(sandbox *domain.Sandbox) (string, error) {
	defaultDriver := ""
	if w.config != nil {
		defaultDriver = w.config.RuntimeDriver
	}
	driver, err := driverpkg.ResolveSandboxRuntimeDriver(sandbox.Summary.Driver, defaultDriver)
	if err != nil {
		return "", domain.ClassifyError(domain.ErrUnsupported, "resolve sandbox runtime driver", err)
	}
	return driver, nil
}

// sandboxWorkspaceVolumeMountInterferes reports whether any user volume is
// mounted inside the guest workspace. The daemon-owned <sandbox>/workspace
// directory does not contain a volume mounted over one of its subdirectories,
// so serving that subtree would return the wrong (host-side) content. Fail
// closed with Unimplemented, matching the mount-mode workspace guard, instead
// of silently reading stale bytes.
func (w *WorkspaceFiles) sandboxWorkspaceVolumeMountInterferes(sandbox *domain.Sandbox) bool {
	if sandbox == nil || len(sandbox.VolumeMounts) == 0 {
		return false
	}
	// guestWorkspace mirrors the config default of /workspace; the real daemon
	// config always carries a normalized GuestWorkspacePath, so the fallback
	// only serves nil/zero configs in tests.
	guestWorkspace := "/workspace"
	if w.config != nil {
		if trimmed := strings.TrimSpace(w.config.GuestWorkspacePath); trimmed != "" {
			guestWorkspace = path.Clean(trimmed)
		}
	}
	for _, mount := range sandbox.VolumeMounts {
		target := path.Clean(strings.TrimSpace(mount.Target))
		if target == "" || target == "." {
			continue
		}
		if target == guestWorkspace || strings.HasPrefix(target, guestWorkspace+"/") {
			return true
		}
	}
	return false
}

// sandboxWorkspaceDriverSupported is an explicit allowlist, not a "not k8s"
// check. A new driver must prove that its workspace is a host directory the
// daemon can read before this API serves it; otherwise it would silently list
// or download stale host-side content instead of reporting UNIMPLEMENTED.
func sandboxWorkspaceDriverSupported(driver string) bool {
	switch driver {
	case driverpkg.RuntimeDriverDocker,
		driverpkg.RuntimeDriverBoxlite,
		driverpkg.RuntimeDriverMicrosandbox:
		return true
	default:
		return false
	}
}

// cleanSandboxWorkspacePath normalizes a slash-separated workspace-relative
// path without trimming it. File names may legally begin or end with spaces or
// contain a backslash on Unix, and List returns them verbatim; trimming or
// rejecting those characters here would make a listed path undownloadable or,
// worse, open a different file.
func cleanSandboxWorkspacePath(raw string, allowEmpty bool) (string, error) {
	if len(raw) > maxSandboxWorkspacePathBytes {
		return "", domain.ClassifyError(domain.ErrInvalidArgument, "workspace path is too long", nil)
	}
	if strings.ContainsRune(raw, 0) {
		return "", domain.ClassifyError(domain.ErrInvalidArgument, "workspace path contains NUL", nil)
	}
	if path.IsAbs(raw) || filepath.IsAbs(raw) {
		return "", domain.ClassifyError(domain.ErrInvalidArgument, fmt.Sprintf("workspace path %q must be relative", raw), nil)
	}
	if raw == "" || raw == "." {
		if allowEmpty {
			return "", nil
		}
		return "", domain.ClassifyError(domain.ErrRequired, "workspace path is required", nil)
	}
	cleaned := path.Clean(raw)
	if cleaned == "." {
		if allowEmpty {
			return "", nil
		}
		return "", domain.ClassifyError(domain.ErrRequired, "workspace path is required", nil)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", domain.ClassifyError(domain.ErrInvalidArgument, fmt.Sprintf("workspace path %q escapes workspace root", raw), nil)
	}
	return cleaned, nil
}

func sandboxWorkspaceEntryKind(info os.FileInfo) (workspaces.SandboxEntryKind, int64) {
	mode := info.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		return workspaces.SandboxEntrySymlink, 0
	case mode.IsDir():
		return workspaces.SandboxEntryDirectory, 0
	case mode.IsRegular():
		return workspaces.SandboxEntryFile, info.Size()
	default:
		return workspaces.SandboxEntryOther, 0
	}
}

func sandboxWorkspaceReadError(err error, operation string) error {
	if errors.Is(err, os.ErrNotExist) {
		return domain.ClassifyError(domain.ErrNotFound, operation, err)
	}
	if errors.Is(err, os.ErrPermission) {
		return domain.ClassifyError(domain.ErrFailedPrecondition, operation+": permission denied", err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
