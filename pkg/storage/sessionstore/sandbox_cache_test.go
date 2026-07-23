package sessionstore

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	domain "agent-compose/pkg/model"
	storagesqlite "agent-compose/pkg/storage/sqlite"
)

func newTestIndex(t *testing.T) *sandboxCache {
	t.Helper()
	idx, _, err := openSandboxCache(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if err := idx.Close(); err != nil {
			t.Errorf("close test index: %v", err)
		}
	})
	return idx
}

func sb(id string, updated time.Time) *domain.Sandbox {
	return &domain.Sandbox{Summary: domain.SandboxSummary{
		ID: id, Driver: "docker", VMStatus: "RUNNING",
		TriggerSource: "manual", Title: "t-" + id,
		CreatedAt: updated, UpdatedAt: updated,
	}}
}

func ids(page []*domain.Sandbox) []string {
	out := make([]string, 0, len(page))
	for _, s := range page {
		out = append(out, s.Summary.ID)
	}
	return out
}

func TestOpenSandboxCacheCreatesSchemaAndDetectsVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")

	// First open on a fresh file: schema created, rebuild needed (was version 0).
	idx, needsRebuild, err := openSandboxCache(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !needsRebuild {
		t.Fatalf("fresh index should need rebuild")
	}
	if _, err := idx.db.Exec(`INSERT INTO sandboxes(id, updated_at) VALUES('a', 1)`); err != nil {
		t.Fatalf("schema missing: %v", err)
	}
	// Stamp completion, as a finished rebuild would, so the reopen sees a current
	// index rather than one still awaiting a rebuild.
	if err := idx.markComplete(context.Background()); err != nil {
		t.Fatalf("markComplete: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("close index: %v", err)
	}

	// Reopen at the same version: no rebuild needed, data preserved.
	idx2, needsRebuild2, err := openSandboxCache(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if needsRebuild2 {
		t.Fatalf("same-version reopen should not need rebuild")
	}
	var count int
	if err := idx2.db.QueryRow(`SELECT COUNT(*) FROM sandboxes`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected preserved row, got %d", count)
	}
	if err := idx2.Close(); err != nil {
		t.Fatalf("close reopened index: %v", err)
	}
}

func TestSandboxCacheNeedsRebuildUntilMarkedComplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	ctx := context.Background()

	// Fresh open needs a rebuild.
	idx, needsRebuild, err := openSandboxCache(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !needsRebuild {
		t.Fatalf("fresh index should need rebuild")
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("close incomplete index: %v", err)
	}

	// Simulate a rebuild that started but never completed (crash/shutdown): the
	// index must still report needsRebuild on the next open so the caller retries
	// instead of trusting a partially-populated index.
	idx2, needsRebuild2, err := openSandboxCache(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !needsRebuild2 {
		t.Fatalf("index must still need rebuild until a rebuild is marked complete")
	}
	if err := idx2.markComplete(ctx); err != nil {
		t.Fatalf("markComplete: %v", err)
	}
	if err := idx2.Close(); err != nil {
		t.Fatalf("close completed index: %v", err)
	}

	// After completion, the index is authoritative and no longer needs rebuild.
	idx3, needsRebuild3, err := openSandboxCache(path)
	if err != nil {
		t.Fatalf("reopen after complete: %v", err)
	}
	if needsRebuild3 {
		t.Fatalf("completed index should not need rebuild")
	}
	if err := idx3.Close(); err != nil {
		t.Fatalf("close current index: %v", err)
	}
}

func TestSandboxCacheRebuildsPreviousSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	idx, _, err := openSandboxCache(path)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	if _, err := idx.db.Exec(`INSERT INTO sandboxes(id, updated_at) VALUES('old', 123);
		INSERT INTO sandbox_projection_meta(id, version) VALUES(1, 3)`); err != nil {
		t.Fatalf("seed version 3 index: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("close index: %v", err)
	}

	reopened, needsRebuild, err := openSandboxCache(path)
	if err != nil {
		t.Fatalf("reopen index: %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened index: %v", err)
		}
	}()
	if !needsRebuild {
		t.Fatal("version 3 index did not require rebuild")
	}
	var count int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM sandboxes`).Scan(&count); err != nil {
		t.Fatalf("count rebuilt index: %v", err)
	}
	if count != 0 {
		t.Fatalf("rebuilt index retained %d old rows", count)
	}
}

func TestOpenSandboxCacheDoesNotDeleteCorruptDataDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	content := []byte("this is not a sqlite database")
	// Write garbage that is not a valid SQLite database.
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	idx, _, err := openSandboxCache(path)
	if err == nil {
		if closeErr := idx.Close(); closeErr != nil {
			t.Fatalf("close unexpectedly opened database: %v", closeErr)
		}
		t.Fatal("openSandboxCache accepted corrupt data.db")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read data.db after failed open: %v", readErr)
	}
	if string(got) != string(content) {
		t.Fatalf("corrupt data.db was modified: got %q, want %q", got, content)
	}
}

func TestSandboxCacheListFiltersOrderKeysetOffset(t *testing.T) {
	idx := newTestIndex(t)
	ctx := context.Background()
	dir := func(id string) string { return "/root/" + id }

	base := time.Unix(10_000, 0).UTC()
	a := sb("a", base.Add(1*time.Second))
	b := sb("b", base.Add(2*time.Second))
	c := sb("c", base.Add(3*time.Second))
	b.Summary.Driver = "boxlite"
	for _, s := range []*domain.Sandbox{a, b, c} {
		if err := idx.Upsert(ctx, s, ""); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}

	// Default order is updated_at DESC -> c, b, a.
	page, total, err := idx.list(ctx, domain.SandboxListOptions{Limit: 10}, dir)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 3 || len(page) != 3 || page[0].Summary.ID != "c" || page[2].Summary.ID != "a" {
		t.Fatalf("order/total wrong: total=%d ids=%v", total, ids(page))
	}
	if page[0].Summary.WorkspacePath != "/root/c/workspace" {
		t.Fatalf("workspace path not recomputed: %s", page[0].Summary.WorkspacePath)
	}

	// Driver filter.
	page, total, err = idx.list(ctx, domain.SandboxListOptions{Driver: "boxlite", Limit: 10}, dir)
	if err != nil {
		t.Fatalf("list by driver: %v", err)
	}
	if total != 1 || len(page) != 1 || page[0].Summary.ID != "b" {
		t.Fatalf("driver filter wrong: %v", ids(page))
	}

	// Keyset: everything strictly older than c -> b, a.
	page, _, err = idx.list(ctx, domain.SandboxListOptions{
		BeforeUpdatedAt: c.Summary.UpdatedAt, BeforeID: "c", Limit: 10,
	}, dir)
	if err != nil {
		t.Fatalf("list by cursor: %v", err)
	}
	if len(page) != 2 || page[0].Summary.ID != "b" {
		t.Fatalf("keyset wrong: %v", ids(page))
	}

	// Offset pagination: page size 2 -> [c,b] then [a].
	page, total, err = idx.list(ctx, domain.SandboxListOptions{Offset: 0, Limit: 2}, dir)
	if err != nil {
		t.Fatalf("list first offset page: %v", err)
	}
	if total != 3 || len(page) != 2 {
		t.Fatalf("offset page1 wrong: total=%d n=%d", total, len(page))
	}
	page, _, err = idx.list(ctx, domain.SandboxListOptions{Offset: 2, Limit: 2}, dir)
	if err != nil {
		t.Fatalf("list second offset page: %v", err)
	}
	if len(page) != 1 || page[0].Summary.ID != "a" {
		t.Fatalf("offset page2 wrong: %v", ids(page))
	}
}

func TestSandboxCacheListFiltersProjectAndStatuses(t *testing.T) {
	idx := newTestIndex(t)
	ctx := context.Background()
	dir := func(id string) string { return "/root/" + id }
	base := time.Unix(10_000, 0).UTC()

	running := sb("project-running", base.Add(2*time.Second))
	stopped := sb("project-stopped", base.Add(time.Second))
	stopped.Summary.VMStatus = "STOPPED"
	other := sb("other-running", base)
	if err := idx.Upsert(ctx, running, "project-a"); err != nil {
		t.Fatalf("upsert running: %v", err)
	}
	if err := idx.Upsert(ctx, stopped, "project-a"); err != nil {
		t.Fatalf("upsert stopped: %v", err)
	}
	if err := idx.Upsert(ctx, other, "project-b"); err != nil {
		t.Fatalf("upsert other: %v", err)
	}

	page, total, err := idx.list(ctx, domain.SandboxListOptions{
		ProjectID: "PROJECT-A", VMStatuses: []string{"running", "stopped"}, Limit: 10,
	}, dir)
	if err != nil {
		t.Fatalf("list by project and statuses: %v", err)
	}
	got := ids(page)
	if total != 2 || len(got) != 2 || got[0] != "project-running" || got[1] != "project-stopped" {
		t.Fatalf("project/status result total=%d ids=%v", total, got)
	}
}

func TestSandboxCacheUpsertAcceptsAuthoritativeOlderTimestamp(t *testing.T) {
	idx := newTestIndex(t)
	ctx := context.Background()
	t0 := time.Unix(1000, 0).UTC()
	t1 := time.Unix(2000, 0).UTC()

	if err := idx.Upsert(ctx, sb("x", t1), ""); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// A successfully committed older timestamp remains authoritative.
	stale := sb("x", t0)
	stale.Summary.Driver = "STALE"
	if err := idx.Upsert(ctx, stale, ""); err != nil {
		t.Fatalf("stale upsert: %v", err)
	}
	var driver string
	var updated int64
	if err := idx.db.QueryRow(`SELECT driver, updated_at FROM sandboxes WHERE id='x'`).Scan(&driver, &updated); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if driver != "STALE" || updated != t0.UnixNano() {
		t.Fatalf("authoritative older write missing: driver=%s updated=%d", driver, updated)
	}

	if err := idx.Delete(ctx, "x"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var count int
	if err := idx.db.QueryRow(`SELECT COUNT(*) FROM sandboxes`).Scan(&count); err != nil {
		t.Fatalf("count rows after delete: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 rows after delete, got %d", count)
	}
}

func TestSandboxCacheSharesDataDatabaseForJoinsAndIsolatedRebuilds(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatalf("open shared data database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close shared data database: %v", err)
		}
	})
	ctx := context.Background()
	if err := storagesqlite.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate shared data database: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE sandbox_owner (sandbox_id TEXT PRIMARY KEY, project_id TEXT NOT NULL);
		CREATE TABLE unrelated_state (id TEXT PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO sandbox_owner(sandbox_id, project_id) VALUES('sandbox-1', 'project-1');
		INSERT INTO unrelated_state(id, value) VALUES('keep', 'authoritative');
	`); err != nil {
		t.Fatalf("seed shared data database: %v", err)
	}
	idx, _, err := openSandboxCacheDB(ctx, db)
	if err != nil {
		t.Fatalf("open sandbox listing cache on shared database: %v", err)
	}
	if err := idx.Upsert(ctx, sb("sandbox-1", time.Unix(100, 0).UTC()), ""); err != nil {
		t.Fatalf("upsert sandbox listing cache: %v", err)
	}
	if err := idx.markComplete(ctx); err != nil {
		t.Fatalf("mark sandbox listing cache complete: %v", err)
	}
	var projectID string
	if err := db.QueryRowContext(ctx, `SELECT owner.project_id FROM sandboxes si JOIN sandbox_owner owner ON owner.sandbox_id = si.id WHERE si.id = ?`, "sandbox-1").Scan(&projectID); err != nil {
		t.Fatalf("join sandbox listing cache with project run: %v", err)
	}
	if projectID != "project-1" {
		t.Fatalf("joined project id = %q, want project-1", projectID)
	}

	if _, err := db.ExecContext(ctx, `UPDATE sandbox_projection_meta SET version = 0 WHERE id = 1`); err != nil {
		t.Fatalf("make sandbox projection schema stale: %v", err)
	}
	if _, needsRebuild, err := openSandboxCacheDB(ctx, db); err != nil {
		t.Fatalf("reopen stale sandbox listing cache: %v", err)
	} else if !needsRebuild {
		t.Fatal("stale sandbox listing cache did not request rebuild")
	}
	var value string
	if err := db.QueryRowContext(ctx, `SELECT value FROM unrelated_state WHERE id = 'keep'`).Scan(&value); err != nil {
		t.Fatalf("query unrelated state after index rebuild: %v", err)
	}
	if value != "authoritative" {
		t.Fatalf("unrelated state = %q, want authoritative", value)
	}

	if _, err := db.ExecContext(ctx, `DROP TABLE sandbox_projection_meta; CREATE TABLE sandbox_projection_meta (broken TEXT)`); err != nil {
		t.Fatalf("malform sandbox projection metadata: %v", err)
	}
	if _, _, err := openSandboxCacheDB(ctx, db); err == nil {
		t.Fatal("malformed migration-owned sandbox projection metadata returned no error")
	}
	if err := db.QueryRowContext(ctx, `SELECT value FROM unrelated_state WHERE id = 'keep'`).Scan(&value); err != nil {
		t.Fatalf("query unrelated state after metadata recovery: %v", err)
	}
	if value != "authoritative" {
		t.Fatalf("unrelated state after metadata recovery = %q, want authoritative", value)
	}
}
