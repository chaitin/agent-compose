package workspaces

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/google/uuid"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// Copies of internal preparation metadata share one idempotently closed lease.
type transientSnapshotLease struct {
	mu   sync.Mutex
	file *os.File
}

func (l *transientSnapshotLease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	return file.Close()
}

func CloseTransientSnapshotLease(workspace *domain.SandboxWorkspace) error {
	if workspace == nil || workspace.SnapshotLease == nil {
		return nil
	}
	return workspace.SnapshotLease.Close()
}

func acquireTransientSnapshotLease(root *os.Root, id string, create bool) (*transientSnapshotLease, bool, error) {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return nil, false, fmt.Errorf("invalid transient snapshot id %q", id)
	}
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE | os.O_EXCL
	}
	file, err := root.OpenFile(id+".lease", flags, 0o600)
	if !create && errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	acquired, err := tryLockTransientSnapshotFile(file)
	if err != nil || !acquired {
		return nil, false, errors.Join(err, file.Close())
	}
	return &transientSnapshotLease{file: file}, true, nil
}
