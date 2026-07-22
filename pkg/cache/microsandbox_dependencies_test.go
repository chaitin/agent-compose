package cache

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-compose/pkg/imagecache"
)

func TestMicrosandboxBaseDiskInventoryAndReferenceProtection(t *testing.T) {
	dataRoot := t.TempDir()
	imageCache, err := imagecache.New(imagecache.Config{Root: filepath.Join(dataRoot, "images")})
	if err != nil {
		t.Fatal(err)
	}
	baseDir := filepath.Join(imageCache.MaterializationRoot(), "image-id", "microsandbox")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	basePath := filepath.Join(baseDir, "base-v1-amd64-6.qcow2")
	if err := os.WriteFile(basePath, []byte("qcow2"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "base-v1-amd64-6.json"), []byte("{}\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dataRoot, "microsandbox")
	sidecarDir := filepath.Join(home, "rootfs-disks")
	if err := os.MkdirAll(sidecarDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sidecar := map[string]any{
		"version": 2, "resource_kind": "microsandbox-rootfs", "sandbox_id": "sandbox-a",
		"base_cache_identity": "base-v1-amd64-6", "backing_file_path": basePath,
	}
	data, err := json.Marshal(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sidecarDir, "sandbox-a.qcow2.owner.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	dependencies := MicrosandboxRootfsDependencies{Home: home, BaseRoot: imageCache.MaterializationRoot()}
	result, err := (MaterializedScanner{Cache: imageCache, Dependencies: dependencies}).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var base Item
	for _, item := range result.Items {
		if item.Path == basePath {
			base = item
			break
		}
	}
	if base.Kind != KindMicrosandboxBaseDisk || base.Status != StatusReferenced || len(base.References) != 1 {
		t.Fatalf("base item = %#v", base)
	}
	if err := (MaterializedRemover{Cache: imageCache, Dependencies: dependencies}).Remove(context.Background(), base); err == nil {
		t.Fatal("referenced base disk removal succeeded")
	}
	if err := os.Remove(filepath.Join(sidecarDir, "sandbox-a.qcow2.owner.json")); err != nil {
		t.Fatal(err)
	}
	base.LastUsedAt = time.Now().Add(-time.Hour)
	if err := (MaterializedRemover{Cache: imageCache, Dependencies: dependencies}).Remove(context.Background(), base); err != nil {
		t.Fatalf("remove unreferenced base disk: %v", err)
	}
	for _, path := range []string{basePath, filepath.Join(baseDir, "base-v1-amd64-6.json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("path %s remains after removal: %v", path, err)
		}
	}
}

func TestMicrosandboxCorruptRootfsOwnershipBlocksBaseRemoval(t *testing.T) {
	dataRoot := t.TempDir()
	imageCache, err := imagecache.New(imagecache.Config{Root: filepath.Join(dataRoot, "images")})
	if err != nil {
		t.Fatal(err)
	}
	baseDir := filepath.Join(imageCache.MaterializationRoot(), "image-id", "microsandbox")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	basePath := filepath.Join(baseDir, "base-v1-amd64-6.qcow2")
	manifestPath := strings.TrimSuffix(basePath, ".qcow2") + ".json"
	for _, path := range []string{basePath, manifestPath} {
		if err := os.WriteFile(path, []byte("corrupt\n"), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	home := filepath.Join(dataRoot, "microsandbox")
	sidecarDir := filepath.Join(home, "rootfs-disks")
	if err := os.MkdirAll(sidecarDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sidecarDir, "sandbox-a.qcow2.owner.json"), []byte("{not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dependencies := MicrosandboxRootfsDependencies{Home: home, BaseRoot: imageCache.MaterializationRoot()}
	listed, err := (MaterializedScanner{Cache: imageCache, Dependencies: dependencies}).List(context.Background())
	if err != nil {
		t.Fatalf("scan with corrupt sidecar failed instead of warning: %v", err)
	}
	if !warningsContain(listed.Warnings, "cannot safely identify its base disk") {
		t.Fatalf("scan warnings = %#v, want corrupt-sidecar warning", listed.Warnings)
	}
	var listedBase Item
	for _, item := range listed.Items {
		if item.Path == basePath {
			listedBase = item
			break
		}
	}
	if listedBase.Status != StatusUnknown {
		t.Fatalf("base status with corrupt sidecar = %q, want %q", listedBase.Status, StatusUnknown)
	}
	base := Item{
		Domain: DomainMaterializedImageCache, Driver: DriverAll, Kind: KindMicrosandboxBaseDisk,
		Path: basePath, Status: StatusOrphaned, LastUsedAt: time.Now(), LastUsedSource: LastUsedSourceMTime,
	}
	base.CacheID, err = GenerateCacheID(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := (MaterializedRemover{Cache: imageCache, Dependencies: dependencies}).Remove(context.Background(), base); err == nil || !strings.Contains(err.Error(), "cannot safely identify its base disk") {
		t.Fatalf("remove error = %v, want conservative corrupt-sidecar failure", err)
	}
	for _, path := range []string{basePath, manifestPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("protected base path %s was removed: %v", path, err)
		}
	}
}

func TestMicrosandboxRootfsDependenciesRejectBackingPathOutsideCache(t *testing.T) {
	dataRoot := t.TempDir()
	imageCache, err := imagecache.New(imagecache.Config{Root: filepath.Join(dataRoot, "images")})
	if err != nil {
		t.Fatal(err)
	}
	baseRoot := imageCache.MaterializationRoot()
	home := filepath.Join(dataRoot, "microsandbox")
	sidecarDir := filepath.Join(home, "rootfs-disks")
	if err := os.MkdirAll(sidecarDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, backing := range map[string]string{
		// Outside the materialization root entirely.
		"sandbox-outside": filepath.Join(dataRoot, "elsewhere", "base-v1-amd64-6.qcow2"),
		// Escapes the root by traversal.
		"sandbox-traversal": filepath.Join(baseRoot, "image-id", "microsandbox", "..", "..", "..", "base-v1-amd64-6.qcow2"),
		// Inside the root but not a base disk path.
		"sandbox-shape": filepath.Join(baseRoot, "image-id", "rootfs"),
		// Base disk path that contradicts the declared cache identity.
		"sandbox-identity": filepath.Join(baseRoot, "image-id", "microsandbox", "base-v1-other-6.qcow2"),
		// Relative paths are never usable as reference keys.
		"sandbox-relative": "image-id/microsandbox/base-v1-amd64-6.qcow2",
	} {
		sidecar := map[string]any{
			"version": 2, "resource_kind": "microsandbox-rootfs", "sandbox_id": name,
			"base_cache_identity": "base-v1-amd64-6", "backing_file_path": backing,
		}
		data, err := json.Marshal(sidecar)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sidecarDir, name+".qcow2.owner.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	dependencies, warnings, err := (MicrosandboxRootfsDependencies{Home: home, BaseRoot: baseRoot}).MaterializedDependencies(context.Background())
	if err != nil {
		t.Fatalf("untrusted sidecars failed the scan instead of warning: %v", err)
	}
	if len(warnings) != 5 {
		t.Fatalf("warnings = %#v, want one per untrusted sidecar", warnings)
	}
	for _, dependency := range dependencies {
		if !dependency.Unresolved {
			t.Fatalf("untrusted sidecar produced a usable dependency: %#v", dependency)
		}
		if dependency.Path != "" {
			t.Fatalf("untrusted sidecar pinned path %q", dependency.Path)
		}
	}
}

func warningsContain(warnings []string, substring string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, substring) {
			return true
		}
	}
	return false
}

func TestMicrosandboxRootfsDependenciesIgnoreEmptyHome(t *testing.T) {
	dependencies, warnings, err := (MicrosandboxRootfsDependencies{}).MaterializedDependencies(context.Background())
	if err != nil || len(dependencies) != 0 || len(warnings) != 0 {
		t.Fatalf("empty-home dependencies = %#v warnings=%#v err=%v", dependencies, warnings, err)
	}
}

func TestMicrosandboxRootfsDependenciesRejectSymlinkedRoot(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, "rootfs-disks")); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	_, _, err := (MicrosandboxRootfsDependencies{Home: home}).MaterializedDependencies(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not a regular directory") {
		t.Fatalf("symlinked root error = %v, want rejection", err)
	}
}

func TestMaterializedRemoverRechecksMicrosandboxReferencesAfterLock(t *testing.T) {
	dataRoot := t.TempDir()
	imageCache, err := imagecache.New(imagecache.Config{Root: filepath.Join(dataRoot, "images")})
	if err != nil {
		t.Fatal(err)
	}
	baseDir := filepath.Join(imageCache.MaterializationRoot(), "image-id", "microsandbox")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	basePath := filepath.Join(baseDir, "base-v1-amd64-6.qcow2")
	if err := os.WriteFile(basePath, []byte("qcow2"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strings.TrimSuffix(basePath, ".qcow2")+".json", []byte("{}\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dataRoot, "microsandbox")
	sidecarDir := filepath.Join(home, "rootfs-disks")
	if err := os.MkdirAll(sidecarDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dependencies := MicrosandboxRootfsDependencies{Home: home, BaseRoot: imageCache.MaterializationRoot()}
	listed, err := (MaterializedScanner{Cache: imageCache, Dependencies: dependencies}).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var base Item
	for _, item := range listed.Items {
		if item.Path == basePath {
			base = item
			break
		}
	}
	if base.Status != StatusOrphaned {
		t.Fatalf("pre-publication base status = %q, want %q", base.Status, StatusOrphaned)
	}

	unlock, err := imageCache.Lock()
	if err != nil {
		t.Fatal(err)
	}
	removeResult := make(chan error, 1)
	go func() {
		removeResult <- (MaterializedRemover{Cache: imageCache, Dependencies: dependencies}).Remove(context.Background(), base)
	}()
	sidecar := map[string]any{
		"version": 2, "resource_kind": "microsandbox-rootfs", "sandbox_id": "sandbox-racing",
		"base_cache_identity": "base-v1-amd64-6", "backing_file_path": basePath,
	}
	data, err := json.Marshal(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sidecarDir, "sandbox-racing.qcow2.owner.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if err := <-removeResult; err == nil || !strings.Contains(err.Error(), "referenced by 1 sandbox") {
		t.Fatalf("racing removal error = %v, want newly published reference protection", err)
	}
	if _, err := os.Stat(basePath); err != nil {
		t.Fatalf("racing removal deleted referenced base: %v", err)
	}
}
