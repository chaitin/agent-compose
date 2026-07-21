package workspaces

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	domain "agent-compose/pkg/model"
)

const provisionerConcurrencyTimeout = 5 * time.Second

func TestProvisionerConcurrentEnsureSameSandboxSingleMaterialization(t *testing.T) {
	const callerCount = 32

	sandboxID := "concurrent-same-sandbox"
	store := newConcurrentProvisionerStore(newConcurrentProvisionerSandbox(t, sandboxID))
	materializerStarted := make(chan string, 1)
	releaseMaterializer := make(chan struct{})
	materializer := &concurrentProvisionerMaterializer{
		started: materializerStarted,
		release: releaseMaterializer,
	}
	provisioner := NewProvisionerWithMaterializer(store, materializer)

	callers := make([]*domain.Sandbox, callerCount)
	results := make(chan concurrentProvisionerEnsureResult, callerCount)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(callerCount)
	for index := range callers {
		caller, err := store.GetSandbox(context.Background(), sandboxID)
		if err != nil {
			t.Fatalf("load caller %d: %v", index, err)
		}
		caller.RuntimeEnvItems = []domain.SandboxEnvVar{{Name: "RUNTIME_CALLER", Value: fmt.Sprintf("%d", index)}}
		caller.ExecutionProviderEnvItems = []domain.SandboxEnvVar{{Name: "PROVIDER_CALLER", Value: fmt.Sprintf("%d", index)}}
		callers[index] = caller

		go func(index int, sandbox *domain.Sandbox) {
			ready.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), provisionerConcurrencyTimeout)
			defer cancel()
			results <- concurrentProvisionerEnsureResult{index: index, err: provisioner.Ensure(ctx, sandbox)}
		}(index, caller)
	}
	ready.Wait()
	close(start)

	select {
	case gotID := <-materializerStarted:
		if gotID != sandboxID {
			t.Fatalf("materializer sandbox ID = %q, want %q", gotID, sandboxID)
		}
	case <-time.After(provisionerConcurrencyTimeout):
		t.Fatal("timed out waiting for shared materialization to start")
	}
	close(releaseMaterializer)

	for range callerCount {
		select {
		case result := <-results:
			if result.err != nil {
				t.Errorf("Ensure caller %d returned error: %v", result.index, result.err)
			}
		case <-time.After(provisionerConcurrencyTimeout):
			t.Fatal("timed out waiting for concurrent Ensure callers")
		}
	}

	if got := materializer.callCount(sandboxID); got != 1 {
		t.Fatalf("materializer calls for %q = %d, want 1", sandboxID, got)
	}
	if got := store.readyUpdateCount(sandboxID); got != 1 {
		t.Fatalf("ready updates for %q = %d, want 1", sandboxID, got)
	}
	for index, caller := range callers {
		assertConcurrentProvisionerCallerReady(t, caller, sandboxID, index)
	}
}

func TestProvisionerConcurrentEnsureDifferentSandboxesOverlap(t *testing.T) {
	sandboxIDs := []string{"concurrent-sandbox-a", "concurrent-sandbox-b"}
	store := newConcurrentProvisionerStore(
		newConcurrentProvisionerSandbox(t, sandboxIDs[0]),
		newConcurrentProvisionerSandbox(t, sandboxIDs[1]),
	)
	materializerStarted := make(chan string, len(sandboxIDs))
	releaseMaterializers := make(chan struct{})
	materializer := &concurrentProvisionerMaterializer{
		started: materializerStarted,
		release: releaseMaterializers,
	}
	provisioner := NewProvisionerWithMaterializer(store, materializer)

	callers := make([]*domain.Sandbox, len(sandboxIDs))
	results := make(chan concurrentProvisionerEnsureResult, len(sandboxIDs))
	start := make(chan struct{})
	for index, sandboxID := range sandboxIDs {
		caller, err := store.GetSandbox(context.Background(), sandboxID)
		if err != nil {
			t.Fatalf("load sandbox %q: %v", sandboxID, err)
		}
		callers[index] = caller
		go func(index int, sandbox *domain.Sandbox) {
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), provisionerConcurrencyTimeout)
			defer cancel()
			results <- concurrentProvisionerEnsureResult{index: index, err: provisioner.Ensure(ctx, sandbox)}
		}(index, caller)
	}
	close(start)

	startedIDs := make(map[string]struct{}, len(sandboxIDs))
	for range sandboxIDs {
		select {
		case sandboxID := <-materializerStarted:
			startedIDs[sandboxID] = struct{}{}
		case <-time.After(provisionerConcurrencyTimeout):
			close(releaseMaterializers)
			t.Fatal("timed out waiting for different sandboxes to materialize concurrently")
		}
	}
	close(releaseMaterializers)

	for _, sandboxID := range sandboxIDs {
		if _, ok := startedIDs[sandboxID]; !ok {
			t.Errorf("materializer did not start for sandbox %q", sandboxID)
		}
	}
	for range sandboxIDs {
		select {
		case result := <-results:
			if result.err != nil {
				t.Errorf("Ensure sandbox %q returned error: %v", sandboxIDs[result.index], result.err)
			}
		case <-time.After(provisionerConcurrencyTimeout):
			t.Fatal("timed out waiting for different-sandbox Ensure calls")
		}
	}
	for index, sandboxID := range sandboxIDs {
		if got := materializer.callCount(sandboxID); got != 1 {
			t.Errorf("materializer calls for %q = %d, want 1", sandboxID, got)
		}
		if got := store.readyUpdateCount(sandboxID); got != 1 {
			t.Errorf("ready updates for %q = %d, want 1", sandboxID, got)
		}
		assertConcurrentProvisionerCallerReady(t, callers[index], sandboxID, -1)
	}
}

func TestProvisionerConcurrentWaiterCancellationDoesNotCancelSharedAttempt(t *testing.T) {
	sandboxID := "concurrent-waiter-cancel"
	store := newConcurrentProvisionerStore(newConcurrentProvisionerSandbox(t, sandboxID))
	materializerStarted := make(chan string, 1)
	releaseMaterializer := make(chan struct{})
	materializer := &concurrentProvisionerMaterializer{
		started: materializerStarted,
		release: releaseMaterializer,
	}
	provisioner := NewProvisionerWithMaterializer(store, materializer)

	leader, err := store.GetSandbox(context.Background(), sandboxID)
	if err != nil {
		t.Fatalf("load leader: %v", err)
	}
	leaderResult := make(chan error, 1)
	go func() {
		leaderResult <- provisioner.Ensure(context.Background(), leader)
	}()
	select {
	case <-materializerStarted:
	case <-time.After(provisionerConcurrencyTimeout):
		t.Fatal("timed out waiting for leader materialization")
	}

	waiter, err := store.GetSandbox(context.Background(), sandboxID)
	if err != nil {
		t.Fatalf("load waiter: %v", err)
	}
	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	cancelWaiter()
	waiterResult := make(chan error, 1)
	go func() {
		waiterResult <- provisioner.Ensure(waiterCtx, waiter)
	}()
	select {
	case err := <-waiterResult:
		if err != context.Canceled {
			t.Fatalf("canceled waiter error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("canceled waiter remained blocked on shared attempt")
	}

	close(releaseMaterializer)
	select {
	case err := <-leaderResult:
		if err != nil {
			t.Fatalf("leader Ensure returned error: %v", err)
		}
	case <-time.After(provisionerConcurrencyTimeout):
		t.Fatal("timed out waiting for leader after waiter cancellation")
	}
	if got := materializer.callCount(sandboxID); got != 1 {
		t.Fatalf("materializer calls = %d, want 1", got)
	}
}

type concurrentProvisionerEnsureResult struct {
	index int
	err   error
}

type concurrentProvisionerStore struct {
	mu           sync.Mutex
	sandboxes    map[string]*domain.Sandbox
	readyUpdates map[string]int
}

var _ SandboxPathResolver = (*concurrentProvisionerStore)(nil)

var _ SandboxStore = (*concurrentProvisionerStore)(nil)

func newConcurrentProvisionerStore(sandboxes ...*domain.Sandbox) *concurrentProvisionerStore {
	store := &concurrentProvisionerStore{
		sandboxes:    make(map[string]*domain.Sandbox, len(sandboxes)),
		readyUpdates: make(map[string]int, len(sandboxes)),
	}
	for _, sandbox := range sandboxes {
		store.sandboxes[sandbox.Summary.ID] = cloneConcurrentProvisionerSandbox(sandbox)
	}
	return store
}

func (s *concurrentProvisionerStore) GetSandbox(_ context.Context, id string) (*domain.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sandbox, ok := s.sandboxes[id]
	if !ok {
		return nil, fmt.Errorf("sandbox %q not found", id)
	}
	return cloneConcurrentProvisionerSandbox(sandbox), nil
}

func (s *concurrentProvisionerStore) SandboxDir(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sandbox := s.sandboxes[id]; sandbox != nil {
		return filepath.Dir(sandbox.Summary.WorkspacePath)
	}
	return ""
}

func (s *concurrentProvisionerStore) UpdateSandbox(_ context.Context, sandbox *domain.Sandbox) error {
	if sandbox == nil {
		return fmt.Errorf("sandbox is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	persisted := cloneConcurrentProvisionerSandbox(sandbox)
	if persisted.WorkspaceProvisioning != nil &&
		persisted.WorkspaceProvisioning.Status == domain.SandboxWorkspaceProvisioningStatusReady {
		s.readyUpdates[persisted.Summary.ID]++
		persisted.Summary.Title = concurrentProvisionerPersistedTitle(persisted.Summary.ID)
	}
	s.sandboxes[persisted.Summary.ID] = persisted
	return nil
}

func (s *concurrentProvisionerStore) readyUpdateCount(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readyUpdates[id]
}

type concurrentProvisionerMaterializer struct {
	mu      sync.Mutex
	calls   map[string]int
	started chan<- string
	release <-chan struct{}
}

var _ WorkspaceMaterializer = (*concurrentProvisionerMaterializer)(nil)

func (m *concurrentProvisionerMaterializer) Materialize(ctx context.Context, sandbox *domain.Sandbox) error {
	if sandbox == nil {
		return fmt.Errorf("sandbox is nil")
	}
	m.mu.Lock()
	if m.calls == nil {
		m.calls = make(map[string]int)
	}
	m.calls[sandbox.Summary.ID]++
	m.mu.Unlock()

	select {
	case m.started <- sandbox.Summary.ID:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-m.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *concurrentProvisionerMaterializer) callCount(id string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[id]
}

func newConcurrentProvisionerSandbox(t *testing.T, id string) *domain.Sandbox {
	t.Helper()
	return &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:            id,
			Title:         "initial-title",
			WorkspacePath: filepath.Join(t.TempDir(), "workspace"),
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
			Status:    domain.SandboxWorkspaceProvisioningStatusPending,
			UpdatedAt: time.Now().UTC(),
		},
	}
}

func cloneConcurrentProvisionerSandbox(src *domain.Sandbox) *domain.Sandbox {
	if src == nil {
		return nil
	}
	dst := *src
	if src.Workspace != nil {
		workspace := *src.Workspace
		dst.Workspace = &workspace
	}
	if src.WorkspaceProvisioning != nil {
		provisioning := *src.WorkspaceProvisioning
		dst.WorkspaceProvisioning = &provisioning
	}
	dst.Summary.Tags = append([]domain.SandboxTag(nil), src.Summary.Tags...)
	dst.EnvItems = append([]domain.SandboxEnvVar(nil), src.EnvItems...)
	dst.VolumeMounts = append([]domain.SandboxVolumeMount(nil), src.VolumeMounts...)
	dst.RuntimeEnvItems = append([]domain.SandboxEnvVar(nil), src.RuntimeEnvItems...)
	dst.ProviderEnvItems = append([]domain.SandboxEnvVar(nil), src.ProviderEnvItems...)
	dst.ExecutionProviderEnvItems = append([]domain.SandboxEnvVar(nil), src.ExecutionProviderEnvItems...)
	return &dst
}

func assertConcurrentProvisionerCallerReady(t *testing.T, sandbox *domain.Sandbox, sandboxID string, callerIndex int) {
	t.Helper()
	if sandbox.WorkspaceProvisioning == nil {
		t.Fatalf("sandbox %q provisioning = nil, want ready", sandboxID)
	}
	if got := sandbox.WorkspaceProvisioning.Status; got != domain.SandboxWorkspaceProvisioningStatusReady {
		t.Errorf("sandbox %q provisioning status = %q, want ready", sandboxID, got)
	}
	if got, want := sandbox.Summary.Title, concurrentProvisionerPersistedTitle(sandboxID); got != want {
		t.Errorf("sandbox %q final title = %q, want %q", sandboxID, got, want)
	}
	if callerIndex < 0 {
		return
	}
	wantValue := fmt.Sprintf("%d", callerIndex)
	if len(sandbox.RuntimeEnvItems) != 1 || sandbox.RuntimeEnvItems[0].Name != "RUNTIME_CALLER" || sandbox.RuntimeEnvItems[0].Value != wantValue {
		t.Errorf("caller %d runtime env = %#v, want its transient value", callerIndex, sandbox.RuntimeEnvItems)
	}
	if len(sandbox.ExecutionProviderEnvItems) != 1 || sandbox.ExecutionProviderEnvItems[0].Name != "PROVIDER_CALLER" || sandbox.ExecutionProviderEnvItems[0].Value != wantValue {
		t.Errorf("caller %d provider env = %#v, want its transient value", callerIndex, sandbox.ExecutionProviderEnvItems)
	}
}

func concurrentProvisionerPersistedTitle(sandboxID string) string {
	return "persisted-ready-" + sandboxID
}
