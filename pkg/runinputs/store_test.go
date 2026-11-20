package runinputs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func plentyOfSpace(string) (uint64, uint64, error) { return 1 << 40, 1 << 40, nil }

func openTestStore(t *testing.T, root string, clock *testClock) *Store {
	t.Helper()
	store, err := Open(Options{
		Root:           root,
		MaxObjectBytes: 1 << 10,
		MaxStoreBytes:  8 << 10,
		TTL:            15 * time.Minute,
		Now:            clock.Now,
		FreeSpace:      plentyOfSpace,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func newClock() *testClock {
	return &testClock{now: time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)}
}

func upload(t *testing.T, store *Store, project string, data []byte) (Object, bool) {
	t.Helper()
	pending, existing, err := store.Begin(project, int64(len(data)), digestOf(data))
	if err != nil {
		t.Fatal(err)
	}
	if existing != nil {
		return *existing, true
	}
	if err := pending.Write(data[:len(data)/2]); err != nil {
		t.Fatal(err)
	}
	if err := pending.Write(data[len(data)/2:]); err != nil {
		t.Fatal(err)
	}
	object, deduplicated, err := pending.Commit()
	if err != nil {
		t.Fatal(err)
	}
	return object, deduplicated
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestUploadCommitPublishesReadOnlyContent(t *testing.T) {
	root := t.TempDir()
	clock := newClock()
	store := openTestStore(t, root, clock)
	data := []byte("alpha,beta\n1,2\n")

	object, deduplicated := upload(t, store, "proj-1", data)
	if deduplicated {
		t.Fatal("first upload reported deduplicated")
	}
	if object.UploadID == "" || object.UploadID == object.SHA256 {
		t.Fatalf("upload id = %q, want an opaque id", object.UploadID)
	}
	if object.SizeBytes != int64(len(data)) || object.SHA256 != digestOf(data) {
		t.Fatalf("object = %+v", object)
	}
	if !object.ExpiresAt.Equal(clock.Now().Add(15 * time.Minute)) {
		t.Fatalf("expires at = %s", object.ExpiresAt)
	}
	content, err := os.ReadFile(object.Path)
	if err != nil || string(content) != string(data) {
		t.Fatalf("content = %q, %v", content, err)
	}
	info, err := os.Stat(object.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o444 {
		t.Fatalf("content mode = %v, want read-only", info.Mode().Perm())
	}
	if names := listDir(t, filepath.Join(root, "proj-1")); len(names) != 2 {
		t.Fatalf("project dir = %v, want content and metadata only", names)
	}
}

func TestCommitRefreshesContentMtime(t *testing.T) {
	clock := newClock()
	store := openTestStore(t, t.TempDir(), clock)
	data := []byte("paused upload")
	pending, _, err := store.Begin("proj-1", int64(len(data)), digestOf(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Write(data); err != nil {
		t.Fatal(err)
	}
	// A paused upload can reach Commit with a temp file older than the residue
	// horizon; the published content must not inherit that timestamp.
	old := clock.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(pending.file.Name(), old, old); err != nil {
		t.Fatal(err)
	}
	object, _, err := pending.Commit()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(object.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().Before(clock.Now().Add(-time.Minute)) {
		t.Fatalf("content mtime = %s, want a timestamp near %s", info.ModTime(), clock.Now())
	}
}

func TestUploadRejectsMismatchAndLeavesNothing(t *testing.T) {
	root := t.TempDir()
	store := openTestStore(t, root, newClock())
	data := []byte("payload")

	pending, _, err := store.Begin("proj-1", int64(len(data)), digestOf([]byte("other")))
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Write(data); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pending.Commit(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("commit with wrong digest = %v, want ErrInvalid", err)
	}

	pending, _, err = store.Begin("proj-1", int64(len(data)), digestOf(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Write(data[:3]); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pending.Commit(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("commit of a short upload = %v, want ErrInvalid", err)
	}

	pending, _, err = store.Begin("proj-1", int64(len(data)), digestOf(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Write(append(data, 'x')); !errors.Is(err, ErrInvalid) {
		t.Fatalf("write past declared size = %v, want ErrInvalid", err)
	}
	pending.Abort()

	if names := listDir(t, filepath.Join(root, "proj-1")); len(names) != 0 {
		t.Fatalf("project dir = %v, want no residue", names)
	}
	if store.used != 0 || store.reserved != 0 || len(store.project) != 0 {
		t.Fatalf("accounting leaked: used=%d reserved=%d project=%v", store.used, store.reserved, store.project)
	}
}

func TestBeginValidatesRequest(t *testing.T) {
	store := openTestStore(t, t.TempDir(), newClock())
	digest := digestOf([]byte("x"))
	for name, call := range map[string]func() error{
		"empty project":    func() error { _, _, err := store.Begin("", 1, digest); return err },
		"escaping project": func() error { _, _, err := store.Begin("../x", 1, digest); return err },
		"zero size":        func() error { _, _, err := store.Begin("p", 0, digest); return err },
		"over limit":       func() error { _, _, err := store.Begin("p", 2<<10, digest); return err },
		"bad digest":       func() error { _, _, err := store.Begin("p", 1, "ABC"); return err },
	} {
		if err := call(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestBeginDeduplicatesWithinProjectOnly(t *testing.T) {
	clock := newClock()
	store := openTestStore(t, t.TempDir(), clock)
	data := []byte("same bytes")
	first, _ := upload(t, store, "proj-1", data)

	clock.Advance(10 * time.Minute)
	again, deduplicated := upload(t, store, "proj-1", data)
	if !deduplicated || again.UploadID != first.UploadID {
		t.Fatalf("second upload = %+v dedup=%v, want the first object", again, deduplicated)
	}
	if !again.ExpiresAt.Equal(clock.Now().Add(15 * time.Minute)) {
		t.Fatalf("dedup did not extend expiry: %s", again.ExpiresAt)
	}

	other, deduplicated := upload(t, store, "proj-2", data)
	if deduplicated || other.UploadID == first.UploadID {
		t.Fatal("content was shared across projects")
	}

	if _, _, err := store.Begin("proj-1", int64(len(data))+1, digestOf(data)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("same digest with a different size = %v, want ErrInvalid", err)
	}
}

func TestConcurrentUploadsOfSameContentKeepOneObject(t *testing.T) {
	root := t.TempDir()
	store := openTestStore(t, root, newClock())
	data := []byte("raced")
	first, _, err := store.Begin("proj-1", int64(len(data)), digestOf(data))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.Begin("proj-1", int64(len(data)), digestOf(data))
	if err != nil {
		t.Fatal(err)
	}
	for _, pending := range []*Upload{first, second} {
		if err := pending.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	winner, deduplicated, err := first.Commit()
	if err != nil || deduplicated {
		t.Fatalf("first commit = %v dedup=%v", err, deduplicated)
	}
	loser, deduplicated, err := second.Commit()
	if err != nil || !deduplicated || loser.UploadID != winner.UploadID {
		t.Fatalf("second commit = %+v dedup=%v err=%v, want the first object", loser, deduplicated, err)
	}
	if names := listDir(t, filepath.Join(root, "proj-1")); len(names) != 2 {
		t.Fatalf("project dir = %v, want one object", names)
	}
	if store.used != int64(len(data)) || store.reserved != 0 || store.project["proj-1"] != int64(len(data)) {
		t.Fatalf("accounting: used=%d reserved=%d project=%v", store.used, store.reserved, store.project)
	}
}

func TestBeginEnforcesCapacity(t *testing.T) {
	clock := newClock()
	store := openTestStore(t, t.TempDir(), clock)
	block := make([]byte, 1<<10)

	// The per-project share is a quarter of the 8 KiB store.
	for index := range 2 {
		block[0] = byte(index)
		upload(t, store, "proj-1", block)
	}
	block[0] = 9
	if _, _, err := store.Begin("proj-1", int64(len(block)), digestOf(block)); !errors.Is(err, ErrExhausted) {
		t.Fatalf("over project share = %v, want ErrExhausted", err)
	}
	for index, project := range []string{"proj-2", "proj-2", "proj-3", "proj-3", "proj-4", "proj-4"} {
		block[0] = byte(10 + index)
		upload(t, store, project, block)
	}
	block[0] = 99
	if _, _, err := store.Begin("proj-5", int64(len(block)), digestOf(block)); !errors.Is(err, ErrExhausted) {
		t.Fatalf("over store limit = %v, want ErrExhausted", err)
	}

	// Expired objects stop counting at once.
	clock.Advance(16 * time.Minute)
	if pending, _, err := store.Begin("proj-5", int64(len(block)), digestOf(block)); err != nil {
		t.Fatalf("after expiry = %v, want admitted", err)
	} else {
		pending.Abort()
	}
}

func TestUploadChargesCapacityAsBytesArrive(t *testing.T) {
	clock := newClock()
	store := openTestStore(t, t.TempDir(), clock)
	block := make([]byte, 1<<10)

	// The project share is a quarter of the 8 KiB store. All three uploads are
	// admitted together because Begin checks the declared size but charges
	// nothing yet.
	pending := make([]*Upload, 0, 3)
	for index := range 3 {
		block[0] = byte(index)
		upload, existing, err := store.Begin("proj-1", int64(len(block)), digestOf(block))
		if err != nil || existing != nil {
			t.Fatalf("Begin %d = %v existing=%v", index, err, existing)
		}
		pending = append(pending, upload)
	}
	if store.reserved != 0 || store.project["proj-1"] != 0 {
		t.Fatalf("Begin charged bytes that were not received: reserved=%d project=%v", store.reserved, store.project)
	}

	// The first two chunks fill the project share; the third is rejected when
	// its bytes arrive, not when it was declared.
	for index, upload := range pending {
		err := upload.Write(block)
		if index < 2 && err != nil {
			t.Fatalf("write %d = %v", index, err)
		}
		if index == 2 && !errors.Is(err, ErrExhausted) {
			t.Fatalf("write over the project share = %v, want ErrExhausted", err)
		}
	}
	for _, upload := range pending {
		upload.Abort()
	}
	if store.used != 0 || store.reserved != 0 || len(store.project) != 0 {
		t.Fatalf("accounting leaked: used=%d reserved=%d project=%v", store.used, store.reserved, store.project)
	}
}

func TestBeginRejectsLowFreeSpace(t *testing.T) {
	store := openTestStore(t, t.TempDir(), newClock())
	store.opts.FreeSpace = func(string) (uint64, uint64, error) { return 100 << 10, 1 << 20, nil }
	data := make([]byte, 1<<10)
	if _, _, err := store.Begin("proj-1", int64(len(data)), digestOf(data)); !errors.Is(err, ErrExhausted) {
		t.Fatalf("below free-space floor = %v, want ErrExhausted", err)
	}
	store.opts.FreeSpace = func(string) (uint64, uint64, error) { return 0, 0, errors.New("statfs unsupported") }
	pending, _, err := store.Begin("proj-1", int64(len(data)), digestOf(data))
	if err != nil {
		t.Fatalf("unreadable free space = %v, want the byte budgets to decide", err)
	}
	pending.Abort()
}

func TestResolveScopesAndExtends(t *testing.T) {
	clock := newClock()
	store := openTestStore(t, t.TempDir(), clock)
	object, _ := upload(t, store, "proj-1", []byte("content"))

	if _, err := store.Resolve("proj-2", object.UploadID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other project = %v, want ErrNotFound", err)
	}
	if _, err := store.Resolve("proj-1", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id = %v, want ErrNotFound", err)
	}

	clock.Advance(14 * time.Minute)
	resolved, err := store.Resolve("proj-1", object.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.ExpiresAt.Equal(clock.Now().Add(15 * time.Minute)) {
		t.Fatalf("resolve did not extend expiry: %s", resolved.ExpiresAt)
	}

	clock.Advance(15 * time.Minute)
	if _, err := store.Resolve("proj-1", object.UploadID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(object.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired content still exists: %v", err)
	}
}

func TestOpenReloadsObjectsAndRemovesResidue(t *testing.T) {
	root := t.TempDir()
	clock := newClock()
	store := openTestStore(t, root, clock)
	live, _ := upload(t, store, "proj-1", []byte("live"))
	expiring, _ := upload(t, store, "proj-1", []byte("expiring"))
	pending, _, err := store.Begin("proj-1", 4, digestOf([]byte("temp")))
	if err != nil {
		t.Fatal(err)
	}
	_ = pending.Write([]byte("te"))
	// A crash leaves the temp file and content without metadata.
	orphan := filepath.Join(root, "proj-1", "orphan-content")
	if err := os.WriteFile(orphan, []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(expiring.Path + metadataSuffix); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, root, clock)
	if _, err := reopened.Resolve("proj-1", live.UploadID); err != nil {
		t.Fatalf("live object after reopen = %v", err)
	}
	if _, err := reopened.Resolve("proj-1", expiring.UploadID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("object without metadata after reopen = %v, want ErrNotFound", err)
	}
	if names := listDir(t, filepath.Join(root, "proj-1")); len(names) != 2 {
		t.Fatalf("project dir after reopen = %v, want only the live object", names)
	}
	if reopened.used != live.SizeBytes {
		t.Fatalf("reopened usage = %d, want %d", reopened.used, live.SizeBytes)
	}
}

func TestCollectRemovesExpiredObjectsAndAgedEmptyDirs(t *testing.T) {
	root := t.TempDir()
	clock := newClock()
	store := openTestStore(t, root, clock)
	object, _ := upload(t, store, "proj-1", []byte("short-lived"))
	inFlight, _, err := store.Begin("proj-1", 4, digestOf([]byte("wait")))
	if err != nil {
		t.Fatal(err)
	}
	defer inFlight.Abort()
	old := clock.Now().Add(-2 * time.Hour)
	// Runtime cleanup must never touch an in-progress temp file, however old.
	if err := os.Chtimes(inFlight.file.Name(), old, old); err != nil {
		t.Fatal(err)
	}
	emptyProject := filepath.Join(root, "proj-empty")
	if err := os.MkdirAll(emptyProject, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(emptyProject, old, old); err != nil {
		t.Fatal(err)
	}

	clock.Advance(16 * time.Minute)
	if removed := store.Collect(); removed != 1 {
		t.Fatalf("collected %d objects, want 1", removed)
	}
	if _, err := os.Stat(object.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired content survived collection")
	}
	if _, err := os.Stat(inFlight.file.Name()); err != nil {
		t.Fatalf("in-flight upload was collected: %v", err)
	}
	if _, err := os.Stat(emptyProject); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aged empty project directory survived collection: %v", err)
	}
}

func TestConcurrentExtensionsPersistTheLatestExpiry(t *testing.T) {
	root := t.TempDir()
	clock := newClock()
	store := openTestStore(t, root, clock)
	object, _ := upload(t, store, "proj-1", []byte("shared"))

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			clock.Advance(time.Second)
			if _, err := store.Resolve("proj-1", object.UploadID); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	latest, err := store.Resolve("proj-1", object.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, root, clock)
	reloaded, err := reopened.Resolve("proj-1", object.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	// Reopening resolves once more, which only extends from the same clock.
	if !reloaded.ExpiresAt.Equal(latest.ExpiresAt) {
		t.Fatalf("persisted expiry = %s, want the latest in-memory expiry %s", reloaded.ExpiresAt, latest.ExpiresAt)
	}
}

func TestResolveAfterDropDoesNotResurrectMetadata(t *testing.T) {
	root := t.TempDir()
	clock := newClock()
	store := openTestStore(t, root, clock)
	object, _ := upload(t, store, "proj-1", []byte("short"))

	clock.Advance(16 * time.Minute)
	store.Collect()
	if _, err := store.Resolve("proj-1", object.UploadID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve after collection = %v, want ErrNotFound", err)
	}
	if names := listDir(t, filepath.Join(root, "proj-1")); len(names) != 0 {
		t.Fatalf("project dir = %v, want nothing left", names)
	}
}
