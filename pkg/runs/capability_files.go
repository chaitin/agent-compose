package runs

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// Publish within the sandbox root and rename a complete snapshot into place.
// The guest can replace either mpi or the destination with a symlink.
func publishCapabilityFile(filePath string, content []byte) (returnErr error) {
	runtimeDir := filepath.Dir(filepath.Dir(filePath))
	sandboxDir := filepath.Dir(runtimeDir)
	// The daemon-owned sandbox directory may precede materialization. Only
	// bootstrap that trusted path; all guest-controlled children stay in Root.
	if err := os.MkdirAll(sandboxDir, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(sandboxDir)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	relative := filepath.Join(filepath.Base(runtimeDir), "mpi")
	if err := root.MkdirAll(relative, 0o755); err != nil {
		return err
	}
	dir, err := root.OpenRoot(relative)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, dir.Close()) }()
	temp := ".capabilities-" + uuid.NewString() + ".tmp"
	file, err := dir.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if file != nil {
			returnErr = errors.Join(returnErr, file.Close())
		}
		if err := dir.Remove(temp); !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, err)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	err = file.Close()
	file = nil
	if err != nil {
		return err
	}
	return dir.Rename(temp, filepath.Base(filePath))
}
