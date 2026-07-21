package workspaces

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
)

func TestProvisionerFileReadyPreservesWorkspaceState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	sandboxRoot := filepath.Join(root, "sandboxes", "file-state")
	workspacePath := filepath.Join(sandboxRoot, "workspace")
	if err := os.MkdirAll(sandboxRoot, 0o755); err != nil {
		t.Fatalf("create sandbox root: %v", err)
	}

	config := &appconfig.Config{DataRoot: dataRoot}
	workspaceID := "file-state-source"
	sourceRoot, err := DefaultFileWorkspaceContentRoot(config, workspaceID)
	if err != nil {
		t.Fatalf("resolve file workspace source root: %v", err)
	}
	writeProvisionerFileStateFile(t, filepath.Join(sourceRoot, "template.txt"), "template v1\n", 0o640)
	writeProvisionerFileStateFile(t, filepath.Join(sourceRoot, "removed-by-user.txt"), "remove me locally\n", 0o600)
	writeProvisionerFileStateFile(t, filepath.Join(sourceRoot, "original-dir", "nested.sh"), "#!/bin/sh\necho v1\n", 0o751)
	if err := os.Chmod(filepath.Join(sourceRoot, "original-dir"), 0o750); err != nil {
		t.Fatalf("set source directory mode: %v", err)
	}

	workspaceConfigs := newProvisionerFileStateConfigStore(domain.WorkspaceConfig{
		ID:         workspaceID,
		Name:       "File state source",
		Type:       "file",
		ConfigJSON: `{}`,
	})
	stored := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:            "file-state",
			WorkspacePath: workspacePath,
		},
		WorkspaceID: workspaceID,
		WorkspaceProvisioning: &domain.SandboxWorkspaceProvisioning{
			Version:   domain.SandboxWorkspaceProvisioningVersion,
			Status:    domain.SandboxWorkspaceProvisioningStatusPending,
			UpdatedAt: time.Unix(1, 0).UTC(),
		},
	}
	sandboxes := newProvisionerFileStateSandboxStore(sandboxRoot, stored)
	materializer := &provisionerFileStateMaterializer{
		delegate: sessionWorkspaceMaterializer{config: config, workspaces: workspaceConfigs},
	}
	provisioner := NewProvisionerWithMaterializer(sandboxes, materializer)
	caller := cloneProvisionerFileStateSandbox(stored)

	if err := provisioner.Ensure(context.Background(), caller); err != nil {
		t.Fatalf("first Ensure returned error: %v", err)
	}
	assertProvisionerFileStateReady(t, caller)
	assertProvisionerFileStateReady(t, sandboxes.persistedSandbox(t))
	if got := materializer.callCount(); got != 1 {
		t.Fatalf("materializer calls after first Ensure = %d, want 1", got)
	}
	if got := workspaceConfigs.callCount(); got != 1 {
		t.Fatalf("workspace config calls after first Ensure = %d, want 1", got)
	}
	assertProvisionerFileStateFile(t, filepath.Join(workspacePath, "template.txt"), "template v1\n", 0o640)
	assertProvisionerFileStateFile(t, filepath.Join(workspacePath, "removed-by-user.txt"), "remove me locally\n", 0o600)
	assertProvisionerFileStateFile(t, filepath.Join(workspacePath, "original-dir", "nested.sh"), "#!/bin/sh\necho v1\n", 0o751)

	writeProvisionerFileStateFile(t, filepath.Join(workspacePath, "template.txt"), "user-edited template\n", 0o601)
	if err := os.Remove(filepath.Join(workspacePath, "removed-by-user.txt")); err != nil {
		t.Fatalf("delete copied template file: %v", err)
	}
	if err := os.Rename(
		filepath.Join(workspacePath, "original-dir"),
		filepath.Join(workspacePath, "renamed-by-user"),
	); err != nil {
		t.Fatalf("rename copied template directory: %v", err)
	}
	writeProvisionerFileStateFile(t, filepath.Join(workspacePath, "user-created.bin"), "\x00user data\n", 0o620)
	if err := os.Symlink("renamed-by-user/nested.sh", filepath.Join(workspacePath, "user-link")); err != nil {
		t.Fatalf("create user symlink: %v", err)
	}
	wantManifest := snapshotProvisionerFileStateManifest(t, workspacePath)

	writeProvisionerFileStateFile(t, filepath.Join(sourceRoot, "template.txt"), "template v2 must not overwrite user data\n", 0o644)
	workspaceConfigs.put(domain.WorkspaceConfig{
		ID:         workspaceID,
		Name:       "Changed source config",
		Type:       "unsupported-if-resolved",
		ConfigJSON: `{"changed":true}`,
	})
	ensureProvisionerFileStateReadyUnchanged(t, provisioner, caller, workspacePath, wantManifest)
	if got := materializer.callCount(); got != 1 {
		t.Fatalf("materializer calls after changed source = %d, want 1", got)
	}
	if got := workspaceConfigs.callCount(); got != 1 {
		t.Fatalf("workspace config calls after changed source = %d, want 1", got)
	}

	workspaceConfigs.delete(workspaceID)
	if err := os.RemoveAll(sourceRoot); err != nil {
		t.Fatalf("delete file workspace source: %v", err)
	}
	ensureProvisionerFileStateReadyUnchanged(t, provisioner, caller, workspacePath, wantManifest)
	if got := materializer.callCount(); got != 1 {
		t.Fatalf("materializer calls after deleted source = %d, want 1", got)
	}
	if got := workspaceConfigs.callCount(); got != 1 {
		t.Fatalf("workspace config calls after deleted source = %d, want 1", got)
	}

	workspaceConfigs.setError(errors.New("workspace config backend unavailable"))
	ensureProvisionerFileStateReadyUnchanged(t, provisioner, caller, workspacePath, wantManifest)
	if got := materializer.callCount(); got != 1 {
		t.Fatalf("materializer calls with unavailable config store = %d, want 1", got)
	}
	if got := workspaceConfigs.callCount(); got != 1 {
		t.Fatalf("workspace config calls with unavailable config store = %d, want 1", got)
	}
}

func ensureProvisionerFileStateReadyUnchanged(
	t *testing.T,
	provisioner *Provisioner,
	sandbox *domain.Sandbox,
	workspacePath string,
	want []provisionerFileStateManifestEntry,
) {
	t.Helper()
	if err := provisioner.Ensure(context.Background(), sandbox); err != nil {
		t.Fatalf("ready Ensure returned error: %v", err)
	}
	assertProvisionerFileStateReady(t, sandbox)
	got := snapshotProvisionerFileStateManifest(t, workspacePath)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ready workspace manifest changed:\n got: %#v\nwant: %#v", got, want)
	}
}

type provisionerFileStateConfigStore struct {
	mu      sync.Mutex
	configs map[string]domain.WorkspaceConfig
	err     error
	calls   int
}

var _ WorkspaceConfigStore = (*provisionerFileStateConfigStore)(nil)

func newProvisionerFileStateConfigStore(configs ...domain.WorkspaceConfig) *provisionerFileStateConfigStore {
	store := &provisionerFileStateConfigStore{configs: make(map[string]domain.WorkspaceConfig, len(configs))}
	for _, config := range configs {
		store.configs[config.ID] = config
	}
	return store
}

func (s *provisionerFileStateConfigStore) GetWorkspaceConfig(_ context.Context, id string) (domain.WorkspaceConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return domain.WorkspaceConfig{}, s.err
	}
	config, ok := s.configs[id]
	if !ok {
		return domain.WorkspaceConfig{}, fmt.Errorf("workspace config %q not found", id)
	}
	return config, nil
}

func (s *provisionerFileStateConfigStore) put(config domain.WorkspaceConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configs[config.ID] = config
}

func (s *provisionerFileStateConfigStore) delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.configs, id)
}

func (s *provisionerFileStateConfigStore) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *provisionerFileStateConfigStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type provisionerFileStateSandboxStore struct {
	mu          sync.Mutex
	sandboxRoot string
	sandbox     *domain.Sandbox
}

var _ SandboxStore = (*provisionerFileStateSandboxStore)(nil)
var _ SandboxPathResolver = (*provisionerFileStateSandboxStore)(nil)

func newProvisionerFileStateSandboxStore(root string, sandbox *domain.Sandbox) *provisionerFileStateSandboxStore {
	return &provisionerFileStateSandboxStore{
		sandboxRoot: root,
		sandbox:     cloneProvisionerFileStateSandbox(sandbox),
	}
}

func (s *provisionerFileStateSandboxStore) GetSandbox(_ context.Context, id string) (*domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sandbox == nil || s.sandbox.Summary.ID != id {
		return nil, fmt.Errorf("sandbox %q not found", id)
	}
	return cloneProvisionerFileStateSandbox(s.sandbox), nil
}

func (s *provisionerFileStateSandboxStore) UpdateSandbox(_ context.Context, sandbox *domain.Sandbox) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sandbox = cloneProvisionerFileStateSandbox(sandbox)
	return nil
}

func (s *provisionerFileStateSandboxStore) SandboxDir(string) string {
	return s.sandboxRoot
}

func (s *provisionerFileStateSandboxStore) persistedSandbox(t *testing.T) *domain.Sandbox {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProvisionerFileStateSandbox(s.sandbox)
}

type provisionerFileStateMaterializer struct {
	mu       sync.Mutex
	calls    int
	delegate WorkspaceMaterializer
}

var _ WorkspaceMaterializer = (*provisionerFileStateMaterializer)(nil)

func (m *provisionerFileStateMaterializer) Materialize(ctx context.Context, sandbox *domain.Sandbox) error {
	m.mu.Lock()
	m.calls++
	delegate := m.delegate
	m.mu.Unlock()
	return delegate.Materialize(ctx, sandbox)
}

func (m *provisionerFileStateMaterializer) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

type provisionerFileStateManifestEntry struct {
	Path          string
	Type          string
	Mode          fs.FileMode
	Content       string
	SymlinkTarget string
}

func snapshotProvisionerFileStateManifest(t *testing.T, root string) []provisionerFileStateManifestEntry {
	t.Helper()
	manifest := make([]provisionerFileStateManifestEntry, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		item := provisionerFileStateManifestEntry{Path: filepath.ToSlash(relPath), Mode: info.Mode()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			item.Type = "symlink"
			item.SymlinkTarget, err = os.Readlink(path)
		case info.IsDir():
			item.Type = "directory"
		case info.Mode().IsRegular():
			item.Type = "file"
			var content []byte
			content, err = os.ReadFile(path)
			item.Content = string(content)
		default:
			return fmt.Errorf("unsupported workspace entry %s with mode %s", relPath, info.Mode())
		}
		if err != nil {
			return err
		}
		manifest = append(manifest, item)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot workspace manifest: %v", err)
	}
	return manifest
}

func writeProvisionerFileStateFile(t *testing.T, path, content string, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("set mode on %s: %v", path, err)
	}
}

func assertProvisionerFileStateFile(t *testing.T, path, wantContent string, wantMode fs.FileMode) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if got := string(content); got != wantContent {
		t.Fatalf("content of %s = %q, want %q", path, got, wantContent)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != wantMode.Perm() {
		t.Fatalf("mode of %s = %o, want %o", path, got, wantMode.Perm())
	}
}

func assertProvisionerFileStateReady(t *testing.T, sandbox *domain.Sandbox) {
	t.Helper()
	if sandbox == nil || sandbox.WorkspaceProvisioning == nil {
		t.Fatalf("workspace provisioning = %#v, want ready", sandbox)
	}
	if got := sandbox.WorkspaceProvisioning.Status; got != domain.SandboxWorkspaceProvisioningStatusReady {
		t.Fatalf("workspace provisioning status = %q, want %q", got, domain.SandboxWorkspaceProvisioningStatusReady)
	}
}

func cloneProvisionerFileStateSandbox(sandbox *domain.Sandbox) *domain.Sandbox {
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
	clone.ExecutionProviderEnvItems = append([]domain.SandboxEnvVar(nil), sandbox.ExecutionProviderEnvItems...)
	return &clone
}
