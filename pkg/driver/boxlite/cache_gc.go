//go:build boxlitecgo

package boxlitedriver

import (
	. "agent-compose/pkg/driver/types"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const boxliteCacheGCMinInterval = 5 * time.Minute

type boxliteCacheGCState struct {
	mu      sync.Mutex
	lastRun time.Time
}

func (r *cgoBoxRuntime) maybeRunCacheGC(currentImageID string) {
	if r == nil || r.config == nil {
		return
	}
	now := time.Now().UTC()
	r.cache.mu.Lock()
	if !r.cache.lastRun.IsZero() && now.Sub(r.cache.lastRun) < boxliteCacheGCMinInterval {
		r.cache.mu.Unlock()
		return
	}
	r.cache.lastRun = now
	r.cache.mu.Unlock()

	if ttl := r.config.BoxCacheTTL; ttl > 0 {
		removed, err := cleanupExpiredCacheDirs(filepath.Join(r.config.DataRoot, "image-cache"), ttl, map[string]struct{}{strings.TrimSpace(currentImageID): {}}, now)
		if err != nil {
			slog.Warn("agent-compose boxlite cache gc failed to prune stale image cache", "error", err)
		} else if len(removed) > 0 {
			slog.Info("agent-compose boxlite cache gc pruned stale image cache", "count", len(removed))
		}
	}
}

func (r *cgoBoxRuntime) cleanupLegacyBoxliteCaches() {
	if r == nil || r.config == nil {
		return
	}
	removed, err := cleanupLegacyBoxliteImageCaches(r.config.BoxliteHome)
	if err != nil {
		slog.Warn("agent-compose boxlite cache gc failed to prune legacy boxlite caches", "error", err)
		return
	}
	if len(removed) > 0 {
		slog.Info("agent-compose boxlite cache gc pruned legacy boxlite caches", "count", len(removed))
	}
}

func cleanupExpiredCacheDirs(root string, ttl time.Duration, keepIDs map[string]struct{}, now time.Time) ([]string, error) {
	if ttl <= 0 {
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read cache root %s: %w", root, err)
	}
	removed := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" {
			continue
		}
		if _, keep := keepIDs[name]; keep {
			deleted, err := cleanupExpiredImageCacheArtifacts(filepath.Join(root, name), ttl, now)
			if err != nil {
				return removed, err
			}
			removed = append(removed, deleted...)
			continue
		}
		path := filepath.Join(root, name)
		info, err := entry.Info()
		if err != nil {
			return removed, fmt.Errorf("stat cache dir %s: %w", path, err)
		}
		if now.Sub(info.ModTime()) < ttl {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return removed, fmt.Errorf("remove cache dir %s: %w", path, err)
		}
		removed = append(removed, path)
	}
	return removed, nil
}

func cleanupExpiredImageCacheArtifacts(cacheDir string, ttl time.Duration, now time.Time) ([]string, error) {
	if ttl <= 0 {
		return nil, nil
	}
	removed := make([]string, 0)
	for _, name := range []string{"rootfs", "rootfs.tmp", "oci.tmp"} {
		path := filepath.Join(cacheDir, name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fmt.Errorf("stat cache artifact %s: %w", path, err)
		}
		if now.Sub(info.ModTime()) < ttl {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return removed, fmt.Errorf("remove cache artifact %s: %w", path, err)
		}
		removed = append(removed, path)
	}
	readyFlag := filepath.Join(cacheDir, ".rootfs.ready")
	if info, err := os.Stat(readyFlag); err == nil {
		if now.Sub(info.ModTime()) >= ttl {
			if err := os.Remove(readyFlag); err != nil && !os.IsNotExist(err) {
				return removed, fmt.Errorf("remove rootfs ready flag %s: %w", readyFlag, err)
			}
			removed = append(removed, readyFlag)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return removed, fmt.Errorf("stat rootfs ready flag %s: %w", readyFlag, err)
	}
	return removed, nil
}

func hasActiveBoxliteBoxes(dbPath string) (bool, error) {
	if strings.TrimSpace(dbPath) == "" {
		return false, nil
	}
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat boxlite db %s: %w", dbPath, err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return false, fmt.Errorf("open boxlite db %s: %w", dbPath, err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)

	var count int
	err = db.QueryRow(`SELECT COUNT(1) FROM box_state WHERE LOWER(TRIM(status)) NOT IN ('', 'stopped', 'exited', 'dead', 'removed')`).Scan(&count)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return false, nil
		}
		return false, fmt.Errorf("query active boxlite boxes: %w", err)
	}
	return count > 0, nil
}

func cleanupLegacyBoxliteImageCaches(boxliteHome string) ([]string, error) {
	roots := []string{
		filepath.Join(boxliteHome, "images", "local"),
		filepath.Join(boxliteHome, "images", "disk-images"),
	}
	removed := make([]string, 0)
	for _, root := range roots {
		deleted, err := removeAllChildren(root)
		if err != nil {
			return removed, err
		}
		removed = append(removed, deleted...)
	}
	return removed, nil
}

func removeAllChildren(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read cache root %s: %w", root, err)
	}
	removed := make([]string, 0, len(entries))
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return removed, fmt.Errorf("remove cache entry %s: %w", path, err)
		}
		removed = append(removed, path)
	}
	return removed, nil
}
