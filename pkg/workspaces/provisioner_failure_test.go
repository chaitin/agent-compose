package workspaces

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	domain "agent-compose/pkg/model"
)

func TestProvisionerFailedRetryPendingPersistenceFailureStopsMaterializer(t *testing.T) {
	t.Parallel()

	persistErr := errors.New("persist pending failed")
	stored := newProvisionerFailureSandbox(t, "failed-pending-persist", domain.SandboxWorkspaceProvisioningStatusFailed)
	store := newProvisionerFailureStore(stored)
	store.failNextUpdate(domain.SandboxWorkspaceProvisioningStatusPending, persistErr)
	materializer := &provisionerFailureMaterializer{}
	provisioner := NewProvisionerWithMaterializer(store, materializer)
	provisioner.filesystem = panicProvisionerStagingFileSystem{}
	caller := cloneProvisionerFailureSandbox(stored)

	err := provisioner.Ensure(context.Background(), caller)
	if !errors.Is(err, persistErr) {
		t.Fatalf("Ensure error = %v, want errors.Is(..., %v)", err, persistErr)
	}
	if got := materializer.callCount(); got != 0 {
		t.Fatalf("Materialize calls = %d, want 0 after pending persistence failure", got)
	}
	assertProvisionerFailureStatus(t, caller, domain.SandboxWorkspaceProvisioningStatusFailed)
	assertProvisionerFailureStatus(t, store.sandbox(t), domain.SandboxWorkspaceProvisioningStatusFailed)
	if got, want := store.updateStatuses(), []string{domain.SandboxWorkspaceProvisioningStatusPending}; !reflect.DeepEqual(got, want) {
		t.Fatalf("UpdateSandbox statuses = %v, want %v", got, want)
	}
}

func TestProvisionerMaterializerFailurePersistsFailed(t *testing.T) {
	t.Parallel()

	materializeErr := errors.New("materialize workspace failed")
	stored := newProvisionerFailureSandbox(t, "materializer-failure", domain.SandboxWorkspaceProvisioningStatusPending)
	store := newProvisionerFailureStore(stored)
	materializer := &provisionerFailureMaterializer{err: materializeErr}
	provisioner := NewProvisionerWithMaterializer(store, materializer)
	caller := cloneProvisionerFailureSandbox(stored)

	err := provisioner.Ensure(context.Background(), caller)
	if !errors.Is(err, materializeErr) {
		t.Fatalf("Ensure error = %v, want errors.Is(..., %v)", err, materializeErr)
	}
	if got := materializer.callCount(); got != 1 {
		t.Fatalf("Materialize calls = %d, want 1", got)
	}
	assertProvisionerFailureStatus(t, caller, domain.SandboxWorkspaceProvisioningStatusFailed)
	assertProvisionerFailureStatus(t, store.sandbox(t), domain.SandboxWorkspaceProvisioningStatusFailed)
	if got, want := store.updateStatuses(), []string{domain.SandboxWorkspaceProvisioningStatusFailed}; !reflect.DeepEqual(got, want) {
		t.Fatalf("UpdateSandbox statuses = %v, want %v", got, want)
	}
}

func TestProvisionerMaterializerFailureJoinsFailedPersistenceError(t *testing.T) {
	t.Parallel()

	materializeErr := errors.New("materialize workspace failed")
	persistErr := errors.New("persist failed status failed")
	stored := newProvisionerFailureSandbox(t, "materializer-and-persist-failure", domain.SandboxWorkspaceProvisioningStatusPending)
	store := newProvisionerFailureStore(stored)
	store.failNextUpdate(domain.SandboxWorkspaceProvisioningStatusFailed, persistErr)
	materializer := &provisionerFailureMaterializer{err: materializeErr}
	provisioner := NewProvisionerWithMaterializer(store, materializer)
	caller := cloneProvisionerFailureSandbox(stored)

	err := provisioner.Ensure(context.Background(), caller)
	if !errors.Is(err, materializeErr) {
		t.Errorf("Ensure error = %v, want original materializer error %v", err, materializeErr)
	}
	if !errors.Is(err, persistErr) {
		t.Errorf("Ensure error = %v, want failed-state persistence error %v", err, persistErr)
	}
	if got := materializer.callCount(); got != 1 {
		t.Fatalf("Materialize calls = %d, want 1", got)
	}
	assertProvisionerFailureStatus(t, caller, domain.SandboxWorkspaceProvisioningStatusPending)
	assertProvisionerFailureStatus(t, store.sandbox(t), domain.SandboxWorkspaceProvisioningStatusPending)
	if got, want := store.updateStatuses(), []string{domain.SandboxWorkspaceProvisioningStatusFailed}; !reflect.DeepEqual(got, want) {
		t.Fatalf("UpdateSandbox statuses = %v, want %v", got, want)
	}
}

func TestProvisionerFailedRetryRepairedMaterializerReachesReady(t *testing.T) {
	t.Parallel()

	materializeErr := errors.New("source temporarily unavailable")
	stored := newProvisionerFailureSandbox(t, "repaired-materializer", domain.SandboxWorkspaceProvisioningStatusPending)
	store := newProvisionerFailureStore(stored)
	materializer := &provisionerFailureMaterializer{err: materializeErr}
	provisioner := NewProvisionerWithMaterializer(store, materializer)
	firstCaller := cloneProvisionerFailureSandbox(stored)

	err := provisioner.Ensure(context.Background(), firstCaller)
	if !errors.Is(err, materializeErr) {
		t.Fatalf("first Ensure error = %v, want errors.Is(..., %v)", err, materializeErr)
	}
	assertProvisionerFailureStatus(t, firstCaller, domain.SandboxWorkspaceProvisioningStatusFailed)
	assertProvisionerFailureStatus(t, store.sandbox(t), domain.SandboxWorkspaceProvisioningStatusFailed)

	materializer.setBehavior(nil, materializeWorkspaceSentinel("repaired"))
	secondCaller := cloneProvisionerFailureSandbox(firstCaller)
	if err := provisioner.Ensure(context.Background(), secondCaller); err != nil {
		t.Fatalf("second Ensure returned error after repair: %v", err)
	}
	assertProvisionerFailureStatus(t, secondCaller, domain.SandboxWorkspaceProvisioningStatusReady)
	assertProvisionerFailureStatus(t, store.sandbox(t), domain.SandboxWorkspaceProvisioningStatusReady)
	if got := materializer.callCount(); got != 2 {
		t.Fatalf("Materialize calls = %d, want 2 across failure and retry", got)
	}
	if got, want := store.updateStatuses(), []string{
		domain.SandboxWorkspaceProvisioningStatusFailed,
		domain.SandboxWorkspaceProvisioningStatusPending,
		domain.SandboxWorkspaceProvisioningStatusReady,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("UpdateSandbox statuses = %v, want %v", got, want)
	}
	assertProvisionerFailureWorkspaceFile(t, secondCaller.Summary.WorkspacePath, "repaired")
}

func TestProvisionerReadyPersistenceFailureLeavesAuthoritativePendingAndRetries(t *testing.T) {
	t.Parallel()

	persistErr := errors.New("persist ready failed")
	stored := newProvisionerFailureSandbox(t, "ready-persist-failure", domain.SandboxWorkspaceProvisioningStatusPending)
	store := newProvisionerFailureStore(stored)
	store.failNextUpdate(domain.SandboxWorkspaceProvisioningStatusReady, persistErr)
	materializer := &provisionerFailureMaterializer{fn: materializeWorkspaceSentinel("first-attempt")}
	provisioner := NewProvisionerWithMaterializer(store, materializer)
	firstCaller := cloneProvisionerFailureSandbox(stored)

	err := provisioner.Ensure(context.Background(), firstCaller)
	if !errors.Is(err, persistErr) {
		t.Fatalf("first Ensure error = %v, want errors.Is(..., %v)", err, persistErr)
	}
	assertProvisionerFailureStatus(t, firstCaller, domain.SandboxWorkspaceProvisioningStatusPending)
	assertProvisionerFailureStatus(t, store.sandbox(t), domain.SandboxWorkspaceProvisioningStatusPending)
	if got := materializer.callCount(); got != 1 {
		t.Fatalf("Materialize calls after ready persistence failure = %d, want 1", got)
	}

	materializer.setBehavior(nil, materializeWorkspaceSentinel("second-attempt"))
	secondCaller := cloneProvisionerFailureSandbox(firstCaller)
	if err := provisioner.Ensure(context.Background(), secondCaller); err != nil {
		t.Fatalf("retry Ensure returned error: %v", err)
	}
	assertProvisionerFailureStatus(t, secondCaller, domain.SandboxWorkspaceProvisioningStatusReady)
	assertProvisionerFailureStatus(t, store.sandbox(t), domain.SandboxWorkspaceProvisioningStatusReady)
	if got := materializer.callCount(); got != 2 {
		t.Fatalf("Materialize calls after retry = %d, want 2", got)
	}
	if got, want := store.updateStatuses(), []string{
		domain.SandboxWorkspaceProvisioningStatusReady,
		domain.SandboxWorkspaceProvisioningStatusReady,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("UpdateSandbox statuses = %v, want %v", got, want)
	}
	assertProvisionerFailureWorkspaceFile(t, secondCaller.Summary.WorkspacePath, "second-attempt")
}

func TestProvisionerPromotionFailurePreservesPrimaryAndSecondaryErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		cleanupErr         error
		persistErr         error
		wantPersistedState string
	}{
		{
			name:               "cleanup failure is joined and failed state persists",
			cleanupErr:         errors.New("cleanup attempt failed"),
			wantPersistedState: domain.SandboxWorkspaceProvisioningStatusFailed,
		},
		{
			name:               "failed-state persistence failure is joined",
			persistErr:         errors.New("persist failed state failed"),
			wantPersistedState: domain.SandboxWorkspaceProvisioningStatusPending,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			promotionErr := errors.New("rename promotion failed")
			stored := newProvisionerFailureSandbox(t, "promotion-"+tt.wantPersistedState, domain.SandboxWorkspaceProvisioningStatusPending)
			store := newProvisionerFailureStore(stored)
			if tt.persistErr != nil {
				store.failNextUpdate(domain.SandboxWorkspaceProvisioningStatusFailed, tt.persistErr)
			}
			materializer := &provisionerFailureMaterializer{fn: materializeWorkspaceSentinel("staged")}
			provisioner := NewProvisionerWithMaterializer(store, materializer)
			filesystem := &provisionerFailureFileSystem{
				renameErr:  promotionErr,
				cleanupErr: tt.cleanupErr,
			}
			provisioner.filesystem = filesystem
			caller := cloneProvisionerFailureSandbox(stored)

			err := provisioner.Ensure(context.Background(), caller)
			if !errors.Is(err, promotionErr) {
				t.Errorf("Ensure error = %v, want original promotion error %v", err, promotionErr)
			}
			if tt.cleanupErr != nil && !errors.Is(err, tt.cleanupErr) {
				t.Errorf("Ensure error = %v, want cleanup error %v", err, tt.cleanupErr)
			}
			if tt.persistErr != nil && !errors.Is(err, tt.persistErr) {
				t.Errorf("Ensure error = %v, want failed-state persistence error %v", err, tt.persistErr)
			}
			if got := materializer.callCount(); got != 1 {
				t.Fatalf("Materialize calls = %d, want 1", got)
			}
			if got := filesystem.renameCallCount(); got != 1 {
				t.Fatalf("Rename calls = %d, want 1", got)
			}
			assertProvisionerFailureStatus(t, caller, tt.wantPersistedState)
			assertProvisionerFailureStatus(t, store.sandbox(t), tt.wantPersistedState)
			if got, want := store.updateStatuses(), []string{domain.SandboxWorkspaceProvisioningStatusFailed}; !reflect.DeepEqual(got, want) {
				t.Fatalf("UpdateSandbox statuses = %v, want %v", got, want)
			}
		})
	}
}

func TestProvisionerPromotionWorkspaceRemovalFailureSkipsRename(t *testing.T) {
	t.Parallel()

	removeErr := errors.New("remove incomplete formal workspace failed")
	stored := newProvisionerFailureSandbox(t, "promotion-remove-failure", domain.SandboxWorkspaceProvisioningStatusPending)
	store := newProvisionerFailureStore(stored)
	materializer := &provisionerFailureMaterializer{fn: materializeWorkspaceSentinel("staged")}
	provisioner := NewProvisionerWithMaterializer(store, materializer)
	filesystem := &provisionerFailureFileSystem{formalRemoveErr: removeErr}
	provisioner.filesystem = filesystem
	caller := cloneProvisionerFailureSandbox(stored)

	err := provisioner.Ensure(context.Background(), caller)
	if !errors.Is(err, removeErr) {
		t.Fatalf("Ensure error = %v, want formal workspace removal error %v", err, removeErr)
	}
	if got := filesystem.renameCallCount(); got != 0 {
		t.Fatalf("Rename calls = %d, want 0 after formal workspace removal failure", got)
	}
	assertProvisionerFailureStatus(t, caller, domain.SandboxWorkspaceProvisioningStatusFailed)
	assertProvisionerFailureStatus(t, store.sandbox(t), domain.SandboxWorkspaceProvisioningStatusFailed)
}

type provisionerFailureStore struct {
	mu             sync.Mutex
	sandboxValue   *domain.Sandbox
	updateFailures map[string][]error
	updates        []string
}

var _ SandboxStore = (*provisionerFailureStore)(nil)
var _ SandboxPathResolver = (*provisionerFailureStore)(nil)

func newProvisionerFailureStore(sandbox *domain.Sandbox) *provisionerFailureStore {
	return &provisionerFailureStore{
		sandboxValue:   cloneProvisionerFailureSandbox(sandbox),
		updateFailures: make(map[string][]error),
	}
}

func (s *provisionerFailureStore) GetSandbox(_ context.Context, id string) (*domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sandboxValue == nil || s.sandboxValue.Summary.ID != id {
		return nil, fmt.Errorf("sandbox %q not found", id)
	}
	return cloneProvisionerFailureSandbox(s.sandboxValue), nil
}

func (s *provisionerFailureStore) SandboxDir(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sandboxValue != nil && s.sandboxValue.Summary.ID == id {
		return filepath.Dir(s.sandboxValue.Summary.WorkspacePath)
	}
	return ""
}

func (s *provisionerFailureStore) UpdateSandbox(_ context.Context, sandbox *domain.Sandbox) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sandbox == nil || sandbox.WorkspaceProvisioning == nil {
		return errors.New("sandbox provisioning state is required")
	}
	status := sandbox.WorkspaceProvisioning.Status
	s.updates = append(s.updates, status)
	if failures := s.updateFailures[status]; len(failures) > 0 {
		err := failures[0]
		s.updateFailures[status] = failures[1:]
		if err != nil {
			return err
		}
	}
	s.sandboxValue = cloneProvisionerFailureSandbox(sandbox)
	s.sandboxValue.RuntimeEnvItems = nil
	s.sandboxValue.ExecutionProviderEnvItems = nil
	return nil
}

func (s *provisionerFailureStore) failNextUpdate(status string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateFailures[status] = append(s.updateFailures[status], err)
}

func (s *provisionerFailureStore) sandbox(t *testing.T) *domain.Sandbox {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProvisionerFailureSandbox(s.sandboxValue)
}

func (s *provisionerFailureStore) updateStatuses() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.updates...)
}

type provisionerFailureMaterializer struct {
	mu    sync.Mutex
	calls int
	err   error
	fn    func(*domain.Sandbox) error
}

type provisionerFailureFileSystem struct {
	osProvisioningFileSystem

	mu              sync.Mutex
	renameErr       error
	cleanupErr      error
	formalRemoveErr error
	renameCalls     int
}

var _ provisioningFileSystem = (*provisionerFailureFileSystem)(nil)

func (f *provisionerFailureFileSystem) Rename(_, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renameCalls++
	return f.renameErr
}

func (f *provisionerFailureFileSystem) RemoveAll(path string) error {
	f.mu.Lock()
	renameAttempted := f.renameCalls > 0
	cleanupErr := f.cleanupErr
	formalRemoveErr := f.formalRemoveErr
	f.mu.Unlock()
	if filepath.Base(path) == "workspace" && formalRemoveErr != nil {
		return formalRemoveErr
	}
	if renameAttempted && filepath.Base(path) != "workspace" && cleanupErr != nil {
		return cleanupErr
	}
	return f.osProvisioningFileSystem.RemoveAll(path)
}

func (f *provisionerFailureFileSystem) renameCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.renameCalls
}

var _ WorkspaceMaterializer = (*provisionerFailureMaterializer)(nil)

func (m *provisionerFailureMaterializer) Materialize(_ context.Context, sandbox *domain.Sandbox) error {
	m.mu.Lock()
	m.calls++
	err := m.err
	fn := m.fn
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if fn != nil {
		return fn(sandbox)
	}
	return nil
}

func (m *provisionerFailureMaterializer) setBehavior(err error, fn func(*domain.Sandbox) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
	m.fn = fn
}

func (m *provisionerFailureMaterializer) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func newProvisionerFailureSandbox(t *testing.T, id, status string) *domain.Sandbox {
	t.Helper()
	root := t.TempDir()
	return &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:            id,
			WorkspacePath: filepath.Join(root, "workspace"),
		},
		WorkspaceID: "workspace-" + id,
		Workspace: &domain.SandboxWorkspace{
			ID:         "workspace-" + id,
			Name:       "Workspace " + id,
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

func cloneProvisionerFailureSandbox(sandbox *domain.Sandbox) *domain.Sandbox {
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

func materializeWorkspaceSentinel(content string) func(*domain.Sandbox) error {
	return func(sandbox *domain.Sandbox) error {
		if sandbox == nil {
			return errors.New("sandbox is nil")
		}
		if err := os.MkdirAll(sandbox.Summary.WorkspacePath, 0o755); err != nil {
			return fmt.Errorf("create materializer workspace: %w", err)
		}
		if err := os.WriteFile(filepath.Join(sandbox.Summary.WorkspacePath, "sentinel.txt"), []byte(content), 0o644); err != nil {
			return fmt.Errorf("write materializer sentinel: %w", err)
		}
		return nil
	}
}

func assertProvisionerFailureStatus(t *testing.T, sandbox *domain.Sandbox, want string) {
	t.Helper()
	if sandbox == nil || sandbox.WorkspaceProvisioning == nil {
		t.Fatalf("workspace provisioning = nil, want %q", want)
	}
	if got := sandbox.WorkspaceProvisioning.Status; got != want {
		t.Fatalf("workspace provisioning status = %q, want %q", got, want)
	}
}

func assertProvisionerFailureWorkspaceFile(t *testing.T, workspacePath, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(workspacePath, "sentinel.txt"))
	if err != nil {
		t.Fatalf("read promoted workspace sentinel: %v", err)
	}
	if got := string(data); got != want {
		t.Fatalf("promoted workspace sentinel = %q, want %q", got, want)
	}
}
