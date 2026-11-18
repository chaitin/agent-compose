package execution

import (
	"errors"
	"fmt"
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
	if _, err := handle.Write(data); err != nil {
		_ = handle.Close()
		return err
	}
	return handle.Close()
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
