package workspaces

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// RemoveOwnedDirectory removes a disposable directory whose ownership has
// already been established by its caller. Copied directories may be read-only;
// only directories missing owner rwx in this owned tree receive those bits.
// Symlinks are removed as entries and are never followed.
func RemoveOwnedDirectory(path string) error {
	parent, err := os.OpenRoot(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	return removeOwnedRootDirectory(parent, filepath.Base(path))
}

func removeOwnedRootDirectory(root *os.Root, path string) error {
	// Use existing permissions first: even a foreign empty 0000 directory can
	// be removed through its writable parent, and writable foreign trees need
	// no ownership change. Only copied read-only trees need permission repair.
	if err := root.RemoveAll(path); err == nil || !errors.Is(err, os.ErrPermission) {
		return err
	}
	info, err := root.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return root.Remove(path)
	}
	// Guest-created directories may belong to a different UID. Removing their
	// entries can already be permitted even though changing their mode is not.
	if info.Mode().Perm()&0o700 != 0o700 {
		if err := root.Chmod(path, info.Mode().Perm()|0o700); err != nil {
			return err
		}
	}
	directory, err := root.OpenRoot(path)
	if err != nil {
		return err
	}
	entries, readErr := fs.ReadDir(directory.FS(), ".")
	if readErr != nil {
		_ = directory.Close()
		return readErr
	}
	for _, entry := range entries {
		if err := removeOwnedRootDirectory(directory, entry.Name()); err != nil {
			_ = directory.Close()
			return err
		}
	}
	if err := directory.Close(); err != nil {
		return err
	}
	return root.Remove(path)
}
