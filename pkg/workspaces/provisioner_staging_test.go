package workspaces

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	domain "agent-compose/pkg/model"
)

func TestProvisionerStagingPromotionReplacesPendingWorkspace(t *testing.T) {
	t.Parallel()

	sandboxRoot := filepath.Join(t.TempDir(), "sandbox-staging-promotion")
	workspacePath := filepath.Join(sandboxRoot, "workspace")
	stagingRoot := filepath.Join(sandboxRoot, "state", "workspace-provisioning")
	staleAttempt := filepath.Join(stagingRoot, "attempt-stale", "partial")
	writeProvisionerStagingFile(t, filepath.Join(workspacePath, "pending-half.txt"), "pending half\n")
	writeProvisionerStagingFile(t, staleAttempt, "stale attempt\n")
	writeProvisionerStagingFile(t, filepath.Join(stagingRoot, "keep", "sentinel.txt"), "keep sibling\n")
	outsideAttemptTarget := filepath.Join(t.TempDir(), "outside-attempt-target")
	writeProvisionerStagingFile(t, filepath.Join(outsideAttemptTarget, "preserved.txt"), "outside preserved\n")
	if err := os.Symlink(outsideAttemptTarget, filepath.Join(stagingRoot, "attempt-link")); err != nil {
		t.Fatalf("create stale attempt symlink: %v", err)
	}

	stored := newProvisionerStagingSandbox("staging-promotion", workspacePath, domain.SandboxWorkspaceProvisioningStatusPending)
	store := newProvisionerStagingStore(stored)
	materializer := &provisionerStagingMaterializer{
		materialize: func(sandbox *domain.Sandbox) error {
			if err := validateProvisionerStagingAttemptPath(sandboxRoot, sandbox.Summary.WorkspacePath); err != nil {
				return err
			}
			if sandbox.Summary.WorkspacePath == workspacePath {
				return fmt.Errorf("provider received formal workspace path %q", workspacePath)
			}
			if _, err := os.Stat(staleAttempt); !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("stale attempt still exists before materialization: %w", err)
			}
			if _, err := os.Lstat(filepath.Join(stagingRoot, "attempt-link")); !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("stale attempt symlink still exists before materialization: %w", err)
			}
			if data, err := os.ReadFile(filepath.Join(outsideAttemptTarget, "preserved.txt")); err != nil || string(data) != "outside preserved\n" {
				return fmt.Errorf("stale attempt symlink target changed: data=%q err=%v", data, err)
			}
			if data, err := os.ReadFile(filepath.Join(stagingRoot, "keep", "sentinel.txt")); err != nil || string(data) != "keep sibling\n" {
				return fmt.Errorf("non-attempt staging sibling changed: data=%q err=%v", data, err)
			}
			data, err := os.ReadFile(filepath.Join(workspacePath, "pending-half.txt"))
			if err != nil {
				return fmt.Errorf("read formal pending half during materialization: %w", err)
			}
			if got, want := string(data), "pending half\n"; got != want {
				return fmt.Errorf("formal pending half during materialization = %q, want %q", got, want)
			}
			if err := os.WriteFile(filepath.Join(sandbox.Summary.WorkspacePath, "provider.txt"), []byte("materialized\n"), 0o644); err != nil {
				return fmt.Errorf("write provider output: %w", err)
			}
			if err := os.MkdirAll(filepath.Join(sandbox.Summary.WorkspacePath, "nested"), 0o755); err != nil {
				return fmt.Errorf("create provider nested output: %w", err)
			}
			if err := os.WriteFile(filepath.Join(sandbox.Summary.WorkspacePath, "nested", "result.txt"), []byte("complete\n"), 0o644); err != nil {
				return fmt.Errorf("write provider nested output: %w", err)
			}
			return nil
		},
	}
	provisioner := NewProvisionerWithMaterializer(store, materializer)
	caller := cloneProvisionerStagingSandbox(stored)

	if err := provisioner.Ensure(context.Background(), caller); err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}

	paths := materializer.pathsFor("staging-promotion")
	if len(paths) != 1 {
		t.Fatalf("materializer paths = %#v, want one staging path", paths)
	}
	if err := validateProvisionerStagingAttemptPath(sandboxRoot, paths[0]); err != nil {
		t.Fatalf("materializer path after Ensure: %v", err)
	}
	if _, err := os.Stat(paths[0]); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("promoted attempt stat = %v, want not exist", err)
	}
	assertProvisionerStagingFile(t, filepath.Join(workspacePath, "provider.txt"), "materialized\n")
	assertProvisionerStagingFile(t, filepath.Join(workspacePath, "nested", "result.txt"), "complete\n")
	if _, err := os.Stat(filepath.Join(workspacePath, "pending-half.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("formal pending half stat after promotion = %v, want not exist", err)
	}
	assertNoProvisionerStagingAttempts(t, stagingRoot)
	assertProvisionerStagingFile(t, filepath.Join(stagingRoot, "keep", "sentinel.txt"), "keep sibling\n")
	assertProvisionerStagingFile(t, filepath.Join(outsideAttemptTarget, "preserved.txt"), "outside preserved\n")
	assertProvisionerStagingReady(t, caller)
	persisted := store.sandbox(t, "staging-promotion")
	assertProvisionerStagingReady(t, persisted)
	if got := persisted.Summary.WorkspacePath; got != workspacePath {
		t.Fatalf("persisted workspace path = %q, want formal path %q", got, workspacePath)
	}
}

func TestProvisionerStagingPromotionCreatesWorkspaceAtMissingTarget(t *testing.T) {
	t.Parallel()

	sandboxRoot := t.TempDir()
	workspacePath := filepath.Join(sandboxRoot, "workspace")
	stored := newProvisionerStagingSandbox("missing-workspace-target", workspacePath, domain.SandboxWorkspaceProvisioningStatusPending)
	store := newProvisionerStagingStore(stored)
	materializer := &provisionerStagingMaterializer{materialize: func(sandbox *domain.Sandbox) error {
		return os.WriteFile(filepath.Join(sandbox.Summary.WorkspacePath, "materialized.txt"), []byte("ready\n"), 0o644)
	}}
	provisioner := NewProvisionerWithMaterializer(store, materializer)
	caller := cloneProvisionerStagingSandbox(stored)

	if _, err := os.Stat(workspacePath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("workspace target stat before Ensure = %v, want not exist", err)
	}
	if err := provisioner.Ensure(context.Background(), caller); err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}

	assertProvisionerStagingFile(t, filepath.Join(workspacePath, "materialized.txt"), "ready\n")
	assertProvisionerStagingReady(t, caller)
	assertProvisionerStagingReady(t, store.sandbox(t, caller.Summary.ID))
}

func TestProvisionerStagingRejectsStateSymlinkWithoutTouchingTarget(t *testing.T) {
	t.Parallel()

	sandboxRoot := t.TempDir()
	workspacePath := filepath.Join(sandboxRoot, "workspace")
	outside := t.TempDir()
	victim := filepath.Join(outside, "attempt-victim", "preserved.txt")
	writeProvisionerStagingFile(t, victim, "preserve outside\n")
	if err := os.Symlink(outside, filepath.Join(sandboxRoot, "state")); err != nil {
		t.Fatalf("create state symlink: %v", err)
	}

	stored := newProvisionerStagingSandbox("state-symlink", workspacePath, domain.SandboxWorkspaceProvisioningStatusPending)
	store := newProvisionerStagingStore(stored)
	materializer := &provisionerStagingMaterializer{}
	provisioner := NewProvisionerWithMaterializer(store, materializer)
	caller := cloneProvisionerStagingSandbox(stored)

	if err := provisioner.Ensure(context.Background(), caller); err == nil {
		t.Fatal("Ensure state symlink error = nil, want fail closed")
	}
	if got := materializer.callCount(); got != 0 {
		t.Fatalf("materializer calls = %d, want 0", got)
	}
	assertProvisionerStagingFile(t, victim, "preserve outside\n")
	if got := caller.WorkspaceProvisioning.Status; got != domain.SandboxWorkspaceProvisioningStatusFailed {
		t.Fatalf("caller provisioning status = %q, want failed", got)
	}
}

func TestProvisionerStagingRejectsWorkspaceOutsideAuthoritativeSandbox(t *testing.T) {
	t.Parallel()

	authoritativeRoot := t.TempDir()
	outsideRoot := t.TempDir()
	workspacePath := filepath.Join(outsideRoot, "workspace")
	victim := filepath.Join(workspacePath, "preserved.txt")
	writeProvisionerStagingFile(t, victim, "preserve workspace\n")
	stored := newProvisionerStagingSandbox("outside-workspace", workspacePath, domain.SandboxWorkspaceProvisioningStatusPending)
	baseStore := newProvisionerStagingStore(stored)
	store := &fixedProvisionerStagingPathStore{provisionerStagingStore: baseStore, root: authoritativeRoot}
	materializer := &provisionerStagingMaterializer{}
	provisioner := NewProvisionerWithMaterializer(store, materializer)
	caller := cloneProvisionerStagingSandbox(stored)

	if err := provisioner.Ensure(context.Background(), caller); err == nil {
		t.Fatal("Ensure outside workspace error = nil, want fail closed")
	}
	if got := materializer.callCount(); got != 0 {
		t.Fatalf("materializer calls = %d, want 0", got)
	}
	assertProvisionerStagingFile(t, victim, "preserve workspace\n")
	if got := caller.WorkspaceProvisioning.Status; got != domain.SandboxWorkspaceProvisioningStatusFailed {
		t.Fatalf("caller provisioning status = %q, want failed", got)
	}
}

func TestProvisionerReadyLeavesStagingAndWorkspaceUntouched(t *testing.T) {
	t.Parallel()

	sandboxRoot := filepath.Join(t.TempDir(), "sandbox-ready-untouched")
	workspacePath := filepath.Join(sandboxRoot, "workspace")
	stagingRoot := filepath.Join(sandboxRoot, "state", "workspace-provisioning")
	writeProvisionerStagingFile(t, filepath.Join(workspacePath, "user", "sentinel.txt"), "preserve me\n")
	writeProvisionerStagingFile(t, filepath.Join(stagingRoot, "attempt-preserve", "partial.txt"), "preserve attempt\n")
	oldTime := time.Unix(1_600_000_000, 0).UTC()
	for _, path := range []string{
		filepath.Join(workspacePath, "user", "sentinel.txt"),
		filepath.Join(workspacePath, "user"),
		workspacePath,
		filepath.Join(stagingRoot, "attempt-preserve", "partial.txt"),
		filepath.Join(stagingRoot, "attempt-preserve"),
		stagingRoot,
	} {
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatalf("set stable mtime on %s: %v", path, err)
		}
	}
	before := snapshotProvisionerStagingTree(t, sandboxRoot)

	stored := newProvisionerStagingSandbox("ready-untouched", workspacePath, domain.SandboxWorkspaceProvisioningStatusReady)
	store := newProvisionerStagingStore(stored)
	materializer := &provisionerStagingMaterializer{panicOnCall: true}
	provisioner := NewProvisionerWithMaterializer(store, materializer)
	provisioner.filesystem = panicProvisionerStagingFileSystem{}
	caller := cloneProvisionerStagingSandbox(stored)

	if err := provisioner.Ensure(context.Background(), caller); err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}

	after := snapshotProvisionerStagingTree(t, sandboxRoot)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("ready sandbox filesystem changed:\n got: %#v\nwant: %#v", after, before)
	}
	if got := materializer.callCount(); got != 0 {
		t.Fatalf("materializer calls = %d, want 0", got)
	}
	if got := store.updateCount(); got != 0 {
		t.Fatalf("store updates = %d, want 0", got)
	}
	assertProvisionerStagingReady(t, caller)
}

func TestProvisionerStagingPromotionIsIsolatedBySandbox(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	sandboxRoots := map[string]string{
		"staging-isolation-a": filepath.Join(parent, "sandbox-a"),
		"staging-isolation-b": filepath.Join(parent, "sandbox-b"),
	}
	staleAttempts := make(map[string]string, len(sandboxRoots))
	stored := make([]*domain.Sandbox, 0, len(sandboxRoots))
	for id, root := range sandboxRoots {
		workspacePath := filepath.Join(root, "workspace")
		writeProvisionerStagingFile(t, filepath.Join(workspacePath, "pending-"+id+".txt"), id+" pending\n")
		staleAttempt := filepath.Join(root, "state", "workspace-provisioning", "attempt-stale-"+id, "partial.txt")
		writeProvisionerStagingFile(t, staleAttempt, id+" stale\n")
		staleAttempts[id] = staleAttempt
		stored = append(stored, newProvisionerStagingSandbox(id, workspacePath, domain.SandboxWorkspaceProvisioningStatusPending))
	}

	store := newProvisionerStagingStore(stored...)
	materializer := &provisionerStagingMaterializer{
		materialize: func(sandbox *domain.Sandbox) error {
			root, ok := sandboxRoots[sandbox.Summary.ID]
			if !ok {
				return fmt.Errorf("unexpected sandbox %q", sandbox.Summary.ID)
			}
			if err := validateProvisionerStagingAttemptPath(root, sandbox.Summary.WorkspacePath); err != nil {
				return err
			}
			return os.WriteFile(
				filepath.Join(sandbox.Summary.WorkspacePath, "owner.txt"),
				[]byte(sandbox.Summary.ID+"\n"),
				0o644,
			)
		},
	}
	provisioner := NewProvisionerWithMaterializer(store, materializer)

	firstID := "staging-isolation-a"
	first := store.sandbox(t, firstID)
	if err := provisioner.Ensure(context.Background(), first); err != nil {
		t.Fatalf("Ensure(%s) returned error: %v", firstID, err)
	}
	if _, err := os.Stat(staleAttempts[firstID]); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("first sandbox stale attempt stat = %v, want not exist", err)
	}
	secondID := "staging-isolation-b"
	assertProvisionerStagingFile(t, staleAttempts[secondID], secondID+" stale\n")
	assertProvisionerStagingFile(t, filepath.Join(sandboxRoots[secondID], "workspace", "pending-"+secondID+".txt"), secondID+" pending\n")

	second := store.sandbox(t, secondID)
	if err := provisioner.Ensure(context.Background(), second); err != nil {
		t.Fatalf("Ensure(%s) returned error: %v", secondID, err)
	}

	for id, root := range sandboxRoots {
		paths := materializer.pathsFor(id)
		if len(paths) != 1 {
			t.Errorf("materializer paths for %s = %#v, want one", id, paths)
			continue
		}
		if err := validateProvisionerStagingAttemptPath(root, paths[0]); err != nil {
			t.Errorf("materializer path for %s: %v", id, err)
		}
		assertProvisionerStagingFile(t, filepath.Join(root, "workspace", "owner.txt"), id+"\n")
		if _, err := os.Stat(filepath.Join(root, "workspace", "pending-"+id+".txt")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("pending half for %s stat after promotion = %v, want not exist", id, err)
		}
		assertNoProvisionerStagingAttempts(t, filepath.Join(root, "state", "workspace-provisioning"))
		assertProvisionerStagingReady(t, store.sandbox(t, id))
	}
	pathsA := materializer.pathsFor(firstID)
	pathsB := materializer.pathsFor(secondID)
	if len(pathsA) == 1 && len(pathsB) == 1 && pathsA[0] == pathsB[0] {
		t.Fatalf("different sandboxes shared staging path %q", pathsA[0])
	}
}

type provisionerStagingStore struct {
	mu        sync.Mutex
	sandboxes map[string]*domain.Sandbox
	updates   int
}

type fixedProvisionerStagingPathStore struct {
	*provisionerStagingStore
	root string
}

func (s *fixedProvisionerStagingPathStore) SandboxDir(string) string {
	return s.root
}

var _ SandboxStore = (*provisionerStagingStore)(nil)
var _ SandboxPathResolver = (*provisionerStagingStore)(nil)

func newProvisionerStagingStore(sandboxes ...*domain.Sandbox) *provisionerStagingStore {
	store := &provisionerStagingStore{sandboxes: make(map[string]*domain.Sandbox, len(sandboxes))}
	for _, sandbox := range sandboxes {
		store.sandboxes[sandbox.Summary.ID] = cloneProvisionerStagingSandbox(sandbox)
	}
	return store
}

func (s *provisionerStagingStore) GetSandbox(_ context.Context, id string) (*domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sandbox, ok := s.sandboxes[id]
	if !ok {
		return nil, fmt.Errorf("sandbox %q not found", id)
	}
	return cloneProvisionerStagingSandbox(sandbox), nil
}

func (s *provisionerStagingStore) SandboxDir(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sandbox := s.sandboxes[id]; sandbox != nil {
		return filepath.Dir(sandbox.Summary.WorkspacePath)
	}
	return ""
}

func (s *provisionerStagingStore) UpdateSandbox(_ context.Context, sandbox *domain.Sandbox) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates++
	s.sandboxes[sandbox.Summary.ID] = cloneProvisionerStagingSandbox(sandbox)
	return nil
}

func (s *provisionerStagingStore) sandbox(t *testing.T, id string) *domain.Sandbox {
	t.Helper()
	sandbox, err := s.GetSandbox(context.Background(), id)
	if err != nil {
		t.Fatalf("GetSandbox(%q): %v", id, err)
	}
	return sandbox
}

func (s *provisionerStagingStore) updateCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updates
}

type provisionerStagingMaterializer struct {
	mu          sync.Mutex
	paths       map[string][]string
	calls       int
	panicOnCall bool
	materialize func(*domain.Sandbox) error
}

type panicProvisionerStagingFileSystem struct{}

var _ provisioningFileSystem = panicProvisionerStagingFileSystem{}

func (panicProvisionerStagingFileSystem) MkdirAll(string, fs.FileMode) error {
	panic("ready sandbox must not create provisioning directories")
}

func (panicProvisionerStagingFileSystem) MkdirTemp(string, string) (string, error) {
	panic("ready sandbox must not create a provisioning attempt")
}

func (panicProvisionerStagingFileSystem) Lstat(string) (fs.FileInfo, error) {
	panic("ready sandbox must not inspect sandbox paths")
}

func (panicProvisionerStagingFileSystem) ReadDir(string) ([]os.DirEntry, error) {
	panic("ready sandbox must not inspect provisioning staging")
}

func (panicProvisionerStagingFileSystem) RemoveAll(string) error {
	panic("ready sandbox must not remove workspace or provisioning staging")
}

func (panicProvisionerStagingFileSystem) Rename(string, string) error {
	panic("ready sandbox must not promote provisioning staging")
}

var _ WorkspaceMaterializer = (*provisionerStagingMaterializer)(nil)

func (m *provisionerStagingMaterializer) Materialize(_ context.Context, sandbox *domain.Sandbox) error {
	m.mu.Lock()
	m.calls++
	if m.paths == nil {
		m.paths = make(map[string][]string)
	}
	m.paths[sandbox.Summary.ID] = append(m.paths[sandbox.Summary.ID], sandbox.Summary.WorkspacePath)
	panicOnCall := m.panicOnCall
	materialize := m.materialize
	m.mu.Unlock()

	if panicOnCall {
		panic("ready sandbox must not invoke materializer")
	}
	if materialize == nil {
		return nil
	}
	return materialize(sandbox)
}

func (m *provisionerStagingMaterializer) pathsFor(id string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.paths[id]...)
}

func (m *provisionerStagingMaterializer) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func newProvisionerStagingSandbox(id, workspacePath, status string) *domain.Sandbox {
	return &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:            id,
			WorkspacePath: workspacePath,
		},
		WorkspaceID: "workspace-" + id,
		Workspace: &domain.SandboxWorkspace{
			ID:         "workspace-" + id,
			Name:       "workspace " + id,
			Type:       "file",
			ConfigJSON: `{}`,
		},
		WorkspaceProvisioning: &domain.SandboxWorkspaceProvisioning{
			Version:   domain.SandboxWorkspaceProvisioningVersion,
			Status:    status,
			UpdatedAt: time.Unix(1, 0).UTC(),
		},
	}
}

func cloneProvisionerStagingSandbox(sandbox *domain.Sandbox) *domain.Sandbox {
	if sandbox == nil {
		return nil
	}
	clone := *sandbox
	clone.Summary.Tags = append([]domain.SandboxTag(nil), sandbox.Summary.Tags...)
	if sandbox.Workspace != nil {
		workspace := *sandbox.Workspace
		clone.Workspace = &workspace
	}
	if sandbox.WorkspaceProvisioning != nil {
		provisioning := *sandbox.WorkspaceProvisioning
		clone.WorkspaceProvisioning = &provisioning
	}
	clone.EnvItems = append([]domain.SandboxEnvVar(nil), sandbox.EnvItems...)
	clone.VolumeMounts = append([]domain.SandboxVolumeMount(nil), sandbox.VolumeMounts...)
	clone.RuntimeEnvItems = append([]domain.SandboxEnvVar(nil), sandbox.RuntimeEnvItems...)
	clone.ProviderEnvItems = append([]domain.SandboxEnvVar(nil), sandbox.ProviderEnvItems...)
	return &clone
}

func validateProvisionerStagingAttemptPath(sandboxRoot, attemptPath string) error {
	wantRoot, err := filepath.Abs(filepath.Join(sandboxRoot, "state", "workspace-provisioning"))
	if err != nil {
		return fmt.Errorf("resolve expected staging root: %w", err)
	}
	gotPath, err := filepath.Abs(attemptPath)
	if err != nil {
		return fmt.Errorf("resolve materializer workspace path: %w", err)
	}
	if filepath.Dir(gotPath) != wantRoot {
		return fmt.Errorf("materializer workspace path = %q, want direct child of %q", gotPath, wantRoot)
	}
	base := filepath.Base(gotPath)
	if !strings.HasPrefix(base, "attempt-") || strings.TrimPrefix(base, "attempt-") == "" {
		return fmt.Errorf("materializer workspace path basename = %q, want attempt-<id>", base)
	}
	return nil
}

func writeProvisionerStagingFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertProvisionerStagingFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if got := string(data); got != want {
		t.Fatalf("content of %s = %q, want %q", path, got, want)
	}
}

func assertNoProvisionerStagingAttempts(t *testing.T, stagingRoot string) {
	t.Helper()
	entries, err := os.ReadDir(stagingRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("read staging root %s: %v", stagingRoot, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "attempt-") {
			t.Errorf("staging attempt remains after promotion: %s", filepath.Join(stagingRoot, entry.Name()))
		}
	}
}

func assertProvisionerStagingReady(t *testing.T, sandbox *domain.Sandbox) {
	t.Helper()
	if sandbox.WorkspaceProvisioning == nil {
		t.Fatal("workspace provisioning = nil, want ready")
	}
	if got := sandbox.WorkspaceProvisioning.Status; got != domain.SandboxWorkspaceProvisioningStatusReady {
		t.Fatalf("workspace provisioning status = %q, want %q", got, domain.SandboxWorkspaceProvisioningStatusReady)
	}
}

type provisionerStagingTreeEntry struct {
	Path    string
	Mode    fs.FileMode
	Size    int64
	ModTime time.Time
	Content string
}

func snapshotProvisionerStagingTree(t *testing.T, root string) []provisionerStagingTreeEntry {
	t.Helper()
	var snapshot []provisionerStagingTreeEntry
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		item := provisionerStagingTreeEntry{
			Path:    filepath.ToSlash(rel),
			Mode:    info.Mode(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			item.Content = string(data)
		}
		snapshot = append(snapshot, item)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot tree %s: %v", root, err)
	}
	return snapshot
}
