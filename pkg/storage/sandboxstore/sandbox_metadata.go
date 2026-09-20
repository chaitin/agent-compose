package sandboxstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/identity"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
)

func (s *Store) UpdateSandbox(_ context.Context, session *Sandbox) error {
	s.hydrateSandboxGuestImage(session)
	session.Summary.UpdatedAt = s.currentTime().UTC()
	unlock := s.lockSandbox(session.Summary.ID)
	defer unlock()
	if err := s.saveSandboxPreservingCounts(session); err != nil {
		return err
	}
	s.recordIndex(session)
	return nil
}

func (s *Store) RemoveSandbox(_ context.Context, id string) error {
	id = strings.TrimSpace(id)
	if err := validateSandboxIDForRemove(id); err != nil {
		return err
	}
	path := s.sandboxDir(id)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("stat sandbox dir %s: %w", id, err)
	}

	unlock := s.lockSandbox(id)
	defer unlock()

	if err := driverpkg.CleanupBoxliteVolumeBridgeMounts(path); err != nil {
		return fmt.Errorf("cleanup session mounts %s: %w", id, err)
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove sandbox dir %s: %w", id, err)
	}
	s.layout.remove(id, path)
	if err := s.deleteIndexRow(id); err != nil {
		slog.Warn("failed to delete sandbox listing cache row after removing authoritative metadata", "sandbox_id", id, "error", err)
	}
	s.sandboxLocks.Delete(sandboxLockKey(id))
	return nil
}

func (s *Store) backfillOwnershipRecords() error {
	locations, err := s.layout.discover()
	if err != nil {
		return fmt.Errorf("discover sandbox directories for lifecycle backfill: %w", err)
	}
	for _, location := range locations {
		sandbox, loadErr := s.loadSandboxFromDir(location.id, location.path)
		if loadErr != nil {
			continue
		}
		if _, recordErr := sandboxes.ReadOwnershipRecord(s.config.SandboxRoot, sandbox.Summary.ID); recordErr == nil {
			continue
		} else if !os.IsNotExist(recordErr) {
			// A corrupt or unsupported record is evidence we cannot safely replace.
			continue
		}
		record := sandboxes.OwnershipRecord{
			Version: sandboxes.OwnershipRecordVersion, SandboxID: sandbox.Summary.ID,
			Driver: sandbox.Summary.Driver, RuntimeID: sandbox.Summary.RuntimeRef,
			SandboxPath: location.path, LifecycleState: "active",
			OwnedResources:    []sandboxes.OwnedResource{{Kind: "runtime", Identity: sandbox.Summary.RuntimeRef}, {Kind: "sandbox-directory", Path: location.path}},
			CacheDependencies: []sandboxes.CacheDependency{{Domain: "runtime-image", Identity: sandbox.Summary.GuestImage}},
		}
		if writeErr := sandboxes.WriteOwnershipRecord(s.config.SandboxRoot, record); writeErr != nil {
			return fmt.Errorf("backfill sandbox ownership %s: %w", sandbox.Summary.ID, writeErr)
		}
	}
	return nil
}

func (s *Store) sandboxDir(id string) string {
	return s.layout.path(id)
}

func (s *Store) SandboxDir(id string) string {
	return s.sandboxDir(id)
}

func validateSandboxIDForRemove(id string) error {
	if id == "" {
		return fmt.Errorf("sandbox id is required")
	}
	if id == "." || id == ".." || filepath.Base(id) != id {
		return fmt.Errorf("invalid sandbox id %q", id)
	}
	return nil
}

func sandboxDirName(id string) string {
	if hash, err := identity.Hash(id); err == nil {
		return hash
	}
	return id
}

func (s *Store) lockSandbox(id string) func() {
	value, _ := s.sandboxLocks.LoadOrStore(sandboxLockKey(id), &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func sandboxLockKey(id string) string {
	return sandboxDirName(id)
}

func (s *Store) hydrateSandboxGuestImage(session *Sandbox) {
	if session == nil {
		return
	}
	if strings.TrimSpace(session.Summary.GuestImage) != "" {
		return
	}
	if vmState, err := s.GetVMState(session.Summary.ID); err == nil {
		session.Summary.GuestImage = driverpkg.ResolveSandboxGuestImage("", vmState.Image, driverpkg.DefaultGuestImageForDriver(s.config, session.Summary.Driver))
		return
	}
	session.Summary.GuestImage = driverpkg.ResolveSandboxGuestImage("", "", driverpkg.DefaultGuestImageForDriver(s.config, session.Summary.Driver))
}

func (s *Store) loadSandbox(id string) (*Sandbox, error) {
	return s.loadSandboxFromDir(id, s.sandboxDir(id))
}

// Invalid contents require a metadata change, not another database retry.
// Keep filesystem read errors separate so transient I/O failures stay retryable.
var errInvalidSandboxMetadata = errors.New("invalid sandbox metadata")

func (s *Store) loadSandboxFromDir(id, sandboxDir string) (*Sandbox, error) {
	path := filepath.Join(sandboxDir, "metadata.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session metadata %s: %w", id, err)
	}
	var session Sandbox
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("%w: decode session metadata %s: %w", errInvalidSandboxMetadata, id, err)
	}
	if strings.TrimSpace(session.Summary.ID) == "" {
		return nil, fmt.Errorf("%w: decode session metadata %s: sandbox id is required", errInvalidSandboxMetadata, id)
	}
	if sandboxDirName(session.Summary.ID) != sandboxDirName(id) {
		return nil, fmt.Errorf("%w: decode session metadata %s: sandbox id %q does not match directory", errInvalidSandboxMetadata, id, session.Summary.ID)
	}
	// WorkspacePath is derived from the active sandbox root. Persisted absolute
	// paths may refer to the filesystem namespace of an older daemon process.
	session.Summary.WorkspacePath = filepath.Join(sandboxDir, "workspace")
	session.Summary.TriggerSource = sandboxes.NormalizeTriggerSource(session.Summary.TriggerSource, session.Summary.Tags)
	if strings.TrimSpace(session.Summary.ShortID) == "" {
		session.Summary.ShortID = identity.ShortID(session.Summary.ID)
	}
	driver, err := driverpkg.ResolveSandboxRuntimeDriver(session.Summary.Driver, s.config.RuntimeDriver)
	if err != nil {
		return nil, fmt.Errorf("%w: session metadata %s has invalid driver: %w", errInvalidSandboxMetadata, id, err)
	}
	session.Summary.Driver = driver
	if err := s.layout.register(session.Summary.ID, sandboxDir); err != nil {
		return nil, fmt.Errorf("register session metadata %s directory: %w", id, err)
	}
	return &session, nil
}

func (s *Store) LoadSandbox(id string) (*Sandbox, error) {
	return s.loadSandbox(id)
}

func (s *Store) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Store) saveSandbox(session *Sandbox) error {
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session metadata: %w", err)
	}
	if err := writeFileAtomically(
		filepath.Join(s.sandboxDir(session.Summary.ID), "metadata.json"),
		append(data, '\n'),
		0o644,
	); err != nil {
		return fmt.Errorf("write session metadata: %w", err)
	}
	return nil
}

func writeFileAtomically(path string, data []byte, perm fs.FileMode) (returnErr error) {
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		if err := os.Remove(temporaryPath); err != nil && !os.IsNotExist(err) {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary file %s: %w", temporaryPath, err))
		}
	}()

	if err := temporary.Chmod(perm); err != nil {
		return fmt.Errorf("set temporary file mode for %s: %w", path, err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write temporary file for %s: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary file for %s: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", path, err)
	}
	closed = true
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace %s with temporary file: %w", path, err)
	}
	return nil
}

func (s *Store) saveSandboxPreservingCounts(session *Sandbox) error {
	existing, err := s.loadSandboxCounts(session.Summary.ID)
	if err != nil {
		return err
	}
	if existing.CellCount > session.Summary.CellCount {
		session.Summary.CellCount = existing.CellCount
	}
	if existing.EventCount > session.Summary.EventCount {
		session.Summary.EventCount = existing.EventCount
	}
	return s.saveSandbox(session)
}

func (s *Store) loadSandboxCounts(id string) (SandboxSummary, error) {
	path := filepath.Join(s.sandboxDir(id), "metadata.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return SandboxSummary{}, nil
		}
		return SandboxSummary{}, fmt.Errorf("read session metadata %s: %w", id, err)
	}
	var session Sandbox
	if err := json.Unmarshal(data, &session); err != nil {
		return SandboxSummary{}, fmt.Errorf("%w: decode session metadata %s: %w", errInvalidSandboxMetadata, id, err)
	}
	return session.Summary, nil
}

func (s *Store) saveEventCount(id string, eventCount int) error {
	session, err := s.loadSandbox(id)
	if err != nil {
		return err
	}
	session.Summary.EventCount = eventCount
	s.hydrateSandboxGuestImage(session)
	session.Summary.UpdatedAt = s.currentTime().UTC()
	if err := s.saveSandbox(session); err != nil {
		return err
	}
	s.recordIndex(session)
	return nil
}

func (s *Store) SaveSandbox(session *Sandbox) error {
	unlock := s.lockSandbox(session.Summary.ID)
	defer unlock()
	if err := s.saveSandboxPreservingCounts(session); err != nil {
		return err
	}
	s.recordIndex(session)
	return nil
}
