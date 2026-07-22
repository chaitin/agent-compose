package sessionstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
)

func cleanupSandboxStore(t *testing.T, store *Store) {
	t.Helper()
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close sandbox store: %v", err)
		}
	})
}

func TestNewWithConfigCompletesInitialIndexRebuild(t *testing.T) {
	root := t.TempDir()
	persisted := sb("persisted", time.Unix(100, 0).UTC())
	dir := filepath.Join(root, persisted.Summary.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sandbox: %v", err)
	}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal sandbox: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0o644); err != nil {
		t.Fatalf("write sandbox: %v", err)
	}

	store, err := NewWithConfig(&appconfig.Config{SandboxRoot: root})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cleanupSandboxStore(t, store)
	result, err := store.ListSandboxes(context.Background(), domain.SandboxListOptions{})
	if err != nil {
		t.Fatalf("list immediately after construction: %v", err)
	}
	if got := ids(result.Sandboxes); len(got) != 1 || got[0] != "persisted" {
		t.Fatalf("sandboxes = %v, want [persisted]", got)
	}
}

func TestNewWithConfigUsesFilesystemWhenIndexCannotBeOpened(t *testing.T) {
	root := t.TempDir()
	persisted := writePersistedSandboxForIndexRecovery(t, root, "filesystem-fallback")
	indexPath := filepath.Join(root, "data.db")
	if err := os.Mkdir(indexPath, 0o755); err != nil {
		t.Fatalf("mkdir unusable index path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(indexPath, "blocker"), []byte("not an index"), 0o644); err != nil {
		t.Fatalf("write index path blocker: %v", err)
	}

	store, err := NewWithConfig(&appconfig.Config{SandboxRoot: root})
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	cleanupSandboxStore(t, store)
	if store.index != nil {
		t.Fatal("store index is non-nil after index initialization failure")
	}
	assertSandboxListed(t, store, persisted.Summary.ID)
}

func TestNewWithDatabaseDoesNotCloseSharedDatabase(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "data.db"))
	if err != nil {
		t.Fatalf("open shared database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close shared database: %v", err)
		}
	})
	store, err := NewWithDatabase(&appconfig.Config{SandboxRoot: filepath.Join(root, "sandboxes")}, db)
	if err != nil {
		t.Fatalf("NewWithDatabase: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("shared database was closed by store: %v", err)
	}
}

func TestNewWithConfigRecoversFromCurrentVersionIndexWithMissingColumns(t *testing.T) {
	root := t.TempDir()
	persisted := writePersistedSandboxForIndexRecovery(t, root, "missing-columns")
	path := filepath.Join(root, "data.db")
	idx, _, err := openSandboxCache(path)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	if _, err := idx.db.Exec(`
DROP TABLE sandboxes;
CREATE TABLE sandboxes (
	id TEXT PRIMARY KEY,
	updated_at INTEGER NOT NULL DEFAULT 0,
	vm_status_search TEXT NOT NULL DEFAULT '',
	sandbox_type TEXT NOT NULL DEFAULT ''
);
INSERT INTO sandbox_projection_meta(id, version) VALUES(1, 4)
	ON CONFLICT(id) DO UPDATE SET version = excluded.version;
`); err != nil {
		t.Fatalf("replace index with incomplete schema: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("close index: %v", err)
	}

	store, err := NewWithConfig(&appconfig.Config{SandboxRoot: root})
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	cleanupSandboxStore(t, store)
	if store.index == nil {
		t.Fatal("store degraded instead of rebuilding malformed sandboxes projection table")
	}
	assertSandboxListed(t, store, persisted.Summary.ID)
}

func TestNewWithConfigRecoversWhenReconciliationHitsIndexFailure(t *testing.T) {
	root := t.TempDir()
	persisted := writePersistedSandboxForIndexRecovery(t, root, "reconcile-failure")
	path := filepath.Join(root, "data.db")
	idx, _, err := openSandboxCache(path)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	if err := idx.markComplete(context.Background()); err != nil {
		t.Fatalf("mark index complete: %v", err)
	}
	if _, err := idx.db.Exec(`
CREATE TRIGGER fail_sandbox_reconcile
BEFORE INSERT ON sandboxes
BEGIN
	SELECT RAISE(ABORT, 'simulated damaged index');
END;
`); err != nil {
		t.Fatalf("create failing trigger: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("close index: %v", err)
	}

	store, err := NewWithConfig(&appconfig.Config{SandboxRoot: root})
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	cleanupSandboxStore(t, store)
	assertSandboxListed(t, store, persisted.Summary.ID)
	var triggerCount int
	if err := store.index.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = 'fail_sandbox_reconcile'`).Scan(&triggerCount); err != nil {
		t.Fatalf("query recovered index trigger: %v", err)
	}
	if triggerCount != 0 {
		t.Fatalf("recovered index retained failing trigger")
	}
}

func writePersistedSandboxForIndexRecovery(t *testing.T, root, id string) *domain.Sandbox {
	t.Helper()
	persisted := sb(id, time.Unix(100, 0).UTC())
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sandbox: %v", err)
	}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal sandbox: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0o644); err != nil {
		t.Fatalf("write sandbox metadata: %v", err)
	}
	return persisted
}

func assertSandboxListed(t *testing.T, store *Store, id string) {
	t.Helper()
	result, err := store.ListSandboxes(context.Background(), domain.SandboxListOptions{})
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if got := ids(result.Sandboxes); len(got) != 1 || got[0] != id {
		t.Fatalf("sandboxes = %v, want [%s]", got, id)
	}
}

func TestNewWithConfigReconcilesCurrentIndexWithFilesystem(t *testing.T) {
	root := t.TempDir()
	store, err := NewWithConfig(&appconfig.Config{SandboxRoot: root})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	persisted := seedSandboxDir(t, store, "persisted", time.Unix(100, 0).UTC())
	missing := seedSandboxDir(t, store, "missing", time.Unix(99, 0).UTC())
	if err := store.index.Upsert(context.Background(), persisted); err != nil {
		t.Fatalf("seed index: %v", err)
	}
	if err := store.index.Delete(context.Background(), persisted.Summary.ID); err != nil {
		t.Fatalf("simulate interrupted write-through: %v", err)
	}
	stale := *persisted
	stale.Summary = persisted.Summary
	stale.Summary.Driver = "stale-index-driver"
	stale.Summary.UpdatedAt = persisted.Summary.UpdatedAt.Add(time.Hour)
	if err := store.index.Upsert(context.Background(), &stale); err != nil {
		t.Fatalf("seed inconsistent index row: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := NewWithConfig(&appconfig.Config{SandboxRoot: root})
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	cleanupSandboxStore(t, reopened)
	result, err := reopened.ListSandboxes(context.Background(), domain.SandboxListOptions{})
	if err != nil {
		t.Fatalf("list after restart: %v", err)
	}
	if got := ids(result.Sandboxes); len(got) != 2 || got[0] != "persisted" || got[1] != "missing" {
		t.Fatalf("sandboxes = %v, want [persisted missing]", got)
	}
	var driver string
	var updatedAt int64
	if err := reopened.index.db.QueryRow(`SELECT driver, updated_at FROM sandboxes WHERE id = ?`, persisted.Summary.ID).Scan(&driver, &updatedAt); err != nil {
		t.Fatalf("read reconciled index: %v", err)
	}
	if driver != persisted.Summary.Driver || updatedAt != persisted.Summary.UpdatedAt.UnixNano() {
		t.Fatalf("reconciled index driver/time = %s/%d, want %s/%d", driver, updatedAt, persisted.Summary.Driver, persisted.Summary.UpdatedAt.UnixNano())
	}
	var missingCount int
	if err := reopened.index.db.QueryRow(`SELECT COUNT(*) FROM sandboxes WHERE id = ?`, missing.Summary.ID).Scan(&missingCount); err != nil {
		t.Fatalf("read missing index row: %v", err)
	}
	if missingCount != 1 {
		t.Fatalf("missing index row count = %d, want 1", missingCount)
	}
}

func TestPersistEventSandboxSummaryDoesNotIndexFailedSave(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sandbox := seedSandboxDir(t, store, "event-save-failure", time.Unix(100, 0).UTC())
	store.recordIndex(sandbox)

	metadataPath := filepath.Join(store.sandboxDir(sandbox.Summary.ID), "metadata.json")
	if err := os.Remove(metadataPath); err != nil {
		t.Fatalf("remove metadata: %v", err)
	}
	if err := os.Mkdir(metadataPath, 0o755); err != nil {
		t.Fatalf("replace metadata with directory: %v", err)
	}
	sandbox.Summary.EventCount = 1
	if err := store.persistEventSandboxSummary(sandbox); err == nil {
		t.Fatal("persist event summary returned nil error")
	}

	var indexedUpdatedAt int64
	if err := store.index.db.QueryRowContext(ctx,
		`SELECT updated_at FROM sandboxes WHERE id = ?`, sandbox.Summary.ID,
	).Scan(&indexedUpdatedAt); err != nil {
		t.Fatalf("read index: %v", err)
	}
	if want := time.Unix(100, 0).UTC().UnixNano(); indexedUpdatedAt != want {
		t.Fatalf("indexed updated_at = %d, want %d", indexedUpdatedAt, want)
	}
}

func TestUpdateSandboxSynchronizesIndexAfterRequestCancellation(t *testing.T) {
	store := newTestStore(t)
	sandbox := seedSandboxDir(t, store, "canceled-update", time.Unix(100, 0).UTC())
	store.recordIndex(sandbox)
	sandbox.Summary.Title = "updated after cancellation"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := store.UpdateSandbox(ctx, sandbox); err != nil {
		t.Fatalf("update sandbox: %v", err)
	}
	result, err := store.ListSandboxes(context.Background(), domain.SandboxListOptions{TitleQuery: "after cancellation"})
	if err != nil {
		t.Fatalf("list updated sandbox: %v", err)
	}
	if got := ids(result.Sandboxes); len(got) != 1 || got[0] != sandbox.Summary.ID {
		t.Fatalf("sandboxes = %v, want [%s]", got, sandbox.Summary.ID)
	}
}

func TestListSandboxesRepairsDirtyIndexBeforeQuery(t *testing.T) {
	store := newTestStore(t)
	sandbox := seedSandboxDir(t, store, "dirty-repair", time.Unix(100, 0).UTC())
	store.indexDirty.Store(true)

	result, err := store.ListSandboxes(context.Background(), domain.SandboxListOptions{})
	if err != nil {
		t.Fatalf("list sandboxes: %v", err)
	}
	if got := ids(result.Sandboxes); len(got) != 1 || got[0] != sandbox.Summary.ID {
		t.Fatalf("sandboxes = %v, want [%s]", got, sandbox.Summary.ID)
	}
	if store.indexDirty.Load() {
		t.Fatal("index remained dirty after successful repair")
	}
}

func TestAddEventReturnsSuccessOnceAppendIsCommitted(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sandbox := seedSandboxDir(t, store, "committed-event", time.Unix(100, 0).UTC())
	store.recordIndex(sandbox)
	if err := os.MkdirAll(filepath.Dir(store.eventsJSONLPath(sandbox.Summary.ID)), 0o755); err != nil {
		t.Fatalf("create event state directory: %v", err)
	}
	if err := store.saveEvents(sandbox.Summary.ID, nil); err != nil {
		t.Fatalf("initialize events: %v", err)
	}

	sandbox.Summary.Driver = "invalid-driver"
	if err := store.saveSandbox(sandbox); err != nil {
		t.Fatalf("save invalid metadata: %v", err)
	}
	event := SandboxEvent{ID: "committed", Type: "test", CreatedAt: time.Unix(101, 0).UTC()}
	if err := store.AddEvent(ctx, sandbox.Summary.ID, event); err != nil {
		t.Fatalf("AddEvent returned error after append: %v", err)
	}
	events, err := store.loadEvents(sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(events) != 1 || events[0].ID != event.ID {
		t.Fatalf("events = %#v, want committed event", events)
	}
}

func TestListSandboxesPrunesUnreadableMetadataFromTotal(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	valid := seedSandboxDir(t, store, "valid-metadata", time.Unix(100, 0).UTC())
	broken := seedSandboxDir(t, store, "broken-metadata", time.Unix(101, 0).UTC())
	store.recordIndex(valid)
	store.recordIndex(broken)
	if err := os.WriteFile(filepath.Join(store.sandboxDir(broken.Summary.ID), "metadata.json"), []byte("{"), 0o644); err != nil {
		t.Fatalf("corrupt metadata: %v", err)
	}

	result, err := store.ListSandboxes(ctx, domain.SandboxListOptions{Limit: 1})
	if err != nil {
		t.Fatalf("list sandboxes: %v", err)
	}
	if got := ids(result.Sandboxes); len(got) != 1 || got[0] != valid.Summary.ID {
		t.Fatalf("sandboxes = %v, want [%s]", got, valid.Summary.ID)
	}
	if result.TotalCount != 1 || result.HasMore {
		t.Fatalf("total=%d hasMore=%v, want 1/false", result.TotalCount, result.HasMore)
	}
}

func TestListSandboxesUnknownTypeMatchesNothing(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sandbox := seedSandboxDir(t, store, "manual", time.Unix(100, 0).UTC())
	store.recordIndex(sandbox)

	result, err := store.ListSandboxes(ctx, domain.SandboxListOptions{SandboxType: "scheduled"})
	if err != nil {
		t.Fatalf("list sandboxes: %v", err)
	}
	if result.TotalCount != 0 || len(result.Sandboxes) != 0 {
		t.Fatalf("result = %#v, want no matches", result)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	store, err := NewWithConfig(&appconfig.Config{SandboxRoot: root})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cleanupSandboxStore(t, store)
	return store
}

func seedSandboxDir(t *testing.T, store *Store, id string, updated time.Time) *domain.Sandbox {
	t.Helper()
	s := sb(id, updated)
	if err := os.MkdirAll(store.sandboxDir(id), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := store.saveSandbox(s); err != nil {
		t.Fatalf("save: %v", err)
	}
	return s
}

func TestListSandboxesServesFromIndexWithContract(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for i, id := range []string{"s1", "s2", "s3"} {
		s := seedSandboxDir(t, store, id, time.Unix(int64(100+i), 0).UTC())
		store.recordIndex(s)
	}

	// Page 1 of 2: contract fields populated, newest first.
	res, err := store.ListSandboxes(ctx, domain.SandboxListOptions{Offset: 0, Limit: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if res.TotalCount != 3 || !res.HasMore || res.NextOffset != 2 || len(res.Sandboxes) != 2 {
		t.Fatalf("contract wrong: total=%d hasMore=%v next=%d n=%d", res.TotalCount, res.HasMore, res.NextOffset, len(res.Sandboxes))
	}
	if res.Sandboxes[0].Summary.ID != "s3" {
		t.Fatalf("order wrong: %v", ids(res.Sandboxes))
	}

	res2, _ := store.ListSandboxes(ctx, domain.SandboxListOptions{Offset: 2, Limit: 2})
	if res2.HasMore || len(res2.Sandboxes) != 1 || res2.Sandboxes[0].Summary.ID != "s1" {
		t.Fatalf("page2 wrong: hasMore=%v ids=%v", res2.HasMore, ids(res2.Sandboxes))
	}
}

func TestListSandboxesLazyPrunesVanishedDir(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	s := seedSandboxDir(t, store, "vanish", time.Unix(100, 0).UTC())
	store.recordIndex(s)

	// Remove the directory out-of-band (not via RemoveSandbox), leaving a ghost row.
	if err := os.RemoveAll(store.sandboxDir("vanish")); err != nil {
		t.Fatalf("rm: %v", err)
	}
	res, err := store.ListSandboxes(ctx, domain.SandboxListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Sandboxes) != 0 {
		t.Fatalf("expected ghost skipped from page, got %v", ids(res.Sandboxes))
	}
	// The ghost row should have been pruned lazily.
	var count int
	if err := store.index.db.QueryRow(`SELECT COUNT(*) FROM sandboxes WHERE id='vanish'`).Scan(&count); err != nil {
		t.Fatalf("count ghost rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("ghost row not pruned lazily")
	}
}

func TestListSandboxesRefillsPageAfterPruningGhosts(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	valid := seedSandboxDir(t, store, "valid", time.Unix(100, 0).UTC())
	store.recordIndex(valid)
	if err := store.index.Upsert(ctx, sb("ghost", time.Unix(101, 0).UTC())); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}

	res, err := store.ListSandboxes(ctx, domain.SandboxListOptions{Limit: 1})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := ids(res.Sandboxes); len(got) != 1 || got[0] != "valid" {
		t.Fatalf("sandboxes = %v, want [valid]", got)
	}
	if res.TotalCount != 1 || res.HasMore {
		t.Fatalf("total=%d hasMore=%v, want 1/false", res.TotalCount, res.HasMore)
	}
}

func TestListSandboxesIgnoresGhostsBeforeOffset(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	newer := seedSandboxDir(t, store, "newer", time.Unix(101, 0).UTC())
	older := seedSandboxDir(t, store, "older", time.Unix(100, 0).UTC())
	store.recordIndex(newer)
	store.recordIndex(older)
	if err := store.index.Upsert(ctx, sb("ghost", time.Unix(102, 0).UTC())); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}

	result, err := store.ListSandboxes(ctx, domain.SandboxListOptions{Offset: 1, Limit: 1})
	if err != nil {
		t.Fatalf("list sandboxes: %v", err)
	}
	if got := ids(result.Sandboxes); len(got) != 1 || got[0] != older.Summary.ID {
		t.Fatalf("sandboxes = %v, want [%s]", got, older.Summary.ID)
	}
	if result.TotalCount != 2 || result.HasMore || result.NextOffset != 2 {
		t.Fatalf("total=%d hasMore=%v next=%d, want 2/false/2", result.TotalCount, result.HasMore, result.NextOffset)
	}
}

func TestSaveSandboxWritesThroughToIndex(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sandbox := seedSandboxDir(t, store, "save-sandbox", time.Unix(100, 0).UTC())
	store.recordIndex(sandbox)
	sandbox.Summary.Title = "write-through title"
	sandbox.Summary.UpdatedAt = time.Unix(101, 0).UTC()

	if err := store.SaveSandbox(sandbox); err != nil {
		t.Fatalf("SaveSandbox: %v", err)
	}
	result, err := store.ListSandboxes(ctx, domain.SandboxListOptions{TitleQuery: "write-through"})
	if err != nil {
		t.Fatalf("list sandboxes: %v", err)
	}
	if got := ids(result.Sandboxes); len(got) != 1 || got[0] != sandbox.Summary.ID {
		t.Fatalf("sandboxes = %v, want [%s]", got, sandbox.Summary.ID)
	}
}

func TestSaveSandboxOlderTimestampReplacesIndex(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sandbox := seedSandboxDir(t, store, "older-save", time.Unix(200, 0).UTC())
	store.recordIndex(sandbox)
	sandbox.Summary.Title = "restored older metadata"
	sandbox.Summary.UpdatedAt = time.Unix(100, 0).UTC()

	if err := store.SaveSandbox(sandbox); err != nil {
		t.Fatalf("SaveSandbox: %v", err)
	}
	result, err := store.ListSandboxes(ctx, domain.SandboxListOptions{
		TitleQuery: "restored older", UpdatedTo: time.Unix(150, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("list sandboxes: %v", err)
	}
	if got := ids(result.Sandboxes); len(got) != 1 || got[0] != sandbox.Summary.ID {
		t.Fatalf("sandboxes = %v, want [%s]", got, sandbox.Summary.ID)
	}
}

func TestListSandboxesPreservesSubMillisecondTimeFilters(t *testing.T) {
	base := time.Unix(1_000, 0).UTC()
	tests := []struct {
		name      string
		timestamp time.Time
		options   domain.SandboxListOptions
	}{
		{
			name:      "created from",
			timestamp: base.Add(100 * time.Microsecond),
			options:   domain.SandboxListOptions{CreatedFrom: base.Add(500 * time.Microsecond)},
		},
		{
			name:      "created to",
			timestamp: base.Add(500 * time.Microsecond),
			options:   domain.SandboxListOptions{CreatedTo: base.Add(100 * time.Microsecond)},
		},
		{
			name:      "updated from",
			timestamp: base.Add(100 * time.Microsecond),
			options:   domain.SandboxListOptions{UpdatedFrom: base.Add(500 * time.Microsecond)},
		},
		{
			name:      "updated to",
			timestamp: base.Add(500 * time.Microsecond),
			options:   domain.SandboxListOptions{UpdatedTo: base.Add(100 * time.Microsecond)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			sandbox := seedSandboxDir(t, store, "precise-time", tt.timestamp)
			store.recordIndex(sandbox)
			result, err := store.ListSandboxes(context.Background(), tt.options)
			if err != nil {
				t.Fatalf("list sandboxes: %v", err)
			}
			if result.TotalCount != 0 || len(result.Sandboxes) != 0 {
				t.Fatalf("result = %#v, want no matches", result)
			}
		})
	}
}

func TestSaveEventsWritesUpdatedTimeThroughToIndex(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	newer := seedSandboxDir(t, store, "initially-newer", time.Unix(101, 0).UTC())
	updated := seedSandboxDir(t, store, "event-updated", time.Unix(100, 0).UTC())
	store.recordIndex(newer)
	store.recordIndex(updated)
	if err := os.MkdirAll(filepath.Dir(store.eventsJSONLPath(updated.Summary.ID)), 0o755); err != nil {
		t.Fatalf("create event state directory: %v", err)
	}

	beforeSave := time.Now().UTC().Add(-time.Second)
	if err := store.SaveEvents(updated.Summary.ID, []SandboxEvent{{ID: "saved", Type: "test"}}); err != nil {
		t.Fatalf("SaveEvents: %v", err)
	}
	result, err := store.ListSandboxes(ctx, domain.SandboxListOptions{UpdatedFrom: beforeSave})
	if err != nil {
		t.Fatalf("list sandboxes: %v", err)
	}
	if got := ids(result.Sandboxes); len(got) != 1 || got[0] != updated.Summary.ID {
		t.Fatalf("sandboxes = %v, want [%s]", got, updated.Summary.ID)
	}
}

func TestListSandboxesTreatsSubstringWildcardsLiterally(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	literal := seedSandboxDir(t, store, "literal", time.Unix(101, 0).UTC())
	literal.Summary.Title = `percent% underscore_ slash\`
	literal.Summary.TriggerSource = `script:wild_%\`
	literal.Workspace = &domain.SandboxWorkspace{Name: `workspace_%\`}
	literal.WorkspaceID = "literal-workspace"
	if err := store.UpdateSandbox(ctx, literal); err != nil {
		t.Fatalf("update literal sandbox: %v", err)
	}

	ordinary := seedSandboxDir(t, store, "ordinary", time.Unix(100, 0).UTC())
	ordinary.Summary.Title = "ordinary"
	ordinary.Summary.TriggerSource = "script:ordinary"
	ordinary.Workspace = &domain.SandboxWorkspace{Name: "workspace ordinary"}
	if err := store.UpdateSandbox(ctx, ordinary); err != nil {
		t.Fatalf("update ordinary sandbox: %v", err)
	}

	tests := []struct {
		name    string
		options domain.SandboxListOptions
	}{
		{name: "title percent", options: domain.SandboxListOptions{TitleQuery: "%"}},
		{name: "title underscore", options: domain.SandboxListOptions{TitleQuery: "_"}},
		{name: "title escape", options: domain.SandboxListOptions{TitleQuery: `\`}},
		{name: "trigger source", options: domain.SandboxListOptions{TriggerSourceQuery: `_%\`}},
		{name: "workspace", options: domain.SandboxListOptions{WorkspaceQuery: `_%\`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := store.ListSandboxes(ctx, tt.options)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if got := ids(result.Sandboxes); len(got) != 1 || got[0] != "literal" {
				t.Fatalf("sandboxes = %v, want [literal]", got)
			}
		})
	}
}

func TestListSandboxesMatchesBothWorkspaceIDs(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sandbox := seedSandboxDir(t, store, "workspace-ids", time.Unix(100, 0).UTC())
	sandbox.WorkspaceID = "top-level-workspace"
	sandbox.Workspace = &domain.SandboxWorkspace{ID: "nested-workspace"}
	if err := store.SaveSandbox(sandbox); err != nil {
		t.Fatalf("SaveSandbox: %v", err)
	}

	for _, query := range []string{"top-level-workspace", "nested-workspace"} {
		t.Run(query, func(t *testing.T) {
			result, err := store.ListSandboxes(ctx, domain.SandboxListOptions{WorkspaceQuery: query})
			if err != nil {
				t.Fatalf("list sandboxes: %v", err)
			}
			if got := ids(result.Sandboxes); len(got) != 1 || got[0] != sandbox.Summary.ID {
				t.Fatalf("sandboxes = %v, want [%s]", got, sandbox.Summary.ID)
			}
		})
	}
}

func TestWriteThroughKeepsIndexInSync(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	s := seedSandboxDir(t, store, "wt", time.Unix(100, 0).UTC())

	// UpdateSandbox must upsert the row.
	s.Summary.VMStatus = "STOPPED"
	if err := store.UpdateSandbox(ctx, s); err != nil {
		t.Fatalf("update: %v", err)
	}
	var status string
	if err := store.index.db.QueryRow(`SELECT vm_status FROM sandboxes WHERE id='wt'`).Scan(&status); err != nil {
		t.Fatalf("expected row after update: %v", err)
	}
	if status != "STOPPED" {
		t.Fatalf("index not updated: %s", status)
	}

	// RemoveSandbox must delete the row.
	if err := store.RemoveSandbox(ctx, "wt"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	var count int
	if err := store.index.db.QueryRow(`SELECT COUNT(*) FROM sandboxes WHERE id='wt'`).Scan(&count); err != nil {
		t.Fatalf("count removed index rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("index row not deleted on remove")
	}
}

func indexSchemaVersion(t *testing.T, idx *sandboxCache) int {
	t.Helper()
	var v int
	if err := idx.db.QueryRow(`SELECT COALESCE((SELECT version FROM sandbox_projection_meta WHERE id = 1), 0)`).Scan(&v); err != nil {
		t.Fatalf("read sandbox projection schema version: %v", err)
	}
	return v
}

func TestRebuildIndexMarksCompleteOnlyOnSuccess(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seedSandboxDir(t, store, "s1", time.Unix(100, 0).UTC())

	// Put the index in an "incomplete rebuild" state.
	if _, err := store.index.db.Exec(`DELETE FROM sandbox_projection_meta WHERE id = 1`); err != nil {
		t.Fatalf("reset version: %v", err)
	}

	// An interrupted (cancelled) rebuild must not stamp the index as complete,
	// so the next startup retries it.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	store.rebuildIndex(cctx)
	if v := indexSchemaVersion(t, store.index); v == sandboxCacheVersion {
		t.Fatalf("interrupted rebuild must not mark index complete (version=%d)", v)
	}

	// A rebuild that runs to completion stamps the index as complete.
	store.rebuildIndex(ctx)
	if v := indexSchemaVersion(t, store.index); v != sandboxCacheVersion {
		t.Fatalf("completed rebuild must mark index complete (version=%d)", v)
	}
}

func TestRebuildIndexBackfillsAndPrunesOrphans(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Seed an orphan index row whose directory does not exist.
	if err := store.index.Upsert(ctx, sb("ghost", time.Unix(1, 0).UTC())); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}
	// Create two real sandbox dirs with metadata.json.
	seedSandboxDir(t, store, "real1", time.Unix(100, 0).UTC())
	seedSandboxDir(t, store, "real2", time.Unix(101, 0).UTC())

	store.rebuildIndex(ctx)

	page, total, err := store.index.list(ctx, domain.SandboxListOptions{Limit: 10}, store.sandboxDir)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected 2 real rows after rebuild (ghost pruned), got %d: %v", total, ids(page))
	}
}
