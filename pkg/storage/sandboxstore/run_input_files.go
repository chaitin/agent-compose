package sandboxstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// RunInputWriter owns temporary inodes inside one sandbox's private workspace.
// Publication uses hard links in that same filesystem and never overwrites.
type RunInputWriter struct {
	root   *os.Root
	files  []inputFileHandle
	retain bool
}

type inputFileHandle struct {
	declaration domain.RunInputFile
	name        string
	file        *os.File
	info        os.FileInfo
	hash        hash.Hash
	received    uint64
}

// OpenRunInput rejects external/mounted workspaces before creating any bytes.
func (s *Store) OpenRunInput(ctx context.Context, sandbox *domain.Sandbox, input domain.RunInputPreparation) (*RunInputWriter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch sandbox.Summary.Driver {
	case "docker", "boxlite", "microsandbox":
	default:
		return nil, fmt.Errorf("%w: file input driver", domain.ErrUnsupported)
	}
	if sandbox.Workspace != nil {
		var config struct {
			Mode string `json:"mode"`
		}
		if err := json.Unmarshal([]byte(sandbox.Workspace.ConfigJSON), &config); err != nil {
			return nil, fmt.Errorf("decode workspace mode: %w", err)
		}
		mode, err := domain.NormalizeWorkspaceMode(config.Mode)
		if err != nil || mode == domain.WorkspaceModeMount {
			return nil, fmt.Errorf("%w: file input requires copy workspace", domain.ErrUnsupported)
		}
	}
	expected := filepath.Join(s.SandboxDir(sandbox.Summary.ID), "workspace")
	if filepath.Clean(sandbox.Summary.WorkspacePath) != expected {
		return nil, fmt.Errorf("%w: workspace is not sandbox-owned", domain.ErrInvalidArgument)
	}
	// Conservatively reject any volume overlay, including a workspace-root mount.
	// A root-level file could otherwise resolve differently inside the guest.
	for _, mount := range sandbox.VolumeMounts {
		target, workspace := filepath.Clean(mount.Target), filepath.Clean(s.config.GuestWorkspacePath)
		if target == "/" || target == workspace || strings.HasPrefix(target, workspace+"/") || strings.HasPrefix(workspace, target+"/") {
			return nil, fmt.Errorf("%w: file input conflicts with workspace volume", domain.ErrUnsupported)
		}
	}
	parent, err := os.OpenRoot(s.SandboxDir(sandbox.Summary.ID))
	if err != nil {
		return nil, err
	}
	info, statErr := parent.Lstat("workspace")
	if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(fmt.Errorf("%w: private workspace directory required", domain.ErrInvalidArgument), statErr, parent.Close())
	}
	root, err := parent.OpenRoot("workspace")
	closeErr := parent.Close()
	if err != nil {
		return nil, errors.Join(err, closeErr)
	}
	if closeErr != nil {
		return nil, errors.Join(closeErr, root.Close())
	}
	writer := &RunInputWriter{root: root}
	if len(input.TemporaryNames) != len(input.Files) {
		return nil, errors.Join(fmt.Errorf("%w: persisted temporary names required", domain.ErrInvalidArgument), writer.Close())
	}
	for index, declaration := range input.Files {
		name := input.TemporaryNames[index]
		if !strings.HasPrefix(name, ".ac-upload-") || filepath.Base(name) != name {
			return nil, errors.Join(fmt.Errorf("%w: invalid temporary name", domain.ErrInvalidArgument), writer.Close())
		}
		file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err != nil {
			return nil, errors.Join(err, writer.Close())
		}
		info, err := file.Stat()
		if err != nil {
			return nil, errors.Join(err, file.Close(), writer.Close())
		}
		writer.files = append(writer.files, inputFileHandle{declaration: declaration, name: name, file: file, info: info, hash: sha256.New()})
	}
	return writer, nil
}

func (w *RunInputWriter) TemporaryNames() []string {
	result := make([]string, len(w.files))
	for i := range w.files {
		result[i] = w.files[i].name
	}
	return result
}

// Write accepts contiguous bytes or an identical, completely received range.
func (w *RunInputWriter) Write(ctx context.Context, id string, offset uint64, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for i := range w.files {
		file := &w.files[i]
		if file.declaration.ID != id {
			continue
		}
		if file.file == nil || offset > file.received || uint64(len(data)) > file.declaration.Size || offset > file.declaration.Size-uint64(len(data)) {
			return fmt.Errorf("%w: invalid file offset or size", domain.ErrInvalidArgument)
		}
		if offset < file.received {
			if uint64(len(data)) > file.received-offset {
				return fmt.Errorf("%w: overlapping file range", domain.ErrInvalidArgument)
			}
			prior := make([]byte, len(data))
			n, err := file.file.ReadAt(prior, int64(offset))
			if err != nil {
				return err
			}
			if n != len(data) || !bytes.Equal(prior, data) {
				return fmt.Errorf("%w: conflicting duplicate file bytes", domain.ErrConflict)
			}
			return nil
		}
		n, err := file.file.Write(data)
		if err != nil {
			return err
		}
		if n != len(data) {
			return io.ErrShortWrite
		}
		if _, err := file.hash.Write(data); err != nil {
			return err
		}
		file.received += uint64(n)
		return nil
	}
	return fmt.Errorf("%w: undeclared input file", domain.ErrInvalidArgument)
}

// Seal validates the entire batch before any filename is published.
func (w *RunInputWriter) Seal(ctx context.Context) error {
	for i := range w.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		file := &w.files[i]
		if file.received != file.declaration.Size || !bytes.Equal(file.hash.Sum(nil), file.declaration.SHA256) {
			return fmt.Errorf("%w: input size or digest mismatch", domain.ErrInvalidArgument)
		}
		if err := file.file.Chmod(0o644); err != nil {
			return err
		}
		if err := file.file.Sync(); err != nil {
			return err
		}
		err := file.file.Close()
		file.file = nil
		if err != nil {
			return err
		}
	}
	return nil
}

// Publish must be preceded by durable publication intent. Once attempted,
// preserve temporary inodes on failure for reconciliation/Sandbox reclamation.
func (w *RunInputWriter) Publish(ctx context.Context, index int, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.retain = true
	file := &w.files[index]
	info, err := w.root.Lstat(file.name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, file.info) {
		return fmt.Errorf("%w: temporary file replaced", domain.ErrConflict)
	}
	if err := w.root.Link(file.name, name); err != nil {
		return err
	}
	published, err := w.root.Lstat(name)
	if err != nil {
		return err
	}
	if !published.Mode().IsRegular() || !os.SameFile(published, file.info) {
		return fmt.Errorf("%w: published file identity changed", domain.ErrConflict)
	}
	return nil
}

func (w *RunInputWriter) Sync() error {
	directory, err := w.root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

// Delivered retires temporary names only after READY was committed durably.
func (w *RunInputWriter) Delivered() error { w.retain = false; return w.Close() }

func (w *RunInputWriter) Close() error {
	if w.root == nil {
		return nil
	}
	var failures []error
	for i := range w.files {
		file := &w.files[i]
		if file.file != nil {
			failures = append(failures, file.file.Close())
			file.file = nil
		}
		if !w.retain {
			info, err := w.root.Lstat(file.name)
			if err != nil {
				failures = append(failures, err)
			} else if os.SameFile(info, file.info) {
				failures = append(failures, w.root.Remove(file.name))
			} else {
				failures = append(failures, fmt.Errorf("%w: temporary file identity changed", domain.ErrConflict))
			}
		}
	}
	failures = append(failures, w.root.Close())
	w.root = nil
	return errors.Join(failures...)
}
