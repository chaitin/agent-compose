package workspaces

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
)

func TestProvisionerNoWorkspaceDoesNotCreateStateOrMaterialize(t *testing.T) {
	t.Parallel()

	stored := provisionerStateSandbox("no-workspace", "/tmp/no-workspace", "")
	stored.WorkspaceID = ""
	stored.Workspace = nil
	store := newProvisionerStateStore(stored)
	materializer := &provisionerStateMaterializer{}
	provisioner := NewProvisionerWithMaterializer(store, materializer)

	caller := cloneProvisionerStateSandbox(stored)
	if err := provisioner.Ensure(context.Background(), caller); err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	if caller.WorkspaceProvisioning != nil {
		t.Fatalf("caller workspace provisioning = %#v, want nil", caller.WorkspaceProvisioning)
	}
	persisted := store.sandbox(t, stored.Summary.ID)
	if persisted.WorkspaceProvisioning != nil {
		t.Fatalf("persisted workspace provisioning = %#v, want nil", persisted.WorkspaceProvisioning)
	}
	if got := store.updateCount(); got != 0 {
		t.Fatalf("UpdateSandbox calls = %d, want 0", got)
	}
	if got := store.workspaceConfigCount(); got != 0 {
		t.Fatalf("GetWorkspaceConfig calls = %d, want 0", got)
	}
	if got := materializer.callCount(); got != 0 {
		t.Fatalf("Materialize calls = %d, want 0", got)
	}
}

func TestProvisionerLegacyWorkspacePersistsReadyWithoutResolvingSource(t *testing.T) {
	t.Parallel()

	for _, vmStatus := range []string{
		domain.VMStatusPending,
		domain.VMStatusRunning,
		domain.VMStatusStopped,
		domain.VMStatusFailed,
	} {
		t.Run(vmStatus, func(t *testing.T) {
			root := t.TempDir()
			workspacePath := filepath.Join(root, "workspace")
			switch vmStatus {
			case domain.VMStatusRunning:
				if err := os.Mkdir(workspacePath, 0o751); err != nil {
					t.Fatalf("create empty legacy workspace: %v", err)
				}
			case domain.VMStatusStopped:
				if err := os.Mkdir(workspacePath, 0o750); err != nil {
					t.Fatalf("create legacy workspace: %v", err)
				}
				if err := os.WriteFile(filepath.Join(workspacePath, "partial.txt"), []byte("preserve partial\n"), 0o640); err != nil {
					t.Fatalf("write legacy partial workspace: %v", err)
				}
			case domain.VMStatusFailed:
				target := filepath.Join(root, "external-target")
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatalf("create legacy symlink target: %v", err)
				}
				if err := os.WriteFile(filepath.Join(target, "preserved.txt"), []byte("preserve target\n"), 0o644); err != nil {
					t.Fatalf("write legacy symlink target: %v", err)
				}
				if err := os.Symlink(target, workspacePath); err != nil {
					t.Fatalf("create legacy workspace symlink: %v", err)
				}
			}
			before := snapshotProvisionerStagingTree(t, root)

			stored := provisionerStateSandbox("legacy-"+strings.ToLower(vmStatus), workspacePath, "")
			stored.Summary.VMStatus = vmStatus
			stored.WorkspaceProvisioning = nil
			store := newProvisionerStateStore(stored)
			store.panicOnWorkspaceConfig = true
			materializer := &provisionerStateMaterializer{panicOnCall: true}
			provisioner := NewProvisionerWithMaterializer(store, materializer)
			provisioner.filesystem = panicProvisionerStagingFileSystem{}

			caller := cloneProvisionerStateSandbox(stored)
			if err := provisioner.Ensure(context.Background(), caller); err != nil {
				t.Fatalf("Ensure returned error: %v", err)
			}
			assertProvisionerState(t, caller, domain.SandboxWorkspaceProvisioningStatusReady)
			assertProvisionerState(t, store.sandbox(t, stored.Summary.ID), domain.SandboxWorkspaceProvisioningStatusReady)
			if got := store.updateCount(); got != 1 {
				t.Fatalf("UpdateSandbox calls = %d, want 1", got)
			}
			if got := store.workspaceConfigCount(); got != 0 {
				t.Fatalf("GetWorkspaceConfig calls = %d, want 0", got)
			}
			if got := materializer.callCount(); got != 0 {
				t.Fatalf("Materialize calls = %d, want 0", got)
			}
			after := snapshotProvisionerStagingTree(t, root)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("legacy workspace tree changed:\n got: %#v\nwant: %#v", after, before)
			}
		})
	}
}

func TestProvisionerReadyReloadsMetadataWithoutSideEffects(t *testing.T) {
	t.Parallel()

	stored := provisionerStateSandbox("ready", "/tmp/ready-workspace", domain.SandboxWorkspaceProvisioningStatusReady)
	stored.Summary.Title = "persisted title"
	store := newProvisionerStateStore(stored)
	store.panicOnWorkspaceConfig = true
	materializer := &provisionerStateMaterializer{panicOnCall: true}
	provisioner := NewProvisionerWithMaterializer(store, materializer)

	caller := cloneProvisionerStateSandbox(stored)
	caller.Summary.Title = "stale caller title"
	caller.Summary.WorkspacePath = ""
	caller.RuntimeEnvItems = []domain.SandboxEnvVar{{Name: "RUNTIME_ONLY", Value: "runtime"}}
	caller.ExecutionProviderEnvItems = []domain.SandboxEnvVar{{Name: "PROVIDER_ONLY", Value: "provider", Secret: true}}
	if err := provisioner.Ensure(context.Background(), caller); err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	if caller.Summary.Title != stored.Summary.Title {
		t.Fatalf("caller title = %q, want reloaded value %q", caller.Summary.Title, stored.Summary.Title)
	}
	if caller.Summary.WorkspacePath != stored.Summary.WorkspacePath {
		t.Fatalf("caller workspace path = %q, want authoritative value %q", caller.Summary.WorkspacePath, stored.Summary.WorkspacePath)
	}
	assertProvisionerTransientEnv(t, caller)
	if got := store.updateCount(); got != 0 {
		t.Fatalf("UpdateSandbox calls = %d, want 0", got)
	}
	if got := store.workspaceConfigCount(); got != 0 {
		t.Fatalf("GetWorkspaceConfig calls = %d, want 0", got)
	}
	if got := materializer.callCount(); got != 0 {
		t.Fatalf("Materialize calls = %d, want 0", got)
	}
}

func TestProvisionerReadyDoesNotResolveWorkspaceConfig(t *testing.T) {
	t.Parallel()

	stored := provisionerStateSandbox("ready-config-guard", "/tmp/ready-config-guard", domain.SandboxWorkspaceProvisioningStatusReady)
	stored.Workspace = nil
	store := newProvisionerStateStore(stored)
	store.panicOnWorkspaceConfig = true
	provisioner := NewProvisioner(&appconfig.Config{}, store, store)

	caller := cloneProvisionerStateSandbox(stored)
	if err := provisioner.Ensure(context.Background(), caller); err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	assertProvisionerState(t, caller, domain.SandboxWorkspaceProvisioningStatusReady)
	if got := store.workspaceConfigCount(); got != 0 {
		t.Fatalf("GetWorkspaceConfig calls = %d, want 0", got)
	}
}

func TestProvisionerUnknownStateFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		provisioning *domain.SandboxWorkspaceProvisioning
	}{
		{
			name: "unknown version",
			provisioning: &domain.SandboxWorkspaceProvisioning{
				Version:   domain.SandboxWorkspaceProvisioningVersion + 1,
				Status:    domain.SandboxWorkspaceProvisioningStatusPending,
				UpdatedAt: time.Unix(100, 0).UTC(),
			},
		},
		{
			name: "unknown status",
			provisioning: &domain.SandboxWorkspaceProvisioning{
				Version:   domain.SandboxWorkspaceProvisioningVersion,
				Status:    "future-state",
				UpdatedAt: time.Unix(200, 0).UTC(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stored := provisionerStateSandbox("unknown-"+strings.ReplaceAll(tt.name, " ", "-"), "/tmp/unknown-workspace", "")
			stored.WorkspaceProvisioning = cloneProvisionerStateProvisioning(tt.provisioning)
			store := newProvisionerStateStore(stored)
			materializer := &provisionerStateMaterializer{}
			provisioner := NewProvisionerWithMaterializer(store, materializer)
			caller := cloneProvisionerStateSandbox(stored)
			before := cloneProvisionerStateSandbox(caller)

			if err := provisioner.Ensure(context.Background(), caller); err == nil {
				t.Fatal("Ensure error = nil, want fail-closed error")
			}
			if !reflect.DeepEqual(caller, before) {
				t.Fatalf("caller mutated on rejected state:\n got: %#v\nwant: %#v", caller, before)
			}
			if got := store.updateCount(); got != 0 {
				t.Fatalf("UpdateSandbox calls = %d, want 0", got)
			}
			if got := materializer.callCount(); got != 0 {
				t.Fatalf("Materialize calls = %d, want 0", got)
			}
			if persisted := store.sandbox(t, stored.Summary.ID); !reflect.DeepEqual(persisted, stored) {
				t.Fatalf("persisted sandbox mutated on rejected state:\n got: %#v\nwant: %#v", persisted, stored)
			}
		})
	}
}

func TestProvisionerPendingAndFailedMaterializeToReady(t *testing.T) {
	t.Parallel()

	for _, initialStatus := range []string{
		domain.SandboxWorkspaceProvisioningStatusPending,
		domain.SandboxWorkspaceProvisioningStatusFailed,
	} {
		initialStatus := initialStatus
		t.Run(initialStatus, func(t *testing.T) {
			t.Parallel()

			stored := provisionerStateSandbox(
				"materialize-"+initialStatus,
				filepath.Join(t.TempDir(), "workspace"),
				initialStatus,
			)
			store := newProvisionerStateStore(stored)
			store.persistedTitleOnUpdate = "title injected by persistent store"
			materializer := &provisionerStateMaterializer{}
			provisioner := NewProvisionerWithMaterializer(store, materializer)

			caller := cloneProvisionerStateSandbox(stored)
			caller.RuntimeEnvItems = []domain.SandboxEnvVar{{Name: "RUNTIME_ONLY", Value: "runtime"}}
			caller.ExecutionProviderEnvItems = []domain.SandboxEnvVar{{Name: "PROVIDER_ONLY", Value: "provider", Secret: true}}
			if err := provisioner.Ensure(context.Background(), caller); err != nil {
				t.Fatalf("Ensure returned error: %v", err)
			}
			assertProvisionerState(t, caller, domain.SandboxWorkspaceProvisioningStatusReady)
			assertProvisionerState(t, store.sandbox(t, stored.Summary.ID), domain.SandboxWorkspaceProvisioningStatusReady)
			if got := materializer.callCount(); got != 1 {
				t.Fatalf("Materialize calls = %d, want 1", got)
			}
			if caller.Summary.Title != store.persistedTitleOnUpdate {
				t.Fatalf("caller title = %q, want final reloaded value %q", caller.Summary.Title, store.persistedTitleOnUpdate)
			}
			assertProvisionerTransientEnv(t, caller)
		})
	}
}

func TestProvisionerMaterializerErrorStillReloadsCaller(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("materialize failed")
	stored := provisionerStateSandbox(
		"materializer-error",
		filepath.Join(t.TempDir(), "workspace"),
		domain.SandboxWorkspaceProvisioningStatusPending,
	)
	stored.Summary.Title = "persisted after failure"
	store := newProvisionerStateStore(stored)
	materializer := &provisionerStateMaterializer{err: wantErr}
	provisioner := NewProvisionerWithMaterializer(store, materializer)

	caller := cloneProvisionerStateSandbox(stored)
	caller.Summary.Title = "stale caller"
	caller.RuntimeEnvItems = []domain.SandboxEnvVar{{Name: "RUNTIME_ONLY", Value: "runtime"}}
	caller.ExecutionProviderEnvItems = []domain.SandboxEnvVar{{Name: "PROVIDER_ONLY", Value: "provider", Secret: true}}
	err := provisioner.Ensure(context.Background(), caller)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Ensure error = %v, want %v", err, wantErr)
	}
	if caller.Summary.Title != stored.Summary.Title {
		t.Fatalf("caller title = %q, want reloaded %q", caller.Summary.Title, stored.Summary.Title)
	}
	assertProvisionerState(t, caller, domain.SandboxWorkspaceProvisioningStatusFailed)
	assertProvisionerTransientEnv(t, caller)
}

func TestProvisionerValidatesSandboxIdentityAndWorkspacePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		sandbox *domain.Sandbox
	}{
		{name: "nil sandbox", sandbox: nil},
		{
			name: "empty sandbox ID",
			sandbox: provisionerStateSandbox(
				" ",
				"/tmp/workspace",
				domain.SandboxWorkspaceProvisioningStatusPending,
			),
		},
		{
			name: "empty workspace path",
			sandbox: provisionerStateSandbox(
				"empty-workspace-path",
				" ",
				domain.SandboxWorkspaceProvisioningStatusPending,
			),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := newProvisionerStateStore()
			if tt.sandbox != nil && strings.TrimSpace(tt.sandbox.Summary.ID) != "" {
				store.put(tt.sandbox)
			}
			materializer := &provisionerStateMaterializer{}
			provisioner := NewProvisionerWithMaterializer(store, materializer)
			before := cloneProvisionerStateSandbox(tt.sandbox)

			if err := provisioner.Ensure(context.Background(), tt.sandbox); err == nil {
				t.Fatal("Ensure error = nil, want validation error")
			}
			if !reflect.DeepEqual(tt.sandbox, before) {
				t.Fatalf("sandbox mutated after validation error:\n got: %#v\nwant: %#v", tt.sandbox, before)
			}
			if got := store.updateCount(); got != 0 {
				t.Fatalf("UpdateSandbox calls = %d, want 0", got)
			}
			if got := materializer.callCount(); got != 0 {
				t.Fatalf("Materialize calls = %d, want 0", got)
			}
		})
	}
}

type provisionerStateStore struct {
	mu                     sync.Mutex
	sandboxes              map[string]*domain.Sandbox
	updates                int
	workspaceConfigCalls   int
	panicOnWorkspaceConfig bool
	persistedTitleOnUpdate string
}

var _ WorkspaceConfigStore = (*provisionerStateStore)(nil)
var _ SandboxStore = (*provisionerStateStore)(nil)
var _ SandboxPathResolver = (*provisionerStateStore)(nil)

func newProvisionerStateStore(sandboxes ...*domain.Sandbox) *provisionerStateStore {
	store := &provisionerStateStore{sandboxes: make(map[string]*domain.Sandbox)}
	for _, sandbox := range sandboxes {
		store.put(sandbox)
	}
	return store
}

func (s *provisionerStateStore) GetWorkspaceConfig(_ context.Context, id string) (domain.WorkspaceConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaceConfigCalls++
	if s.panicOnWorkspaceConfig {
		panic("GetWorkspaceConfig must not be called")
	}
	return domain.WorkspaceConfig{ID: id, Type: "file", ConfigJSON: `{}`}, nil
}

func (s *provisionerStateStore) GetSandbox(_ context.Context, id string) (*domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sandbox, ok := s.sandboxes[id]
	if !ok {
		return nil, fmt.Errorf("sandbox %q not found", id)
	}
	return cloneProvisionerStateSandbox(sandbox), nil
}

func (s *provisionerStateStore) SandboxDir(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sandbox := s.sandboxes[id]; sandbox != nil {
		return filepath.Dir(sandbox.Summary.WorkspacePath)
	}
	return ""
}

func (s *provisionerStateStore) UpdateSandbox(_ context.Context, sandbox *domain.Sandbox) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates++
	persisted := cloneProvisionerStateSandbox(sandbox)
	if s.persistedTitleOnUpdate != "" {
		persisted.Summary.Title = s.persistedTitleOnUpdate
	}
	persisted.RuntimeEnvItems = nil
	persisted.ExecutionProviderEnvItems = nil
	s.sandboxes[sandbox.Summary.ID] = persisted
	return nil
}

func (s *provisionerStateStore) put(sandbox *domain.Sandbox) {
	if sandbox == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sandboxes[sandbox.Summary.ID] = cloneProvisionerStateSandbox(sandbox)
}

func (s *provisionerStateStore) sandbox(t *testing.T, id string) *domain.Sandbox {
	t.Helper()
	sandbox, err := s.GetSandbox(context.Background(), id)
	if err != nil {
		t.Fatalf("GetSandbox(%q): %v", id, err)
	}
	return sandbox
}

func (s *provisionerStateStore) updateCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updates
}

func (s *provisionerStateStore) workspaceConfigCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workspaceConfigCalls
}

type provisionerStateMaterializer struct {
	mu          sync.Mutex
	calls       int
	panicOnCall bool
	err         error
}

var _ WorkspaceMaterializer = (*provisionerStateMaterializer)(nil)

func (m *provisionerStateMaterializer) Materialize(_ context.Context, sandbox *domain.Sandbox) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.panicOnCall {
		panic("Materialize must not be called")
	}
	return m.err
}

func (m *provisionerStateMaterializer) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func provisionerStateSandbox(id, workspacePath, status string) *domain.Sandbox {
	sandbox := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:            id,
			Title:         "original title",
			WorkspacePath: workspacePath,
		},
		WorkspaceID: "workspace-1",
		Workspace: &domain.SandboxWorkspace{
			ID:         "workspace-1",
			Name:       "Workspace",
			Type:       "file",
			ConfigJSON: `{}`,
		},
	}
	if status != "" {
		sandbox.WorkspaceProvisioning = &domain.SandboxWorkspaceProvisioning{
			Version:   domain.SandboxWorkspaceProvisioningVersion,
			Status:    status,
			UpdatedAt: time.Unix(1, 0).UTC(),
		}
	}
	return sandbox
}

func cloneProvisionerStateSandbox(sandbox *domain.Sandbox) *domain.Sandbox {
	if sandbox == nil {
		return nil
	}
	clone := *sandbox
	clone.Summary.Tags = append([]domain.SandboxTag(nil), sandbox.Summary.Tags...)
	if sandbox.Workspace != nil {
		workspace := *sandbox.Workspace
		clone.Workspace = &workspace
	}
	clone.WorkspaceProvisioning = cloneProvisionerStateProvisioning(sandbox.WorkspaceProvisioning)
	clone.EnvItems = append([]domain.SandboxEnvVar(nil), sandbox.EnvItems...)
	clone.VolumeMounts = append([]domain.SandboxVolumeMount(nil), sandbox.VolumeMounts...)
	clone.RuntimeEnvItems = append([]domain.SandboxEnvVar(nil), sandbox.RuntimeEnvItems...)
	clone.ProviderEnvItems = append([]domain.SandboxEnvVar(nil), sandbox.ProviderEnvItems...)
	clone.ExecutionProviderEnvItems = append([]domain.SandboxEnvVar(nil), sandbox.ExecutionProviderEnvItems...)
	return &clone
}

func cloneProvisionerStateProvisioning(provisioning *domain.SandboxWorkspaceProvisioning) *domain.SandboxWorkspaceProvisioning {
	if provisioning == nil {
		return nil
	}
	clone := *provisioning
	return &clone
}

func assertProvisionerState(t *testing.T, sandbox *domain.Sandbox, want string) {
	t.Helper()
	if sandbox.WorkspaceProvisioning == nil {
		t.Fatalf("workspace provisioning = nil, want %q", want)
	}
	if sandbox.WorkspaceProvisioning.Version != domain.SandboxWorkspaceProvisioningVersion {
		t.Errorf("workspace provisioning version = %d, want %d", sandbox.WorkspaceProvisioning.Version, domain.SandboxWorkspaceProvisioningVersion)
	}
	if sandbox.WorkspaceProvisioning.Status != want {
		t.Errorf("workspace provisioning status = %q, want %q", sandbox.WorkspaceProvisioning.Status, want)
	}
	if sandbox.WorkspaceProvisioning.UpdatedAt.IsZero() {
		t.Error("workspace provisioning updated_at is zero")
	}
}

func assertProvisionerTransientEnv(t *testing.T, sandbox *domain.Sandbox) {
	t.Helper()
	wantRuntime := []domain.SandboxEnvVar{{Name: "RUNTIME_ONLY", Value: "runtime"}}
	wantProvider := []domain.SandboxEnvVar{{Name: "PROVIDER_ONLY", Value: "provider", Secret: true}}
	if !reflect.DeepEqual(sandbox.RuntimeEnvItems, wantRuntime) {
		t.Errorf("RuntimeEnvItems = %#v, want %#v", sandbox.RuntimeEnvItems, wantRuntime)
	}
	if !reflect.DeepEqual(sandbox.ExecutionProviderEnvItems, wantProvider) {
		t.Errorf("ExecutionProviderEnvItems = %#v, want %#v", sandbox.ExecutionProviderEnvItems, wantProvider)
	}
}
