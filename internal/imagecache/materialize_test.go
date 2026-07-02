package imagecache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func TestMaterializeOCILayoutCopiesValidLayoutAndReadyFlag(t *testing.T) {
	cache := newTestCache(t)
	image := writeMaterializeTestImage(t, cache, "team/app:latest")

	result, err := cache.MaterializeOCILayout(context.Background(), "team/app:latest")
	if err != nil {
		t.Fatalf("MaterializeOCILayout returned error: %v", err)
	}
	if result.ImageID != image.ConfigDigest || result.ResolvedRef != image.RepoDigests[0] {
		t.Fatalf("result = %#v, image = %#v", result, image)
	}
	if result.LayoutPath != cache.MaterializedOCILayoutPath(image.ConfigDigest) {
		t.Fatalf("LayoutPath = %q, want %q", result.LayoutPath, cache.MaterializedOCILayoutPath(image.ConfigDigest))
	}
	if !ReadyFlagExists(filepath.Join(cache.MaterializedImageDir(image.ConfigDigest), ociLayoutReadyFileName)) {
		t.Fatalf("ready flag was not written")
	}
	index, err := layout.ImageIndexFromPath(result.LayoutPath)
	if err != nil {
		t.Fatalf("ImageIndexFromPath returned error: %v", err)
	}
	indexManifest, err := index.IndexManifest()
	if err != nil {
		t.Fatalf("IndexManifest returned error: %v", err)
	}
	if len(indexManifest.Manifests) != 1 {
		t.Fatalf("manifests = %#v", indexManifest.Manifests)
	}
	for _, name := range []string{"oci-layout", "index.json", "blobs"} {
		if _, err := os.Stat(filepath.Join(result.LayoutPath, name)); err != nil {
			t.Fatalf("materialized layout missing %s: %v", name, err)
		}
	}
}

func TestMaterializeOCILayoutReadyCacheHitDoesNotOverwrite(t *testing.T) {
	cache := newTestCache(t)
	image := writeMaterializeTestImage(t, cache, "team/app:latest")

	first, err := cache.MaterializeOCILayout(context.Background(), image.ConfigDigest)
	if err != nil {
		t.Fatalf("first MaterializeOCILayout returned error: %v", err)
	}
	sentinelPath := filepath.Join(first.LayoutPath, "sentinel")
	if err := os.WriteFile(sentinelPath, []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	second, err := cache.MaterializeOCILayout(context.Background(), "team/app:latest")
	if err != nil {
		t.Fatalf("second MaterializeOCILayout returned error: %v", err)
	}
	if second != first {
		t.Fatalf("second result = %#v, want %#v", second, first)
	}
	if _, err := os.Stat(sentinelPath); err != nil {
		t.Fatalf("ready cache hit overwrote layout directory: %v", err)
	}
}

func TestMaterializeOCILayoutReturnsNotFound(t *testing.T) {
	cache := newTestCache(t)
	_, err := cache.MaterializeOCILayout(context.Background(), "missing:latest")
	if err == nil {
		t.Fatal("MaterializeOCILayout returned nil error, want not found")
	}
	if !errors.Is(err, &Error{Kind: ErrorKindNotFound}) {
		t.Fatalf("MaterializeOCILayout error = %v, want not found", err)
	}
}

func writeMaterializeTestImage(t *testing.T, cache *Cache, requestedRef string) ImageMetadata {
	t.Helper()
	img, err := random.Image(1024, 2)
	if err != nil {
		t.Fatalf("random.Image returned error: %v", err)
	}
	configFile, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("ConfigFile returned error: %v", err)
	}
	createdAt := time.Date(2026, 6, 11, 10, 11, 12, 0, time.UTC)
	configFile.OS = "linux"
	configFile.Architecture = "amd64"
	configFile.Created = v1.Time{Time: createdAt}
	configFile.Config.Labels = map[string]string{"name": "materialize"}
	img, err = mutate.ConfigFile(img, configFile)
	if err != nil {
		t.Fatalf("mutate.ConfigFile returned error: %v", err)
	}
	img = mutate.MediaType(img, types.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, types.OCIConfigJSON)

	if _, err := layout.Write(cache.OCILayoutPath(), empty.Index); err != nil {
		t.Fatalf("layout.Write returned error: %v", err)
	}
	if err := layout.Path(cache.OCILayoutPath()).AppendImage(img, layout.WithPlatform(v1.Platform{OS: "linux", Architecture: "amd64"})); err != nil {
		t.Fatalf("AppendImage returned error: %v", err)
	}
	manifestDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("Digest returned error: %v", err)
	}
	configDigest, err := img.ConfigName()
	if err != nil {
		t.Fatalf("ConfigName returned error: %v", err)
	}
	size, err := img.Size()
	if err != nil {
		t.Fatalf("Size returned error: %v", err)
	}
	metadata, err := NewImageMetadata(MetadataInput{
		RequestedRef:    requestedRef,
		ManifestDigest:  manifestDigest.String(),
		ConfigDigest:    configDigest.String(),
		Platform:        Platform{OS: "linux", Architecture: "amd64"},
		MediaType:       string(types.OCIManifestSchema1),
		Labels:          configFile.Config.Labels,
		SizeBytes:       size,
		CreatedAt:       createdAt,
		LayoutCachePath: cache.OCILayoutPath(),
	})
	if err != nil {
		t.Fatalf("NewImageMetadata returned error: %v", err)
	}
	if err := cache.SaveMetadata(MetadataFile{Images: []ImageMetadata{metadata}}); err != nil {
		t.Fatalf("SaveMetadata returned error: %v", err)
	}
	return metadata
}
