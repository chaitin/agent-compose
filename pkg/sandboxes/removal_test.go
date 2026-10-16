package sandboxes_test

import (
	"context"
	"errors"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

func TestRemovalCoordinatorPersistsAndResumesStages(t *testing.T) {
	root := t.TempDir()
	sandbox := removalTestSandbox(t, removalTestSandboxSpec{
		Root:    root,
		ID:      "sandbox-stage",
		Status:  domain.VMStatusRunning,
		Updated: time.Now().Add(-time.Hour),
	})
	store := &removalTestStore{sandboxes: map[string]*domain.Sandbox{sandbox.Summary.ID: sandbox}}
	runtime := &removalTestRuntime{removeErrors: []error{errors.New("injected remove failure")}}
	coordinator := &sandboxes.RemovalCoordinator{SandboxRoot: root, Store: store, Runtime: runtime}

	if _, err := coordinator.Remove(context.Background(), sandbox.Summary.ID, false); !errors.Is(err, sandboxes.ErrSandboxRunning) {
		t.Fatalf("Remove running without force error = %v", err)
	}
	if _, err := coordinator.Remove(context.Background(), sandbox.Summary.ID, true); err == nil {
		t.Fatal("Remove returned nil for injected runtime failure")
	}
	record, err := sandboxes.ReadOwnershipRecord(root, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("ReadOwnershipRecord: %v", err)
	}
	// A failed runtime removal must not leave the record in DELETING: nothing was
	// removed, so the pre-deletion state is restored and the failure is still
	// reported. Leaving it in DELETING is the stuck state of issue #772.
	if record.LifecycleState != "active" || record.StageCompleted(sandboxes.DeletionStageRuntime) {
		t.Fatalf("record after failure = %#v", record)
	}
	if store.sandboxes[sandbox.Summary.ID].Summary.VMStatus != domain.VMStatusStopped {
		t.Fatalf("sandbox status = %q", store.sandboxes[sandbox.Summary.ID].Summary.VMStatus)
	}

	result, err := coordinator.Remove(context.Background(), sandbox.Summary.ID, true)
	if err != nil || !result.Removed {
		t.Fatalf("resumed Remove result=%#v err=%v", result, err)
	}
	if runtime.stopCalls != 1 || runtime.removeCalls != 2 || store.removeCalls != 1 {
		t.Fatalf("calls stop/remove/store = %d/%d/%d", runtime.stopCalls, runtime.removeCalls, store.removeCalls)
	}
	if _, err := sandboxes.ReadOwnershipRecord(root, sandbox.Summary.ID); !os.IsNotExist(err) {
		t.Fatalf("ownership record remains: %v", err)
	}
}

// A permanently failing runtime removal must not strand the sandbox in DELETING.
// Nothing was removed, so every attempt returns to the same recoverable state and
// keeps reporting the failure, which leaves `sandbox rm` retryable and keeps the
// record out of an intermediate state that no product path clears.
func TestRemovalCoordinatorRollsBackDeletionIntentWhenRuntimeRemovalFails(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		stopErr    error
		removeErr  error
		wantStatus string
		wantErr    string
	}{
		{
			name: "runtime removal fails while stopped", status: domain.VMStatusStopped,
			removeErr:  errors.New("invalid configuration: no configuration has been provided"),
			wantStatus: domain.VMStatusStopped, wantErr: "remove sandbox runtime",
		},
		{
			name: "runtime removal fails after stopping", status: domain.VMStatusRunning,
			removeErr:  errors.New("invalid configuration: no configuration has been provided"),
			wantStatus: domain.VMStatusStopped, wantErr: "remove sandbox runtime",
		},
		{
			name: "runtime removal fails while failed", status: domain.VMStatusFailed,
			removeErr:  errors.New("invalid configuration: no configuration has been provided"),
			wantStatus: domain.VMStatusFailed, wantErr: "remove sandbox runtime",
		},
		{
			name: "runtime stop fails", status: domain.VMStatusRunning,
			stopErr:    errors.New("stop unavailable"),
			wantStatus: domain.VMStatusRunning, wantErr: "stop sandbox runtime",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			sandbox := removalTestSandbox(t, removalTestSandboxSpec{
				Root: root, ID: "sandbox-rollback", Status: tt.status, Updated: time.Now().Add(-time.Hour),
			})
			store := &removalTestStore{sandboxes: map[string]*domain.Sandbox{sandbox.Summary.ID: sandbox}}
			runtime := &removalTestRuntime{}
			if tt.stopErr != nil {
				runtime.stopErrors = []error{tt.stopErr, tt.stopErr}
			}
			if tt.removeErr != nil {
				runtime.removeErrors = []error{tt.removeErr, tt.removeErr}
			}
			coordinator := &sandboxes.RemovalCoordinator{SandboxRoot: root, Store: store, Runtime: runtime}

			// Two consecutive failures must converge on the same state instead
			// of leaving a DELETING record behind after the first one.
			for attempt := 1; attempt <= 2; attempt++ {
				_, err := coordinator.Remove(context.Background(), sandbox.Summary.ID, true)
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("attempt %d error = %v, want %q", attempt, err, tt.wantErr)
				}
				record, err := sandboxes.ReadOwnershipRecord(root, sandbox.Summary.ID)
				if err != nil {
					t.Fatalf("ReadOwnershipRecord after attempt %d: %v", attempt, err)
				}
				if record.LifecycleState == "deleting" || record.StageCompleted(sandboxes.DeletionStageRuntime) {
					t.Fatalf("record stuck in deletion after attempt %d: %#v", attempt, record)
				}
				stored, err := store.GetSandbox(context.Background(), sandbox.Summary.ID)
				if err != nil {
					t.Fatalf("GetSandbox after attempt %d: %v", attempt, err)
				}
				if stored.Summary.VMStatus != tt.wantStatus {
					t.Fatalf("sandbox status after attempt %d = %q, want %q", attempt, stored.Summary.VMStatus, tt.wantStatus)
				}
			}
		})
	}
}

// A deletion resumed from a journal must restore the predecessor the intent
// recorded, including states such as FAILED. Guessing the predecessor would
// rewrite a failed sandbox into STOPPED even though nothing was removed.
func TestRemovalCoordinatorRollbackRestoresRecordedPredecessorStatus(t *testing.T) {
	root := t.TempDir()
	sandbox := removalTestSandbox(t, removalTestSandboxSpec{
		Root: root, ID: "sandbox-resumed", Status: domain.VMStatusDeleting, Updated: time.Now().Add(-time.Hour),
	})
	store := &removalTestStore{sandboxes: map[string]*domain.Sandbox{sandbox.Summary.ID: sandbox}}
	runtime := &removalTestRuntime{removeErrors: []error{errors.New("cluster unreachable")}}
	coordinator := &sandboxes.RemovalCoordinator{SandboxRoot: root, Store: store, Runtime: runtime}

	// Simulate a daemon that persisted the deletion intent and exited before the
	// runtime removal ran: the sandbox shows DELETING and only the journal knows
	// what it was before.
	if err := sandboxes.WriteOwnershipRecord(root, sandboxes.OwnershipRecord{
		Version: sandboxes.OwnershipRecordVersion, SandboxID: sandbox.Summary.ID, Driver: "docker",
		RuntimeID: sandbox.Summary.RuntimeRef, SandboxPath: filepath.Dir(sandbox.Summary.WorkspacePath),
		LifecycleState: "deleting", PreviousVMStatus: domain.VMStatusFailed,
		CompletedStages: []sandboxes.DeletionStage{sandboxes.DeletionStageIntent},
	}); err != nil {
		t.Fatalf("WriteOwnershipRecord: %v", err)
	}

	if _, err := coordinator.Remove(context.Background(), sandbox.Summary.ID, true); err == nil {
		t.Fatal("Remove returned nil for injected runtime failure")
	}
	stored, err := store.GetSandbox(context.Background(), sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("GetSandbox: %v", err)
	}
	if stored.Summary.VMStatus != domain.VMStatusFailed {
		t.Fatalf("sandbox status after rolled-back resume = %q, want %q", stored.Summary.VMStatus, domain.VMStatusFailed)
	}
	record, err := sandboxes.ReadOwnershipRecord(root, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("ReadOwnershipRecord: %v", err)
	}
	if record.LifecycleState != "active" || record.StageCompleted(sandboxes.DeletionStageRuntime) {
		t.Fatalf("record after rolled-back resume = %#v", record)
	}
}

// A journal written before the predecessor status was recorded cannot name it,
// but it must still roll back to a real, retryable lifecycle state instead of
// stranding the sandbox in DELETING.
func TestRemovalCoordinatorRollbackFallsBackForPredecessorlessJournal(t *testing.T) {
	root := t.TempDir()
	sandbox := removalTestSandbox(t, removalTestSandboxSpec{
		Root: root, ID: "sandbox-legacy", Status: domain.VMStatusDeleting, Updated: time.Now().Add(-time.Hour),
	})
	store := &removalTestStore{sandboxes: map[string]*domain.Sandbox{sandbox.Summary.ID: sandbox}}
	runtime := &removalTestRuntime{removeErrors: []error{errors.New("cluster unreachable")}}
	coordinator := &sandboxes.RemovalCoordinator{SandboxRoot: root, Store: store, Runtime: runtime}

	if err := sandboxes.WriteOwnershipRecord(root, sandboxes.OwnershipRecord{
		Version: sandboxes.OwnershipRecordVersion, SandboxID: sandbox.Summary.ID, Driver: "docker",
		RuntimeID: sandbox.Summary.RuntimeRef, SandboxPath: filepath.Dir(sandbox.Summary.WorkspacePath),
		LifecycleState: "deleting", CompletedStages: []sandboxes.DeletionStage{sandboxes.DeletionStageIntent},
	}); err != nil {
		t.Fatalf("WriteOwnershipRecord: %v", err)
	}

	if _, err := coordinator.Remove(context.Background(), sandbox.Summary.ID, true); err == nil {
		t.Fatal("Remove returned nil for injected runtime failure")
	}
	stored, err := store.GetSandbox(context.Background(), sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("GetSandbox: %v", err)
	}
	if stored.Summary.VMStatus != domain.VMStatusStopped {
		t.Fatalf("sandbox status after rolled-back legacy journal = %q, want %q", stored.Summary.VMStatus, domain.VMStatusStopped)
	}
}

func TestRemovalCoordinatorRecoveryOnlyProcessesDeletingRecords(t *testing.T) {
	root := t.TempDir()
	deleting := removalTestSandbox(t, removalTestSandboxSpec{
		Root:    root,
		ID:      "sandbox-deleting",
		Status:  domain.VMStatusStopped,
		Updated: time.Now(),
	})
	ordinary := removalTestSandbox(t, removalTestSandboxSpec{
		Root:    root,
		ID:      "sandbox-ordinary",
		Status:  domain.VMStatusStopped,
		Updated: time.Now(),
	})
	store := &removalTestStore{sandboxes: map[string]*domain.Sandbox{deleting.Summary.ID: deleting, ordinary.Summary.ID: ordinary}}
	deletingRecord, _ := sandboxes.ReadOwnershipRecord(root, deleting.Summary.ID)
	deletingRecord.LifecycleState = "deleting"
	deletingRecord.Complete(sandboxes.DeletionStageIntent)
	if err := sandboxes.WriteOwnershipRecord(root, deletingRecord); err != nil {
		t.Fatal(err)
	}
	coordinator := &sandboxes.RemovalCoordinator{SandboxRoot: root, Store: store, Runtime: &removalTestRuntime{}}
	warnings := coordinator.Recover(context.Background())
	if len(warnings) != 0 {
		t.Fatalf("Recover warnings = %#v", warnings)
	}
	if _, ok := store.sandboxes[deleting.Summary.ID]; ok {
		t.Fatal("deleting sandbox was not recovered")
	}
	if _, ok := store.sandboxes[ordinary.Summary.ID]; !ok {
		t.Fatal("ordinary ownership record was removed during recovery")
	}
}

func TestRemovalCoordinatorRejectsInvalidOwnershipRecord(t *testing.T) {
	tests := []struct {
		name    string
		record  func(t *testing.T, root, sandboxID string)
		wantErr string
	}{
		{
			name: "corrupt json",
			record: func(t *testing.T, root, sandboxID string) {
				t.Helper()
				path, err := sandboxes.OwnershipRecordPath(root, sandboxID)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "lifecycle record is invalid",
		},
		{
			name: "path escape",
			record: func(t *testing.T, root, sandboxID string) {
				t.Helper()
				path, err := sandboxes.OwnershipRecordPath(root, sandboxID)
				if err != nil {
					t.Fatal(err)
				}
				data := []byte(`{"version":1,"sandbox_id":"` + sandboxID + `","driver":"docker","runtime_id":"runtime","sandbox_path":"/tmp/outside","lifecycle_state":"active"}`)
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "outside sandbox root",
		},
		{
			name: "symlink escape",
			record: func(t *testing.T, root, sandboxID string) {
				t.Helper()
				outside := t.TempDir()
				escape := filepath.Join(root, "escape")
				if err := os.Symlink(outside, escape); err != nil {
					t.Fatal(err)
				}
				path, err := sandboxes.OwnershipRecordPath(root, sandboxID)
				if err != nil {
					t.Fatal(err)
				}
				data := []byte(`{"version":1,"sandbox_id":"` + sandboxID + `","driver":"docker","runtime_id":"runtime","sandbox_path":"` + filepath.Join(escape, sandboxID) + `","lifecycle_state":"active"}`)
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "outside sandbox root",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			sandbox := removalTestSandbox(t, removalTestSandboxSpec{
				Root:    root,
				ID:      "sandbox-invalid",
				Status:  domain.VMStatusStopped,
				Updated: time.Now(),
			})
			tt.record(t, root, sandbox.Summary.ID)
			store := &removalTestStore{sandboxes: map[string]*domain.Sandbox{sandbox.Summary.ID: sandbox}}
			runtime := &removalTestRuntime{}
			coordinator := &sandboxes.RemovalCoordinator{SandboxRoot: root, Store: store, Runtime: runtime}

			_, err := coordinator.Remove(context.Background(), sandbox.Summary.ID, true)
			if !errors.Is(err, sandboxes.ErrOwnershipUnknown) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Remove error = %v", err)
			}
			if runtime.removeCalls != 0 || store.removeCalls != 0 {
				t.Fatalf("unsafe removal calls runtime/store = %d/%d", runtime.removeCalls, store.removeCalls)
			}
			if _, err := os.Stat(filepath.Dir(sandbox.Summary.WorkspacePath)); err != nil {
				t.Fatalf("sandbox data changed: %v", err)
			}
		})
	}
}

func TestRemovalCoordinatorPruneSeparatesRecordsAndResidues(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	owned := removalTestSandbox(t, removalTestSandboxSpec{
		Root:    root,
		ID:      "sandbox-owned",
		Status:  domain.VMStatusStopped,
		Updated: now.Add(-48 * time.Hour),
	})
	foreign := removalTestSandbox(t, removalTestSandboxSpec{
		Root:    root,
		ID:      "sandbox-foreign",
		Status:  domain.VMStatusStopped,
		Updated: now.Add(-48 * time.Hour),
	})
	store := &removalTestStore{sandboxes: map[string]*domain.Sandbox{owned.Summary.ID: owned, foreign.Summary.ID: foreign}}
	residues := &removalTestResidues{items: []sandboxes.RuntimeResidue{
		{Driver: "docker", RuntimeID: "known-runtime", SandboxID: owned.Summary.ID, UpdatedAt: now.Add(-48 * time.Hour), OwnershipValid: true, Removable: true},
		{Driver: "docker", RuntimeID: "orphan-runtime", SandboxID: "sandbox-orphan", UpdatedAt: now.Add(-48 * time.Hour), OwnershipValid: true, Removable: true},
		{Driver: "docker", RuntimeID: "unsafe-runtime", SandboxID: "", UpdatedAt: now.Add(-48 * time.Hour), OwnershipValid: false},
		{Driver: "docker", RuntimeID: "second-orphan-runtime", SandboxID: "sandbox-second-orphan", UpdatedAt: now.Add(-48 * time.Hour), OwnershipValid: true, Removable: true},
	}}
	coordinator := &sandboxes.RemovalCoordinator{
		SandboxRoot: root, Store: store, Runtime: &removalTestRuntime{}, Residues: residues, Now: func() time.Time { return now },
		Targets: removalTestTargets{targets: map[string]sandboxes.SandboxOwnershipTarget{
			owned.Summary.ID: {ProjectID: "project-a", AgentName: "worker"}, foreign.Summary.ID: {ProjectID: "project-b", AgentName: "worker"},
		}},
	}
	dryRun, err := coordinator.Prune(context.Background(), sandboxes.PruneRequest{ProjectID: "project-a", Driver: "docker", OlderThan: 24 * time.Hour})
	if err != nil || !dryRun.DryRun || len(dryRun.Matched) != 1 || residues.listCalls != 0 {
		t.Fatalf("record-only prune = %#v err=%v residue calls=%d", dryRun, err, residues.listCalls)
	}
	result, err := coordinator.Prune(context.Background(), sandboxes.PruneRequest{ProjectID: "project-a", Driver: "docker", OlderThan: 24 * time.Hour, IncludeOrphans: true, Force: true})
	if err != nil || result.DryRun || len(result.Matched) != 4 || len(result.Removed) != 3 || len(result.Skipped) != 1 {
		t.Fatalf("forced prune = %#v err=%v", result, err)
	}
	if residues.removeCalls != 2 || result.Removed[1] != "sandbox-orphan" || result.Removed[2] != "sandbox-second-orphan" {
		t.Fatalf("residue removal calls/result = %d/%#v", residues.removeCalls, result.Removed)
	}
	if len(residues.removed) != 2 || residues.removed[0].RuntimeID != "orphan-runtime" || residues.removed[1].RuntimeID != "second-orphan-runtime" {
		t.Fatalf("removed residues = %#v", residues.removed)
	}
}

// TestPrunableStatusAcceptsTheRecoverableIntermediateState pins the rule the CLI
// and the coordinator share. DELETING is prunable because Remove resumes the
// deletion from its journal and rolls back to a real lifecycle state when a
// runtime step fails, so a prune attempt can no longer strand a record there.
// RUNNING is not: prune must never stop a live runtime behind the operator's
// back.
func TestPrunableStatusAcceptsTheRecoverableIntermediateState(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{status: domain.VMStatusStopped, want: true},
		{status: domain.VMStatusFailed, want: true},
		{status: domain.VMStatusDeleting, want: true},
		{status: domain.VMStatusRunning, want: false},
		{status: domain.VMStatusPending, want: false},
		{status: "", want: false},
	}
	for _, tt := range tests {
		if got := sandboxes.PrunableStatus(tt.status); got != tt.want {
			t.Fatalf("PrunableStatus(%q) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

// TestRemovalCoordinatorPruneClearsAStuckDeletingRecord covers the record a crash
// between the deletion intent and the runtime call leaves in DELETING - the
// state issue #772 reports as having no product exit. Prune refused the status,
// so the record stayed DELETING forever. It may now target it, and a failed
// attempt must leave a real lifecycle state on both the record and its journal,
// so the record becomes something the operator can act on again.
func TestRemovalCoordinatorPruneClearsAStuckDeletingRecord(t *testing.T) {
	root := t.TempDir()
	stuck := removalTestSandbox(t, removalTestSandboxSpec{
		Root:    root,
		ID:      "sandbox-stuck-deleting",
		Status:  domain.VMStatusDeleting,
		Updated: time.Now().Add(-time.Hour),
	})
	record, err := sandboxes.ReadOwnershipRecord(root, stuck.Summary.ID)
	if err != nil {
		t.Fatalf("ReadOwnershipRecord: %v", err)
	}
	// The intent journal, as a crash mid-delete leaves it.
	record.LifecycleState = "deleting"
	if err := sandboxes.WriteOwnershipRecord(root, record); err != nil {
		t.Fatalf("WriteOwnershipRecord: %v", err)
	}
	store := &removalTestStore{sandboxes: map[string]*domain.Sandbox{stuck.Summary.ID: stuck}}
	runtime := &removalTestRuntime{removeErrors: []error{errors.New("injected remove failure")}}
	coordinator := &sandboxes.RemovalCoordinator{SandboxRoot: root, Store: store, Runtime: runtime}

	result, err := coordinator.Prune(context.Background(), sandboxes.PruneRequest{Statuses: []string{domain.VMStatusDeleting}, Force: true})
	if err != nil {
		t.Fatalf("Prune returned error: %v", err)
	}
	if len(result.Matched) != 1 || len(result.Skipped) != 1 || len(result.Removed) != 0 {
		t.Fatalf("prune result = %#v, want the stuck record matched and reported as skipped", result)
	}
	if reasons := strings.Join(result.Skipped[0].BlockedReasons, " "); !strings.Contains(reasons, "remove sandbox runtime") {
		t.Fatalf("skipped reasons = %#v, want the runtime removal failure reported to the operator", result.Skipped[0].BlockedReasons)
	}
	if status := store.sandboxes[stuck.Summary.ID].Summary.VMStatus; status == domain.VMStatusDeleting {
		t.Fatalf("sandbox status = %q, want a real lifecycle state after the failed deletion", status)
	}
	rolled, err := sandboxes.ReadOwnershipRecord(root, stuck.Summary.ID)
	if err != nil {
		t.Fatalf("ReadOwnershipRecord after prune: %v", err)
	}
	if rolled.LifecycleState == "deleting" {
		t.Fatalf("journal lifecycle = %q, want a state the recovery scan no longer retries", rolled.LifecycleState)
	}

	// The rolled-back record is an ordinary stopped record now, so the retry that
	// used to be impossible succeeds once the runtime is reachable again.
	runtime.removeErrors = nil
	result, err = coordinator.Prune(context.Background(), sandboxes.PruneRequest{Statuses: []string{domain.VMStatusStopped}, Force: true})
	if err != nil || len(result.Removed) != 1 {
		t.Fatalf("second prune result = %#v err = %v, want the record removed", result, err)
	}
	if _, err := sandboxes.ReadOwnershipRecord(root, stuck.Summary.ID); !os.IsNotExist(err) {
		t.Fatalf("ownership record remains after prune: %v", err)
	}
}

func TestRemovalCoordinatorSerializesConcurrentResume(t *testing.T) {
	root := t.TempDir()
	config := &appconfig.Config{DataRoot: root, SandboxRoot: filepath.Join(root, "sandboxes"), RuntimeDriver: "docker", DefaultImage: "guest:latest"}
	store, err := sandboxstore.NewWithConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := store.CreateSandboxWithOptions(context.Background(), "race", "", "docker", "guest:latest", "", "test", nil, nil, nil, sandboxstore.CreateSandboxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sandbox.Summary.VMStatus = domain.VMStatusStopped
	if err := store.UpdateSandbox(context.Background(), sandbox); err != nil {
		t.Fatal(err)
	}
	locks := sandboxes.NewLifecycleLocks()
	runtime := newBlockingLifecycleRuntime()
	lifecycle := sandboxes.Lifecycle{Config: config, Store: store, WorkspaceEnsurer: noOpWorkspaceEnsurer{}, Driver: runtime, Locks: locks}
	coordinator := &sandboxes.RemovalCoordinator{SandboxRoot: config.SandboxRoot, Store: store, Runtime: runtime, Locks: locks}

	resumeDone := make(chan error, 1)
	go func() {
		_, resumeErr := lifecycle.ResumeLoaded(context.Background(), sandbox, nil)
		resumeDone <- resumeErr
	}()
	<-runtime.startEntered
	removeDone := make(chan error, 1)
	go func() {
		_, removeErr := coordinator.Remove(context.Background(), sandbox.Summary.ID, true)
		removeDone <- removeErr
	}()
	select {
	case <-runtime.removeEntered:
		t.Fatal("remove reached the runtime while resume held the lifecycle lock")
	case <-time.After(50 * time.Millisecond):
	}
	close(runtime.continueStart)
	if err := <-resumeDone; err != nil {
		t.Fatalf("ResumeLoaded: %v", err)
	}
	if err := <-removeDone; err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if runtime.stopCalls != 1 || runtime.removeCalls != 1 {
		t.Fatalf("runtime stop/remove calls = %d/%d", runtime.stopCalls, runtime.removeCalls)
	}
	if _, err := store.GetSandbox(context.Background(), sandbox.Summary.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sandbox remains after serialized remove: %v", err)
	}
}

type removalTestSandboxSpec struct {
	Root    string
	ID      string
	Status  string
	Updated time.Time
}

func removalTestSandbox(t *testing.T, spec removalTestSandboxSpec) *domain.Sandbox {
	t.Helper()
	dir := filepath.Join(spec.Root, spec.ID)
	if err := os.MkdirAll(filepath.Join(dir, "workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: spec.ID, Driver: "docker", VMStatus: spec.Status, RuntimeRef: "runtime-" + spec.ID, WorkspacePath: filepath.Join(dir, "workspace"), CreatedAt: spec.Updated, UpdatedAt: spec.Updated}}
	if err := sandboxes.WriteOwnershipRecord(spec.Root, sandboxes.OwnershipRecord{Version: sandboxes.OwnershipRecordVersion, SandboxID: spec.ID, Driver: "docker", RuntimeID: sandbox.Summary.RuntimeRef, SandboxPath: dir, LifecycleState: "active"}); err != nil {
		t.Fatal(err)
	}
	return sandbox
}

type removalTestStore struct {
	sandboxes   map[string]*domain.Sandbox
	removeCalls int
}

func (s *removalTestStore) GetSandbox(_ context.Context, id string) (*domain.Sandbox, error) {
	item, ok := s.sandboxes[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	return item, nil
}

func (s *removalTestStore) ListSandboxes(context.Context, domain.SandboxListOptions) (domain.SandboxListResult, error) {
	result := domain.SandboxListResult{}
	for _, item := range s.sandboxes {
		result.Sandboxes = append(result.Sandboxes, item)
	}
	return result, nil
}

func (s *removalTestStore) UpdateSandbox(_ context.Context, sandbox *domain.Sandbox) error {
	s.sandboxes[sandbox.Summary.ID] = sandbox
	return nil
}

func (s *removalTestStore) RemoveSandbox(_ context.Context, id string) error {
	s.removeCalls++
	item, ok := s.sandboxes[id]
	if !ok {
		return os.ErrNotExist
	}
	delete(s.sandboxes, id)
	return os.RemoveAll(filepath.Dir(item.Summary.WorkspacePath))
}

type removalTestRuntime struct {
	stopCalls    int
	removeCalls  int
	stopErrors   []error
	removeErrors []error
}

type noOpWorkspaceEnsurer struct{}

func (noOpWorkspaceEnsurer) Ensure(context.Context, *domain.Sandbox) error { return nil }

type blockingLifecycleRuntime struct {
	startEntered  chan struct{}
	continueStart chan struct{}
	removeEntered chan struct{}
	stopCalls     int
	removeCalls   int
}

func newBlockingLifecycleRuntime() *blockingLifecycleRuntime {
	return &blockingLifecycleRuntime{startEntered: make(chan struct{}), continueStart: make(chan struct{}), removeEntered: make(chan struct{}, 1)}
}

func (r *blockingLifecycleRuntime) StartSandboxVM(context.Context, *domain.Sandbox) error {
	close(r.startEntered)
	<-r.continueStart
	return nil
}

func (r *blockingLifecycleRuntime) StopSandboxVM(context.Context, *domain.Sandbox) error {
	r.stopCalls++
	return nil
}

func (r *blockingLifecycleRuntime) RemoveSandboxVM(context.Context, *domain.Sandbox) error {
	r.removeCalls++
	r.removeEntered <- struct{}{}
	return nil
}

func (r *removalTestRuntime) StopSandboxVM(context.Context, *domain.Sandbox) error {
	r.stopCalls++
	if len(r.stopErrors) == 0 {
		return nil
	}
	err := r.stopErrors[0]
	r.stopErrors = r.stopErrors[1:]
	return err
}

func (r *removalTestRuntime) RemoveSandboxVM(context.Context, *domain.Sandbox) error {
	r.removeCalls++
	if len(r.removeErrors) == 0 {
		return nil
	}
	err := r.removeErrors[0]
	r.removeErrors = r.removeErrors[1:]
	return err
}

type removalTestTargets struct {
	targets map[string]sandboxes.SandboxOwnershipTarget
}

func (r removalTestTargets) ResolveSandboxTargets(context.Context, []*domain.Sandbox) (map[string]sandboxes.SandboxOwnershipTarget, error) {
	return r.targets, nil
}

type removalTestResidues struct {
	items       []sandboxes.RuntimeResidue
	listCalls   int
	removeCalls int
	removed     []sandboxes.RuntimeResidue
}

func (r *removalTestResidues) ListRuntimeResidues(context.Context) ([]sandboxes.RuntimeResidue, []string, error) {
	r.listCalls++
	return append([]sandboxes.RuntimeResidue(nil), r.items...), nil, nil
}

func (r *removalTestResidues) RemoveRuntimeResidue(_ context.Context, residue sandboxes.RuntimeResidue) error {
	r.removeCalls++
	r.removed = append(r.removed, residue)
	return nil
}
