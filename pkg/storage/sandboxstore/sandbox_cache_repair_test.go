package sandboxstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

type repairProjectResolver func(context.Context, []*domain.Sandbox) (map[string]string, error)

func (f repairProjectResolver) ResolveSandboxProjectIDs(ctx context.Context, sandboxes []*domain.Sandbox) (map[string]string, error) {
	return f(ctx, sandboxes)
}

// failThenBlockIndexRepair fails the write-through lookup, then holds its
// background retry until released. Unrelated sandbox lookups remain available.
func failThenBlockIndexRepair(t *testing.T, store *Store, id string) (<-chan struct{}, func()) {
	t.Helper()
	started, release := make(chan struct{}), make(chan struct{})
	var attempts atomic.Int32
	store.projectResolver = repairProjectResolver(func(ctx context.Context, sandboxes []*domain.Sandbox) (map[string]string, error) {
		if len(sandboxes) != 1 || sandboxes[0].Summary.ID != id {
			return map[string]string{}, nil
		}
		switch attempts.Add(1) {
		case 1:
			return nil, context.DeadlineExceeded
		case 2:
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return map[string]string{id: "repaired-project"}, nil
	})
	return started, func() { close(release) }
}

func awaitIndexCondition(t *testing.T, condition func() bool) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-timer.C:
			t.Fatal("sandbox index did not reach the expected state")
		case <-ticker.C:
		}
	}
}

func awaitIndexSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(10 * time.Second):
		t.Fatal("sandbox index operation did not complete")
	}
}

func TestSandboxIndexRepairDoesNotBlockReadsOrOtherSandboxes(t *testing.T) {
	store := newTestStore(t)
	broken := seedSandboxDir(t, store, "needs-repair", time.Unix(100, 0))
	unrelated := seedSandboxDir(t, store, "unrelated", time.Unix(101, 0))
	store.recordIndex(broken)
	store.recordIndex(unrelated)
	// A global rebuild would accidentally index this unrelated directory.
	seedSandboxDir(t, store, "not-indexed", time.Unix(102, 0))
	started, release := failThenBlockIndexRepair(t, store, broken.Summary.ID)
	broken.Summary.Title = "authoritative title"
	if err := store.SaveSandbox(broken); err != nil {
		t.Fatal(err)
	}
	awaitIndexSignal(t, started)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	summaries, err := store.ListSandboxSummaries(ctx, []string{broken.Summary.ID, unrelated.Summary.ID})
	if err != nil || len(summaries) != 2 || summaries[broken.Summary.ID].Title != broken.Summary.Title {
		t.Fatalf("summaries = %#v, error = %v", summaries, err)
	}
	result, err := store.ListSandboxes(ctx, SandboxListOptions{})
	if err != nil || result.TotalCount != 2 {
		t.Fatalf("list during repair = %#v, error = %v", result, err)
	}
	unrelated.Summary.Title = "independent update"
	if err := store.SaveSandbox(unrelated); err != nil {
		t.Fatal(err)
	}
	var title string
	if err := store.index.db.QueryRow(`SELECT title FROM sandboxes WHERE id = ?`, unrelated.Summary.ID).Scan(&title); err != nil || title != unrelated.Summary.Title {
		t.Fatalf("unrelated title = %q, error = %v", title, err)
	}
	release()
	awaitIndexCondition(t, func() bool { return store.indexRepairs.revision(broken.Summary.ID) == 0 })
	var project string
	if err := store.index.db.QueryRow(`SELECT title, project_id FROM sandboxes WHERE id = ?`, broken.Summary.ID).Scan(&title, &project); err != nil || title != broken.Summary.Title || project != "repaired-project" {
		t.Fatalf("repaired title/project = %q/%q, error = %v", title, project, err)
	}
	var count int
	if err := store.index.db.QueryRow(`SELECT COUNT(*) FROM sandboxes WHERE id = 'not-indexed'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unrelated directory indexed: count = %d, error = %v", count, err)
	}
}

func TestSandboxIndexRepairKeepsNewerFailureQueued(t *testing.T) {
	store := newTestStore(t)
	sandbox := seedSandboxDir(t, store, "newer-write", time.Unix(100, 0))
	started, release := failThenBlockIndexRepair(t, store, sandbox.Summary.ID)
	store.recordIndex(sandbox)
	awaitIndexSignal(t, started)
	// The in-flight repair already read the old metadata. A newer committed
	// write that also fails must not be cleared by that repair's success.
	sandbox.Summary.Title = "newest metadata"
	if err := store.saveSandbox(sandbox); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.syncSandboxIndex(ctx, sandbox.Summary.ID, sandboxIndexRefresh); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write = %v", err)
	}
	store.markIndexRepair(sandbox.Summary.ID)
	release()
	awaitIndexCondition(t, func() bool { return store.indexRepairs.revision(sandbox.Summary.ID) == 0 })
	var title string
	if err := store.index.db.QueryRow(`SELECT title FROM sandboxes WHERE id = ?`, sandbox.Summary.ID).Scan(&title); err != nil || title != sandbox.Summary.Title {
		t.Fatalf("repaired title = %q, error = %v", title, err)
	}
}

func TestSandboxIndexRepairDoesNotResurrectDeletedSandbox(t *testing.T) {
	store := newTestStore(t)
	sandbox := seedSandboxDir(t, store, "deleted-write", time.Unix(100, 0))
	started, release := failThenBlockIndexRepair(t, store, sandbox.Summary.ID)
	store.recordIndex(sandbox)
	awaitIndexSignal(t, started)
	if err := os.RemoveAll(store.sandboxDir(sandbox.Summary.ID)); err != nil {
		t.Fatal(err)
	}
	store.markIndexRepair(sandbox.Summary.ID)
	release()
	awaitIndexCondition(t, func() bool { return store.indexRepairs.revision(sandbox.Summary.ID) == 0 })
	var count int
	if err := store.index.db.QueryRow(`SELECT COUNT(*) FROM sandboxes WHERE id = ?`, sandbox.Summary.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted sandbox row count = %d, error = %v", count, err)
	}
}

func TestSandboxIndexRepairStopsBeforeDatabaseClose(t *testing.T) {
	store := newTestStore(t)
	sandbox := seedSandboxDir(t, store, "shutdown", time.Unix(100, 0))
	started, _ := failThenBlockIndexRepair(t, store, sandbox.Summary.ID)
	store.recordIndex(sandbox)
	awaitIndexSignal(t, started)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-store.indexRepairs.done:
	default:
		t.Fatal("Close returned with the repair worker still running")
	}
	if err := store.index.db.Ping(); err == nil {
		t.Fatal("standalone database remained open")
	}
}

func TestSandboxIndexRepairPrunesInvalidMetadata(t *testing.T) {
	cases := []struct{ name, metadata string }{
		{"malformed JSON", "{"},
		{"wrong JSON type", `{"summary":{"id":123}}`},
		{"missing ID", `{"summary":{"driver":"docker"}}`},
		{"mismatched ID", `{"summary":{"id":"other","driver":"docker"}}`},
		{"invalid driver", `{"summary":{"id":"unreadable","driver":"invalid-driver"}}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			sandbox := seedSandboxDir(t, store, "unreadable", time.Unix(100, 0))
			store.recordIndex(sandbox)
			path := filepath.Join(store.sandboxDir(sandbox.Summary.ID), "metadata.json")
			if err := os.WriteFile(path, []byte(test.metadata), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := store.syncSandboxIndex(t.Context(), sandbox.Summary.ID, sandboxIndexRefresh); err != nil {
				t.Fatalf("invalid metadata did not converge by pruning its cached row: %v", err)
			}
			var count int
			if err := store.index.db.QueryRow(`SELECT COUNT(*) FROM sandboxes WHERE id = ?`, sandbox.Summary.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid metadata cached row count = %d, error = %v", count, err)
			}
			metadata, err := os.ReadFile(path)
			if err != nil || string(metadata) != test.metadata {
				t.Fatalf("authoritative metadata changed: %q, error = %v", metadata, err)
			}
			if err := store.saveSandbox(sandbox); err != nil {
				t.Fatal(err)
			}
			store.recordIndex(sandbox)
			summaries, err := store.ListSandboxSummaries(t.Context(), []string{sandbox.Summary.ID})
			if err != nil || summaries[sandbox.Summary.ID].Title != sandbox.Summary.Title {
				t.Fatalf("corrected metadata was not indexed: %#v, error = %v", summaries, err)
			}
		})
	}
}

func TestSandboxIndexRepairClearsPendingInvalidMetadata(t *testing.T) {
	store := newTestStore(t)
	sandbox := seedSandboxDir(t, store, "becomes-invalid", time.Unix(100, 0))
	started, release := failThenBlockIndexRepair(t, store, sandbox.Summary.ID)
	store.recordIndex(sandbox)
	awaitIndexSignal(t, started)
	if err := os.WriteFile(filepath.Join(store.sandboxDir(sandbox.Summary.ID), "metadata.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.markIndexRepair(sandbox.Summary.ID)
	release()
	awaitIndexCondition(t, func() bool { return store.indexRepairs.revision(sandbox.Summary.ID) == 0 })
	summaries, err := store.ListSandboxSummaries(t.Context(), []string{sandbox.Summary.ID})
	if err != nil || len(summaries) != 0 {
		t.Fatalf("invalid sandbox still cached or retrying: %#v, error = %v", summaries, err)
	}
}

func TestSandboxIndexRepairKeepsFilesystemReadErrorsRetryable(t *testing.T) {
	store := newTestStore(t)
	sandbox := seedSandboxDir(t, store, "unreadable-file", time.Unix(100, 0))
	store.recordIndex(sandbox)
	path := filepath.Join(store.sandboxDir(sandbox.Summary.ID), "metadata.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	// A filesystem read error must not be mistaken for invalid JSON and cause
	// the last known projection to be pruned as permanently unrepairable.
	err := store.syncSandboxIndex(t.Context(), sandbox.Summary.ID, sandboxIndexRefresh)
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("filesystem read error lost: %v", err)
	}
	var count int
	if err := store.index.db.QueryRow(`SELECT COUNT(*) FROM sandboxes WHERE id = ?`, sandbox.Summary.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("transient failure pruned cached row: count=%d, error=%v", count, err)
	}
}

func TestSandboxIndexRepairRetriesFailedInvalidMetadataPrune(t *testing.T) {
	store := newTestStore(t)
	sandbox := seedSandboxDir(t, store, "failed-prune", time.Unix(100, 0))
	store.recordIndex(sandbox)
	if err := os.WriteFile(filepath.Join(store.sandboxDir(sandbox.Summary.ID), "metadata.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := store.index.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'sandboxes'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	// The source is permanently invalid, but removing its projection can still
	// fail transiently. The repair must stay pending until deletion succeeds.
	if _, err := store.index.db.Exec(`DROP TABLE sandboxes`); err != nil {
		t.Fatal(err)
	}
	store.recordIndex(sandbox)
	if store.indexRepairs.revision(sandbox.Summary.ID) == 0 {
		t.Fatal("failed projection deletion was dropped from pending repairs")
	}
	if _, err := store.index.db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	awaitIndexCondition(t, func() bool { return store.indexRepairs.revision(sandbox.Summary.ID) == 0 })
	var count int
	if err := store.index.db.QueryRow(`SELECT COUNT(*) FROM sandboxes WHERE id = ?`, sandbox.Summary.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid sandbox projection survived retry: count=%d, error=%v", count, err)
	}
}
