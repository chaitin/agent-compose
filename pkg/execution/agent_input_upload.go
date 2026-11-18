package execution

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// writeAgentInputUpload writes data to relativePath under workspaceRoot without
// following a workspace symlink. An agent can rename .agent-compose and replace
// it with a link between turns, so the daemon must not create upload
// directories through a path an earlier turn planted.
//
// The write is anchored with os.Root: even if a component becomes a symlink
// after the component check, the kernel keeps the file inside the workspace.
// O_EXCL keeps the final name from overwriting or following an existing entry.
func writeAgentInputUpload(workspaceRoot, relativePath string, data []byte) error {
	return createAgentInputUpload(workspaceRoot, relativePath, func(handle *os.File) error {
		_, err := handle.Write(data)
		return err
	})
}

// copyAgentInputUpload copies a staged upload into the workspace with the same
// anchoring as writeAgentInputUpload. The copy is a new inode, so the agent can
// change its file without touching the staged object other runs may share.
// On Linux, io.Copy between files uses copy_file_range, which shares extents
// on filesystems that support reflinks.
//
// The staged object was verified when it was uploaded and is read-only in a
// daemon-owned directory; the copy checks the size, not the digest again.
func copyAgentInputUpload(workspaceRoot, relativePath, stagedPath string, size int64) error {
	source, err := os.Open(stagedPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("upload expired before it reached the workspace; upload it again")
		}
		return fmt.Errorf("open staged upload: %w", err)
	}
	defer func() { _ = source.Close() }()
	return createAgentInputUpload(workspaceRoot, relativePath, func(handle *os.File) error {
		copied, err := io.Copy(handle, source)
		if err != nil {
			return err
		}
		if copied != size {
			return fmt.Errorf("staged upload is %d bytes, expected %d", copied, size)
		}
		return nil
	})
}

// createAgentInputUpload creates relativePath under workspaceRoot through an
// os.Root anchor and lets fill write its content. A failed fill removes the
// partial file so the turn leaves nothing half-written behind.
func createAgentInputUpload(workspaceRoot, relativePath string, fill func(*os.File) error) error {
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		return fmt.Errorf("create sandbox workspace: %w", err)
	}
	root, err := os.OpenRoot(workspaceRoot)
	if err != nil {
		return fmt.Errorf("open sandbox workspace: %w", err)
	}
	// The anchor only scopes the operations below; the file handle owns its
	// own close error and the written bytes are already durable.
	defer func() { _ = root.Close() }()
	if dir := filepath.Dir(relativePath); dir != "." {
		if err := mkdirAgentInputDir(root, dir); err != nil {
			return err
		}
	}
	handle, err := root.OpenFile(relativePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := fill(handle); err != nil {
		_ = handle.Close()
		_ = root.Remove(relativePath)
		return err
	}
	if err := handle.Close(); err != nil {
		_ = root.Remove(relativePath)
		return err
	}
	return nil
}

// mkdirAgentInputDir creates every missing component under root and rejects an
// existing symlink instead of following it.
func mkdirAgentInputDir(root *os.Root, relative string) error {
	current := ""
	for _, segment := range strings.Split(filepath.ToSlash(relative), "/") {
		current = filepath.Join(current, segment)
		info, err := root.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("agent input directory %q is a symlink", current)
			}
			if !info.IsDir() {
				return fmt.Errorf("agent input directory %q is not a directory", current)
			}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("inspect agent input directory %q: %w", current, err)
		}
		if err := root.Mkdir(current, 0o755); err != nil {
			return fmt.Errorf("create agent input directory %q: %w", current, err)
		}
	}
	return nil
}
