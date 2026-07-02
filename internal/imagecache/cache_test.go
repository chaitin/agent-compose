package imagecache

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCachePathsAndEnsure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "images")
	cache, err := New(Config{Root: root})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	if cache.Root() != root {
		t.Fatalf("Root = %q, want %q", cache.Root(), root)
	}
	if cache.MetadataPath() != filepath.Join(root, metadataFileName) {
		t.Fatalf("MetadataPath = %q", cache.MetadataPath())
	}
	if cache.OCILayoutPath() != filepath.Join(root, ociLayoutDirName) {
		t.Fatalf("OCILayoutPath = %q", cache.OCILayoutPath())
	}
	if _, err := os.Stat(cache.OCILayoutPath()); err != nil {
		t.Fatalf("OCI layout dir was not created: %v", err)
	}
}

func TestMetadataLoadSaveRoundTrip(t *testing.T) {
	cache := newTestCache(t)
	initial, err := cache.LoadMetadata()
	if err != nil {
		t.Fatalf("LoadMetadata empty cache returned error: %v", err)
	}
	if initial.Version != metadataVersion || len(initial.Images) != 0 {
		t.Fatalf("empty metadata = %#v", initial)
	}

	wantPulledAt := time.Date(2026, 6, 11, 1, 2, 3, 0, time.UTC)
	metadata := MetadataFile{Images: []ImageMetadata{{
		CacheKey:       "sha256:abc",
		RequestedRef:   "busybox",
		NormalizedRef:  "index.docker.io/library/busybox:latest",
		RepoTags:       []string{"busybox:latest"},
		ManifestDigest: "sha256:def",
		ConfigDigest:   "sha256:abc",
		Platform:       Platform{OS: "linux", Architecture: "amd64"},
		Labels:         map[string]string{"name": "test"},
		PulledAt:       wantPulledAt,
	}}}
	if err := cache.SaveMetadata(metadata); err != nil {
		t.Fatalf("SaveMetadata returned error: %v", err)
	}
	got, err := cache.LoadMetadata()
	if err != nil {
		t.Fatalf("LoadMetadata returned error: %v", err)
	}
	if len(got.Images) != 1 || got.Images[0].PulledAt != wantPulledAt || got.Images[0].Labels["name"] != "test" {
		t.Fatalf("round-trip metadata = %#v", got)
	}
}

func TestLoadMetadataRejectsCorruptJSON(t *testing.T) {
	cache := newTestCache(t)
	if err := os.WriteFile(cache.MetadataPath(), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt metadata: %v", err)
	}
	_, err := cache.LoadMetadata()
	if err == nil {
		t.Fatal("LoadMetadata returned nil error for corrupt JSON")
	}
	if !IsKind(err, ErrorKindInternal) {
		t.Fatalf("LoadMetadata error kind = %q, want %q: %v", Kind(err), ErrorKindInternal, err)
	}
}

func TestCacheLockTempDirAndReadyFlag(t *testing.T) {
	cache := newTestCache(t)
	if err := cache.WithLock(func() error {
		tmp, err := cache.TempDir("busybox:latest")
		if err != nil {
			return err
		}
		if _, err := os.Stat(tmp); err != nil {
			return err
		}
		readyPath := filepath.Join(tmp, ".ready")
		if ReadyFlagExists(readyPath) {
			t.Fatalf("ready flag exists before write")
		}
		if err := WriteReadyFlag(readyPath); err != nil {
			return err
		}
		if !ReadyFlagExists(readyPath) {
			t.Fatalf("ready flag missing after write")
		}
		return nil
	}); err != nil {
		t.Fatalf("WithLock returned error: %v", err)
	}
}

func TestErrorKindSupportsErrorsIsAndAs(t *testing.T) {
	err := NewError(ErrorKindNotFound, "inspect", "missing:latest", os.ErrNotExist)
	if !errors.Is(err, &Error{Kind: ErrorKindNotFound}) {
		t.Fatalf("errors.Is did not match kind: %v", err)
	}
	var cacheErr *Error
	if !errors.As(err, &cacheErr) || cacheErr.Reference != "missing:latest" {
		t.Fatalf("errors.As = %#v", cacheErr)
	}
}

func TestParseReferenceUsesGoContainerRegistry(t *testing.T) {
	got, err := ParseReference("busybox")
	if err != nil {
		t.Fatalf("ParseReference returned error: %v", err)
	}
	if got != "index.docker.io/library/busybox:latest" {
		t.Fatalf("ParseReference = %q", got)
	}
}

func newTestCache(t *testing.T) *Cache {
	t.Helper()
	cache, err := New(Config{Root: filepath.Join(t.TempDir(), "images")})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	return cache
}
