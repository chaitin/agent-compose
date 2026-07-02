//go:build boxlitecgo

package driver

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestCleanupExpiredCacheDirsKeepsCurrentAndFreshEntries(t *testing.T) {
	now := time.Now().UTC()
	root := filepath.Join(t.TempDir(), "image-cache")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir image-cache: %v", err)
	}

	staleDir := filepath.Join(root, "stale")
	currentDir := filepath.Join(root, "current")
	freshDir := filepath.Join(root, "fresh")
	for _, dir := range []string{staleDir, currentDir, freshDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	staleAt := now.Add(-2 * time.Hour)
	if err := os.Chtimes(staleDir, staleAt, staleAt); err != nil {
		t.Fatalf("chtimes stale dir: %v", err)
	}
	if err := os.Chtimes(currentDir, staleAt, staleAt); err != nil {
		t.Fatalf("chtimes current dir: %v", err)
	}

	removed, err := cleanupExpiredCacheDirs(root, time.Hour, map[string]struct{}{"current": {}}, now)
	if err != nil {
		t.Fatalf("cleanupExpiredCacheDirs: %v", err)
	}
	if len(removed) != 1 || removed[0] != staleDir {
		t.Fatalf("removed = %#v, want [%q]", removed, staleDir)
	}
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("stale dir exists after cleanup, err=%v", err)
	}
	if _, err := os.Stat(currentDir); err != nil {
		t.Fatalf("current dir missing after cleanup: %v", err)
	}
	if _, err := os.Stat(freshDir); err != nil {
		t.Fatalf("fresh dir missing after cleanup: %v", err)
	}
}

func TestCleanupExpiredCacheDirsPrunesStaleArtifactsInsideCurrentEntry(t *testing.T) {
	now := time.Now().UTC()
	root := filepath.Join(t.TempDir(), "image-cache")
	currentDir := filepath.Join(root, "current")
	rootfsDir := filepath.Join(currentDir, "rootfs")
	if err := os.MkdirAll(rootfsDir, 0o755); err != nil {
		t.Fatalf("mkdir rootfs dir: %v", err)
	}
	readyFlag := filepath.Join(currentDir, ".rootfs.ready")
	if err := os.WriteFile(readyFlag, []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write rootfs ready flag: %v", err)
	}
	staleAt := now.Add(-2 * time.Hour)
	if err := os.Chtimes(rootfsDir, staleAt, staleAt); err != nil {
		t.Fatalf("chtimes rootfs dir: %v", err)
	}
	if err := os.Chtimes(readyFlag, staleAt, staleAt); err != nil {
		t.Fatalf("chtimes ready flag: %v", err)
	}

	removed, err := cleanupExpiredCacheDirs(root, time.Hour, map[string]struct{}{"current": {}}, now)
	if err != nil {
		t.Fatalf("cleanupExpiredCacheDirs: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed count = %d, want 2 (%#v)", len(removed), removed)
	}
	if _, err := os.Stat(currentDir); err != nil {
		t.Fatalf("current dir missing after artifact cleanup: %v", err)
	}
	if _, err := os.Stat(rootfsDir); !os.IsNotExist(err) {
		t.Fatalf("rootfs dir exists after cleanup, err=%v", err)
	}
	if _, err := os.Stat(readyFlag); !os.IsNotExist(err) {
		t.Fatalf("ready flag exists after cleanup, err=%v", err)
	}
}

func TestHasActiveBoxliteBoxes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "boxlite.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE box_state (id TEXT PRIMARY KEY NOT NULL, status TEXT NOT NULL, pid INTEGER, json TEXT NOT NULL);`); err != nil {
		t.Fatalf("create box_state: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO box_state(id, status, pid, json) VALUES('stopped-box', 'stopped', NULL, '{}');`); err != nil {
		t.Fatalf("insert stopped box: %v", err)
	}
	active, err := hasActiveBoxliteBoxes(dbPath)
	if err != nil {
		t.Fatalf("hasActiveBoxliteBoxes(stopped): %v", err)
	}
	if active {
		t.Fatalf("active = true, want false")
	}
	if _, err := db.Exec(`INSERT INTO box_state(id, status, pid, json) VALUES('running-box', 'running', 123, '{}');`); err != nil {
		t.Fatalf("insert running box: %v", err)
	}
	active, err = hasActiveBoxliteBoxes(dbPath)
	if err != nil {
		t.Fatalf("hasActiveBoxliteBoxes(running): %v", err)
	}
	if !active {
		t.Fatalf("active = false, want true")
	}
}

func TestCleanupLegacyBoxliteImageCachesRemovesChildren(t *testing.T) {
	boxliteHome := t.TempDir()
	paths := []string{
		filepath.Join(boxliteHome, "images", "local", "legacy-a", "layer.txt"),
		filepath.Join(boxliteHome, "images", "disk-images", "legacy-b.ext4"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir parent for %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	removed, err := cleanupLegacyBoxliteImageCaches(boxliteHome)
	if err != nil {
		t.Fatalf("cleanupLegacyBoxliteImageCaches: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed count = %d, want 2 (%#v)", len(removed), removed)
	}
	for _, path := range []string{
		filepath.Join(boxliteHome, "images", "local", "legacy-a"),
		filepath.Join(boxliteHome, "images", "disk-images", "legacy-b.ext4"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("path %s exists after cleanup, err=%v", path, err)
		}
	}
}
