package sandboxes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestDeletionRecoveryRunsPendingDeletionsConcurrentlyAndStops(t *testing.T) {
	root := t.TempDir()
	writeDeletingRecoveryRecords(t, root, "a", "b")
	runtime := &recoveryRuntime{
		blockedID: "a",
		started:   make(chan string, 2),
		completed: make(chan string, 2),
	}
	recovery := newTestDeletionRecovery(root, runtime)
	if err := recovery.Start(context.Background()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	wantStarted := map[string]bool{"a": false, "b": false}
	for range wantStarted {
		select {
		case id := <-runtime.started:
			wantStarted[id] = true
		case <-time.After(10 * time.Second):
			t.Fatalf("recovery workers did not start both deletions: %v", wantStarted)
		}
	}
	select {
	case id := <-runtime.completed:
		if id != "b" {
			t.Fatalf("completed deletion = %q, want unblocked sandbox b", id)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("unblocked deletion waited behind blocked deletion")
	}

	waitForRecoveryJournalRemoval(t, root, "b")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := recovery.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	if _, err := ReadOwnershipRecord(root, "a"); err != nil {
		t.Fatalf("canceled deletion did not retain its journal: %v", err)
	}
}

func TestDeletionRecoveryReportsFailureAndRollsBackJournal(t *testing.T) {
	root := t.TempDir()
	writeDeletingRecoveryRecords(t, root, "retry")
	failedRuntime := &recoveryRuntime{failureID: "retry", started: make(chan string, 1), completed: make(chan string, 1)}
	first := newTestDeletionRecovery(root, failedRuntime)
	if err := first.Start(context.Background()); err != nil {
		t.Fatalf("first Start returned error: %v", err)
	}
	waitForDeletionRecoveryDone(t, first)

	// A failed deletion rolls its journal back instead of leaving it in
	// deleting, so later daemon starts neither resume the same failing deletion
	// nor repeat the same warning forever.
	record, err := ReadOwnershipRecord(root, "retry")
	if err != nil {
		t.Fatalf("failed deletion did not retain its journal: %v", err)
	}
	if record.LifecycleState == "deleting" {
		t.Fatalf("failed deletion journal = %#v, want a rolled-back state", record)
	}

	// The rolled-back sandbox is no longer pending, so recovery leaves it alone:
	// deleting it again stays an explicit request rather than background work.
	untouched := &recoveryRuntime{started: make(chan string, 1), completed: make(chan string, 1)}
	second := newTestDeletionRecovery(root, untouched)
	if err := second.Start(context.Background()); err != nil {
		t.Fatalf("second Start returned error: %v", err)
	}
	waitForDeletionRecoveryDone(t, second)
	select {
	case id := <-untouched.started:
		t.Fatalf("recovery resumed the rolled-back sandbox %q", id)
	default:
	}

	// The failure keeps a product path: an explicit removal with a working
	// runtime deletes the sandbox and its journal.
	successRuntime := &recoveryRuntime{started: make(chan string, 1), completed: make(chan string, 1)}
	coordinator := &RemovalCoordinator{SandboxRoot: root, Store: recoveryStore{}, Runtime: successRuntime}
	if _, err := coordinator.Remove(context.Background(), "retry", true); err != nil {
		t.Fatalf("explicit Remove after rollback: %v", err)
	}
	if _, err := ReadOwnershipRecord(root, "retry"); !os.IsNotExist(err) {
		t.Fatalf("journal after explicit Remove = %v, want not exist", err)
	}
}

func TestDeletionRecoveryRemovesArchivedSandboxWithoutCleanupPolicy(t *testing.T) {
	root := t.TempDir()
	archiveRoot := filepath.Join(t.TempDir(), "archives")
	sandboxID := "archived-before-removal"
	sandboxDir := filepath.Join(root, sandboxID)
	if err := os.MkdirAll(filepath.Join(sandboxDir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	store := &archivedRecoveryStore{sandbox: &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID: sandboxID, VMStatus: domain.VMStatusStopped,
			WorkspacePath: filepath.Join(sandboxDir, "workspace"),
		},
		Archive: &domain.SandboxArchive{State: domain.SandboxArchiveStateArchived, ID: "archive"},
	}}
	writeRecoveryArchive(t, archiveRoot, sandboxID, "archive")
	recovery := NewDeletionRecoveryWithArchiveRoot(&RemovalCoordinator{
		SandboxRoot: root, Store: store, Runtime: &recoveryRuntime{},
	}, archiveRoot, discardRecoveryLogger())
	if err := recovery.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForDeletionRecoveryDone(t, recovery)
	if !store.removed {
		t.Fatal("archived sandbox was not removed by startup recovery")
	}
	if _, err := ReadOwnershipRecord(root, sandboxID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ownership record remains after archived recovery: %v", err)
	}
}

func TestDeletionRecoveryDoesNotTrustUnverifiedArchivedMetadata(t *testing.T) {
	root := t.TempDir()
	archiveRoot := filepath.Join(t.TempDir(), "archives")
	if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	sandboxID := "forged-archive-state"
	sandboxDir := filepath.Join(root, sandboxID)
	if err := os.MkdirAll(filepath.Join(sandboxDir, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	store := &archivedRecoveryStore{sandbox: &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID: sandboxID, VMStatus: domain.VMStatusStopped,
			WorkspacePath: filepath.Join(sandboxDir, "workspace"),
		},
		Archive: &domain.SandboxArchive{State: domain.SandboxArchiveStateArchived, ID: "missing"},
	}}
	recovery := NewDeletionRecoveryWithArchiveRoot(&RemovalCoordinator{
		SandboxRoot: root, Store: store, Runtime: &recoveryRuntime{},
	}, archiveRoot, discardRecoveryLogger())
	if err := recovery.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForDeletionRecoveryDone(t, recovery)
	if store.removed {
		t.Fatal("unverified archived metadata triggered sandbox removal")
	}
	if _, err := os.Stat(sandboxDir); err != nil {
		t.Fatalf("unverified archived metadata removed sandbox data: %v", err)
	}
}

func TestDeletionRecoveryPagesSandboxMetadata(t *testing.T) {
	store := &pagedRecoveryStore{}
	recovery := NewDeletionRecovery(&RemovalCoordinator{
		SandboxRoot: t.TempDir(), Store: store, Runtime: &recoveryRuntime{},
	}, discardRecoveryLogger())
	if err := recovery.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForDeletionRecoveryDone(t, recovery)

	if len(store.options) != 2 {
		t.Fatalf("ListSandboxes calls = %d, want 2", len(store.options))
	}
	if first := store.options[0]; first.Offset != 0 || first.Limit != deletionRecoveryPageSize {
		t.Fatalf("first page options = %#v", first)
	}
	if second := store.options[1]; second.Offset != 7 || second.Limit != deletionRecoveryPageSize {
		t.Fatalf("second page options = %#v", second)
	}
}

func TestDeletionRecoveryStopsPagingWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &pagedRecoveryStore{afterList: cancel}
	recovery := &DeletionRecovery{coordinator: &RemovalCoordinator{Store: store}}

	warnings := recovery.appendArchivedSandboxes(ctx, make(map[string]struct{}))
	if len(warnings) != 0 {
		t.Fatalf("canceled recovery warnings = %v", warnings)
	}
	if len(store.options) != 1 {
		t.Fatalf("ListSandboxes calls after cancellation = %d, want 1", len(store.options))
	}
}

func TestDeletionRecoveryReportsUnreadableJournal(t *testing.T) {
	root := t.TempDir()
	lifecycleRoot := LifecycleRoot(root)
	if err := os.MkdirAll(lifecycleRoot, 0o700); err != nil {
		t.Fatalf("create lifecycle root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lifecycleRoot, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("write broken lifecycle journal: %v", err)
	}

	var logs bytes.Buffer
	recovery := NewDeletionRecovery(&RemovalCoordinator{
		SandboxRoot: root,
		Store:       recoveryStore{},
		Runtime:     &recoveryRuntime{},
	}, slog.New(slog.NewTextHandler(&logs, nil)))
	if err := recovery.Start(context.Background()); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	waitForDeletionRecoveryDone(t, recovery)
	if !strings.Contains(logs.String(), "broken.json") {
		t.Fatalf("recovery log = %q, want unreadable journal warning", logs.String())
	}
}

func TestDeletionRecoveryLifecycleEdges(t *testing.T) {
	t.Run("missing coordinator", func(t *testing.T) {
		recovery := NewDeletionRecovery(nil, discardRecoveryLogger())
		if err := recovery.Start(context.Background()); err == nil {
			t.Fatal("Start returned nil error without a coordinator")
		}
		if err := recovery.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown before Start returned error: %v", err)
		}
	})

	t.Run("start is idempotent", func(t *testing.T) {
		recovery := newTestDeletionRecovery(t.TempDir(), &recoveryRuntime{})
		if err := recovery.Start(context.Background()); err != nil {
			t.Fatalf("first Start returned error: %v", err)
		}
		if err := recovery.Start(context.Background()); err != nil {
			t.Fatalf("second Start returned error: %v", err)
		}
		waitForDeletionRecoveryDone(t, recovery)
	})

	t.Run("shutdown honors deadline", func(t *testing.T) {
		root := t.TempDir()
		writeDeletingRecoveryRecords(t, root, "slow")
		release := make(chan struct{})
		runtime := &recoveryRuntime{blockedID: "slow", ignoreCancellation: true, release: release, started: make(chan string, 1)}
		recovery := newTestDeletionRecovery(root, runtime)
		if err := recovery.Start(context.Background()); err != nil {
			t.Fatalf("Start returned error: %v", err)
		}
		select {
		case <-runtime.started:
		case <-time.After(10 * time.Second):
			t.Fatal("slow deletion did not start")
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if err := recovery.Shutdown(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Shutdown error = %v, want deadline exceeded", err)
		}
		close(release)
		if err := recovery.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown after release returned error: %v", err)
		}
	})
}

func newTestDeletionRecovery(root string, runtime RemovalRuntime) *DeletionRecovery {
	return NewDeletionRecovery(&RemovalCoordinator{
		SandboxRoot: root,
		Store:       recoveryStore{},
		Runtime:     runtime,
	}, discardRecoveryLogger())
}

func writeDeletingRecoveryRecords(t *testing.T, root string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := WriteOwnershipRecord(root, OwnershipRecord{
			SandboxID: id, SandboxPath: filepath.Join(root, id), LifecycleState: "deleting",
		}); err != nil {
			t.Fatalf("write ownership record %s: %v", id, err)
		}
	}
}

func waitForDeletionRecoveryDone(t *testing.T, recovery *DeletionRecovery) {
	t.Helper()
	recovery.mu.Lock()
	done := recovery.done
	recovery.mu.Unlock()
	if done == nil {
		t.Fatal("deletion recovery did not initialize its completion signal")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deletion recovery did not finish")
	}
}

func waitForRecoveryJournalRemoval(t *testing.T, root, sandboxID string) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := ReadOwnershipRecord(root, sandboxID)
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		if err != nil {
			t.Fatalf("read recovery journal %s: %v", sandboxID, err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("recovery journal %s was not removed", sandboxID)
		case <-ticker.C:
		}
	}
}

func discardRecoveryLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func writeRecoveryArchive(t *testing.T, archiveRoot, sandboxID, archiveID string) {
	t.Helper()
	directoryPath := filepath.Join(archiveRoot, sandboxID)
	if err := os.MkdirAll(directoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := []byte("committed archive")
	if err := os.WriteFile(filepath.Join(directoryPath, archiveID+".tar.zst"), archive, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive)
	directory, err := os.OpenRoot(directoryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	if err := writeArchiveManifest(directory, archiveID+".json", sandboxArchiveManifest{
		Version: sandboxArchiveManifestVersion, ArchiveID: archiveID, SandboxID: sandboxID,
		SizeBytes: int64(len(archive)), SHA256: hex.EncodeToString(digest[:]),
	}); err != nil {
		t.Fatal(err)
	}
}

type recoveryStore struct{}

func (recoveryStore) GetSandbox(_ context.Context, id string) (*domain.Sandbox, error) {
	return &domain.Sandbox{Summary: domain.SandboxSummary{ID: id}}, nil
}

func (recoveryStore) ListSandboxes(context.Context, domain.SandboxListOptions) (domain.SandboxListResult, error) {
	return domain.SandboxListResult{}, nil
}

func (recoveryStore) UpdateSandbox(context.Context, *domain.Sandbox) error { return nil }
func (recoveryStore) RemoveSandbox(context.Context, string) error          { return nil }

type pagedRecoveryStore struct {
	recoveryStore
	options   []domain.SandboxListOptions
	afterList func()
}

func (s *pagedRecoveryStore) ListSandboxes(_ context.Context, options domain.SandboxListOptions) (domain.SandboxListResult, error) {
	s.options = append(s.options, options)
	if s.afterList != nil {
		s.afterList()
	}
	if options.Offset == 0 {
		return domain.SandboxListResult{HasMore: true, NextOffset: 7}, nil
	}
	return domain.SandboxListResult{}, nil
}

type archivedRecoveryStore struct {
	sandbox *domain.Sandbox
	removed bool
}

func (s *archivedRecoveryStore) GetSandbox(_ context.Context, id string) (*domain.Sandbox, error) {
	if s.sandbox == nil || s.sandbox.Summary.ID != id || s.removed {
		return nil, os.ErrNotExist
	}
	return s.sandbox, nil
}

func (s *archivedRecoveryStore) ListSandboxes(context.Context, domain.SandboxListOptions) (domain.SandboxListResult, error) {
	if s.sandbox == nil || s.removed {
		return domain.SandboxListResult{}, nil
	}
	return domain.SandboxListResult{Sandboxes: []*domain.Sandbox{s.sandbox}}, nil
}

func (s *archivedRecoveryStore) UpdateSandbox(_ context.Context, sandbox *domain.Sandbox) error {
	s.sandbox = sandbox
	return nil
}

func (s *archivedRecoveryStore) RemoveSandbox(context.Context, string) error {
	s.removed = true
	return nil
}

type recoveryRuntime struct {
	blockedID          string
	failureID          string
	ignoreCancellation bool
	release            <-chan struct{}
	started            chan string
	completed          chan string
}

func (r *recoveryRuntime) StopSandboxVM(context.Context, *domain.Sandbox) error { return nil }

func (r *recoveryRuntime) RemoveSandboxVM(ctx context.Context, sandbox *domain.Sandbox) error {
	if sandbox == nil {
		return errors.New("sandbox is nil")
	}
	if r.started != nil {
		r.started <- sandbox.Summary.ID
	}
	if sandbox.Summary.ID == r.failureID {
		return errors.New("runtime removal failed")
	}
	if sandbox.Summary.ID == r.blockedID {
		if r.ignoreCancellation {
			<-r.release
		} else {
			<-ctx.Done()
			return ctx.Err()
		}
	}
	if r.completed != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r.completed <- sandbox.Summary.ID:
		}
	}
	return nil
}
