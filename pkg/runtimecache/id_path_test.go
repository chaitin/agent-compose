package runtimecache

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"agent-compose/pkg/identity"
)

var testCacheIDRE = regexp.MustCompile(`^[a-f0-9]{64}$`)

func TestGenerateCacheIDIsStableAndDistinct(t *testing.T) {
	item := Item{
		Domain:  DomainMaterializedImageCache,
		Driver:  DriverBoxLite,
		Kind:    "materialized-rootfs",
		Path:    filepath.Join(t.TempDir(), "image-cache", "image-a", "rootfs"),
		ImageID: "sha256:image-a",
	}

	first, err := GenerateCacheID(item)
	if err != nil {
		t.Fatalf("GenerateCacheID returned error: %v", err)
	}
	second, err := GenerateCacheID(item)
	if err != nil {
		t.Fatalf("GenerateCacheID second returned error: %v", err)
	}
	if first != second {
		t.Fatalf("GenerateCacheID not stable: %q != %q", first, second)
	}

	changed := item
	changed.Path = filepath.Join(filepath.Dir(item.Path), "other-rootfs")
	other, err := GenerateCacheID(changed)
	if err != nil {
		t.Fatalf("GenerateCacheID changed returned error: %v", err)
	}
	if other == first {
		t.Fatalf("GenerateCacheID did not distinguish identity: %q", other)
	}
	if !testCacheIDRE.MatchString(first) {
		t.Fatalf("GenerateCacheID = %q, want full sha256 id", first)
	}
	if got := ShortCacheID(first); got != first[:12] {
		t.Fatalf("ShortCacheID = %q", got)
	}

	parsed, err := ParseCacheID(first)
	if err != nil {
		t.Fatalf("ParseCacheID returned error: %v", err)
	}
	if parsed.ID != first || parsed.Hash != first {
		t.Fatalf("ParseCacheID = %#v", parsed)
	}
}

func TestGenerateCacheIDRejectsIncompleteOrInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		item Item
	}{
		{name: "missing domain", item: Item{Driver: DriverDocker, Kind: "oci-layout", Path: "/tmp/cache"}},
		{name: "missing kind", item: Item{Domain: DomainOCIImageStore, Driver: DriverDocker, Path: "/tmp/cache"}},
		{name: "invalid kind", item: Item{Domain: DomainOCIImageStore, Driver: DriverDocker, Kind: "../oci", Path: "/tmp/cache"}},
		{name: "missing identity", item: Item{Domain: DomainOCIImageStore, Driver: DriverDocker, Kind: "oci-layout"}},
		{name: "invalid driver", item: Item{Domain: DomainOCIImageStore, Driver: "podman", Kind: "oci-layout", Path: "/tmp/cache"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GenerateCacheID(tt.item)
			if !errors.Is(err, ErrInvalidCacheID) {
				t.Fatalf("GenerateCacheID error = %v, want ErrInvalidCacheID", err)
			}
		})
	}
}

func TestParseCacheIDRejectsInvalidIDs(t *testing.T) {
	tests := []string{
		"",
		"oci-image-store:docker:oci-layout:0123456789abcdef",
		"sha256:",
		"sha256:0123456789abcdef",
		"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeg",
		"../root",
	}

	for _, id := range tests {
		t.Run(id, func(t *testing.T) {
			_, err := ParseCacheID(id)
			if !errors.Is(err, ErrInvalidCacheID) {
				t.Fatalf("ParseCacheID(%q) error = %v, want ErrInvalidCacheID", id, err)
			}
		})
	}
}

func TestResolveCacheIDExactAndPrefix(t *testing.T) {
	first := mustCacheIDItemForResolve(t, "/tmp/runtime-cache-test/first")
	second := mustCacheIDItemForResolve(t, "/tmp/runtime-cache-test/second")
	items := []Item{first, second}

	resolved, err := ResolveCacheID(items, first.CacheID)
	if err != nil {
		t.Fatalf("ResolveCacheID exact returned error: %v", err)
	}
	if resolved != first.CacheID {
		t.Fatalf("ResolveCacheID exact = %q, want %q", resolved, first.CacheID)
	}

	legacyFirst := identity.Prefix + first.CacheID
	resolved, err = ResolveCacheID(items, legacyFirst)
	if err != nil {
		t.Fatalf("ResolveCacheID legacy exact returned error: %v", err)
	}
	if resolved != first.CacheID {
		t.Fatalf("ResolveCacheID legacy exact = %q, want %q", resolved, first.CacheID)
	}
	parsedLegacy, err := ParseCacheID(legacyFirst)
	if err != nil || parsedLegacy.ID != first.CacheID {
		t.Fatalf("ParseCacheID legacy = %#v, %v", parsedLegacy, err)
	}

	prefix := ShortCacheID(second.CacheID)
	resolved, err = ResolveCacheID(items, prefix)
	if err != nil {
		t.Fatalf("ResolveCacheID prefix returned error: %v", err)
	}
	if resolved != second.CacheID {
		t.Fatalf("ResolveCacheID prefix = %q, want %q", resolved, second.CacheID)
	}
}

func TestResolveCacheIDRejectsInvalidMissingAndAmbiguous(t *testing.T) {
	first := mustCacheIDItemForResolve(t, "/tmp/runtime-cache-test/first")
	second := first
	second.CacheID = first.CacheID[:12] + strings.Repeat("0", 52)

	if _, err := ResolveCacheID([]Item{first}, "../bad"); !errors.Is(err, ErrInvalidCacheID) {
		t.Fatalf("invalid error = %v, want ErrInvalidCacheID", err)
	}
	if _, err := ResolveCacheID([]Item{first}, strings.Repeat("f", 12)); !errors.Is(err, ErrCacheNotFound) {
		t.Fatalf("missing error = %v, want ErrCacheNotFound", err)
	}
	if _, err := ResolveCacheID([]Item{first, second}, ShortCacheID(first.CacheID)); !errors.Is(err, ErrAmbiguousCacheID) {
		t.Fatalf("ambiguous error = %v, want ErrAmbiguousCacheID", err)
	}
}

func mustCacheIDItemForResolve(t *testing.T, path string) Item {
	t.Helper()
	item := Item{
		Domain: DomainRuntimeDerivedCache,
		Driver: DriverBoxLite,
		Kind:   "boxlite-disk-image",
		Path:   path,
	}
	cacheID, err := GenerateCacheID(item)
	if err != nil {
		t.Fatalf("GenerateCacheID returned error: %v", err)
	}
	item.CacheID = cacheID
	return item
}

func TestValidateCachePathAcceptsTargetInsideRoot(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "cache", "item")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	safe, err := ValidateCachePath(root, target)
	if err != nil {
		t.Fatalf("ValidateCachePath returned error: %v", err)
	}
	if safe.CanonicalRoot == "" || safe.CanonicalTarget == "" || safe.CanonicalParent == "" {
		t.Fatalf("ValidateCachePath returned incomplete safe path: %#v", safe)
	}
}

func TestValidateCachePathRejectsUnsafeTargets(t *testing.T) {
	temp := t.TempDir()
	root := filepath.Join(temp, "root")
	outside := filepath.Join(temp, "outside")
	if err := os.MkdirAll(filepath.Join(root, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cache", "file"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "file"), []byte("no"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "file"), filepath.Join(root, "cache", "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "cache", "broken")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		target string
	}{
		{name: "path traversal", target: filepath.Join(root, "..", "outside", "file")},
		{name: "symlink escape", target: filepath.Join(root, "cache", "escape")},
		{name: "broken symlink", target: filepath.Join(root, "cache", "broken")},
		{name: "root deletion", target: root},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidateCachePath(root, tt.target)
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("ValidateCachePath error = %v, want ErrUnsafePath", err)
			}
		})
	}
}

func TestEstimateSizeAndWarnings(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(root, "sub")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "b"), []byte("123"), 0o644); err != nil {
		t.Fatal(err)
	}

	size, warnings := EstimateSize(root)
	if size != 8 {
		t.Fatalf("EstimateSize size = %d, want 8", size)
	}
	if len(warnings) != 0 {
		t.Fatalf("EstimateSize warnings = %#v", warnings)
	}

	size, warnings = EstimateSize(filepath.Join(root, "missing"))
	if size != 0 || len(warnings) == 0 {
		t.Fatalf("EstimateSize missing = %d, %#v; want warning", size, warnings)
	}
	if !strings.Contains(warnings[0], "size walk") {
		t.Fatalf("EstimateSize warning = %#v, want size walk warning", warnings)
	}
}

func TestAppendWarningsSkipsEmptyAndCopies(t *testing.T) {
	base := []string{"existing"}
	got := AppendWarnings(base, "", " next ", "final")
	if strings.Join(got, "|") != "existing|next|final" {
		t.Fatalf("AppendWarnings = %#v", got)
	}
	got[0] = "changed"
	if base[0] != "existing" {
		t.Fatalf("AppendWarnings modified input slice: %#v", base)
	}
}
