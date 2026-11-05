package sandboxstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
)

func newEgressPolicyRevisionSandbox(t *testing.T, store *Store) string {
	t.Helper()
	sandbox, err := store.CreateSandbox(context.Background(), "Policy revision", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}
	return sandbox.Summary.ID
}

func TestEgressPolicyRevisionMonotonicAndAcknowledged(t *testing.T) {
	store := newCoverageStore(t)
	sandboxID := newEgressPolicyRevisionSandbox(t, store)
	ctx := context.Background()

	missing, err := store.GetEgressPolicyRevision(ctx, sandboxID)
	if err != nil {
		t.Fatalf("GetEgressPolicyRevision() before any observe error = %v", err)
	}
	if missing.Recorded() || missing.Acknowledged() {
		t.Fatalf("revision before any observe = %+v, want an absent record", missing)
	}

	firstAt := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return firstAt }
	first, err := store.ObserveEgressPolicyRevision(ctx, sandboxID, "sha256:aaa", false)
	if err != nil {
		t.Fatalf("ObserveEgressPolicyRevision() error = %v", err)
	}
	if first.Revision != 1 || first.Hash != "sha256:aaa" || !first.AppliedAt.Equal(firstAt) {
		t.Fatalf("first revision = %+v, want revision 1 applied at %v", first, firstAt)
	}
	if first.Acknowledged() {
		t.Fatal("an unacknowledged observation recorded an acknowledgement")
	}

	// Replaying the same policy keeps the revision and its applied time.
	secondAt := firstAt.Add(time.Minute)
	store.now = func() time.Time { return secondAt }
	replayed, err := store.ObserveEgressPolicyRevision(ctx, sandboxID, "sha256:aaa", true)
	if err != nil {
		t.Fatalf("replayed ObserveEgressPolicyRevision() error = %v", err)
	}
	if replayed.Revision != 1 || !replayed.AppliedAt.Equal(firstAt) {
		t.Fatalf("replayed revision = %+v, want revision 1 still applied at %v", replayed, firstAt)
	}
	if !replayed.AckAt.Equal(secondAt) {
		t.Fatalf("replayed acknowledgement = %v, want %v", replayed.AckAt, secondAt)
	}

	// A changed policy advances the revision and starts unacknowledged again.
	thirdAt := secondAt.Add(time.Minute)
	store.now = func() time.Time { return thirdAt }
	changed, err := store.ObserveEgressPolicyRevision(ctx, sandboxID, "sha256:bbb", false)
	if err != nil {
		t.Fatalf("changed ObserveEgressPolicyRevision() error = %v", err)
	}
	if changed.Revision != 2 || changed.Hash != "sha256:bbb" || !changed.AppliedAt.Equal(thirdAt) || changed.Acknowledged() {
		t.Fatalf("changed revision = %+v, want revision 2 applied at %v without an acknowledgement", changed, thirdAt)
	}

	persisted, err := store.GetEgressPolicyRevision(ctx, sandboxID)
	if err != nil {
		t.Fatalf("GetEgressPolicyRevision() error = %v", err)
	}
	if persisted != changed {
		t.Fatalf("persisted revision = %+v, want %+v", persisted, changed)
	}
}

func TestEgressPolicyRevisionPersistsAcrossStoreInstances(t *testing.T) {
	root := t.TempDir()
	config := &appconfig.Config{SandboxRoot: root}
	store, err := NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sandboxID := newEgressPolicyRevisionSandbox(t, store)

	appliedAt := time.Date(2026, 11, 3, 12, 30, 0, 0, time.UTC)
	store.now = func() time.Time { return appliedAt }
	observed, err := store.ObserveEgressPolicyRevision(context.Background(), sandboxID, "sha256:persisted", true)
	if err != nil {
		t.Fatalf("ObserveEgressPolicyRevision() error = %v", err)
	}

	reopened, err := NewWithConfig(config)
	if err != nil {
		t.Fatalf("reopen NewWithConfig() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.GetEgressPolicyRevision(context.Background(), sandboxID)
	if err != nil {
		t.Fatalf("reopened GetEgressPolicyRevision() error = %v", err)
	}
	if got != observed {
		t.Fatalf("reopened revision = %+v, want %+v", got, observed)
	}
}

func TestObserveEgressPolicyRevisionSerializesConcurrentReaders(t *testing.T) {
	store := newCoverageStore(t)
	sandboxID := newEgressPolicyRevisionSandbox(t, store)

	var wg sync.WaitGroup
	for index := 0; index < 16; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.ObserveEgressPolicyRevision(context.Background(), sandboxID, "sha256:same", true); err != nil {
				t.Errorf("ObserveEgressPolicyRevision() error = %v", err)
			}
		}()
	}
	wg.Wait()

	revision, err := store.GetEgressPolicyRevision(context.Background(), sandboxID)
	if err != nil {
		t.Fatalf("GetEgressPolicyRevision() error = %v", err)
	}
	if revision.Revision != 1 || !revision.Acknowledged() {
		t.Fatalf("revision = %+v, want one acknowledged revision for one unchanged policy", revision)
	}
	assertNoEgressPolicyRevisionTemps(t, store, sandboxID)
}

func TestObserveEgressPolicyRevisionValidatesInput(t *testing.T) {
	store := newCoverageStore(t)
	sandboxID := newEgressPolicyRevisionSandbox(t, store)
	if _, err := store.ObserveEgressPolicyRevision(context.Background(), sandboxID, "  ", true); err == nil {
		t.Fatal("ObserveEgressPolicyRevision() accepted an empty hash")
	}
	if _, err := store.ObserveEgressPolicyRevision(context.Background(), "../escape", "sha256:aaa", true); err == nil {
		t.Fatal("ObserveEgressPolicyRevision() accepted an invalid sandbox id")
	}
	if _, err := store.GetEgressPolicyRevision(context.Background(), "../escape"); err == nil {
		t.Fatal("GetEgressPolicyRevision() accepted an invalid sandbox id")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ObserveEgressPolicyRevision(cancelled, sandboxID, "sha256:aaa", true); !errors.Is(err, context.Canceled) {
		t.Fatalf("ObserveEgressPolicyRevision() error = %v, want context.Canceled", err)
	}
}

func TestEgressPolicyRevisionRejectsCorruptState(t *testing.T) {
	store := newCoverageStore(t)
	sandboxID := newEgressPolicyRevisionSandbox(t, store)
	path := store.egressPolicyRevisionPath(sandboxID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create revision directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt revision: %v", err)
	}
	if _, err := store.GetEgressPolicyRevision(context.Background(), sandboxID); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("GetEgressPolicyRevision() error = %v, want a decode error", err)
	}
	if _, err := store.ObserveEgressPolicyRevision(context.Background(), sandboxID, "sha256:aaa", true); err == nil {
		t.Fatal("ObserveEgressPolicyRevision() overwrote corrupt state")
	}
}

func assertNoEgressPolicyRevisionTemps(t *testing.T, store *Store, sandboxID string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(store.egressPolicyRevisionPath(sandboxID)), ".policy-revision.json.tmp-*"))
	if err != nil {
		t.Fatalf("glob revision temps: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("revision temporary files remain: %v", matches)
	}
	if revision, err := store.GetEgressPolicyRevision(context.Background(), sandboxID); err != nil || !revision.Recorded() {
		t.Fatalf("revision after concurrent observe = %+v, %v", revision, err)
	}
}
