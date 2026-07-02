package imagecache

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func TestIntegrationPullFromLocalRegistryWritesOCILayoutAndMetadata(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(registry.New())
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")
	refString := host + "/team/app:latest"

	createdAt := time.Date(2026, 6, 11, 8, 9, 10, 0, time.UTC)
	img := newRegistryTestImage(t, Platform{OS: "linux", Architecture: "amd64"}, createdAt, map[string]string{
		"org.opencontainers.image.title": "integration-app",
	})
	pushTestImage(t, ctx, refString, img)

	cache := newPullTestCache(t, host)
	result, err := cache.Pull(ctx, PullRequest{
		Reference: refString,
		Platform:  Platform{OS: "linux", Architecture: "amd64"},
	})
	if err != nil {
		t.Fatalf("Pull returned error: %v", err)
	}

	if result.Image.RequestedRef != refString || result.Image.NormalizedRef != refString {
		t.Fatalf("result refs = %#v", result.Image)
	}
	if result.Image.ManifestDigest == "" || result.Image.ConfigDigest == "" || result.ResolvedRef == "" {
		t.Fatalf("result digests = %#v, resolved=%q", result.Image, result.ResolvedRef)
	}
	if result.Image.Platform.OS != "linux" || result.Image.Platform.Architecture != "amd64" {
		t.Fatalf("platform = %#v", result.Image.Platform)
	}
	if result.Image.MediaType != string(types.OCIManifestSchema1) {
		t.Fatalf("media type = %q", result.Image.MediaType)
	}
	if result.Image.Labels["org.opencontainers.image.title"] != "integration-app" {
		t.Fatalf("labels = %#v", result.Image.Labels)
	}
	if !result.Image.CreatedAt.Equal(createdAt) || result.Image.SizeBytes <= 0 {
		t.Fatalf("metadata times/size = %#v", result.Image)
	}
	if len(result.Progress) == 0 {
		t.Fatalf("progress was empty")
	}

	for _, path := range []string{
		filepath.Join(cache.OCILayoutPath(), "oci-layout"),
		filepath.Join(cache.OCILayoutPath(), "index.json"),
		filepath.Join(cache.OCILayoutPath(), "blobs"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected OCI layout path %s: %v", path, err)
		}
	}
	index, err := layout.ImageIndexFromPath(cache.OCILayoutPath())
	if err != nil {
		t.Fatalf("ImageIndexFromPath returned error: %v", err)
	}
	indexManifest, err := index.IndexManifest()
	if err != nil {
		t.Fatalf("IndexManifest returned error: %v", err)
	}
	if len(indexManifest.Manifests) != 1 {
		t.Fatalf("layout manifests = %#v", indexManifest.Manifests)
	}

	inspect, err := cache.Inspect(ctx, InspectRequest{Reference: refString})
	if err != nil {
		t.Fatalf("Inspect by ref returned error: %v", err)
	}
	if inspect.Image.ManifestDigest != result.Image.ManifestDigest {
		t.Fatalf("inspect image = %#v, want manifest %q", inspect.Image, result.Image.ManifestDigest)
	}
	inspect, err = cache.Inspect(ctx, InspectRequest{Reference: result.Image.ConfigDigest})
	if err != nil {
		t.Fatalf("Inspect by digest returned error: %v", err)
	}
	if inspect.Image.ConfigDigest != result.Image.ConfigDigest {
		t.Fatalf("inspect by digest = %#v", inspect.Image)
	}
	list, err := cache.List(ctx, ListRequest{Query: "team/app"})
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(list.Images) != 1 {
		t.Fatalf("list images = %#v", list.Images)
	}
}

func TestIntegrationPullMissingPlatformDoesNotUpdateMetadata(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(registry.New())
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")
	refString := host + "/team/platform-only:latest"

	armImage := newRegistryTestImage(t, Platform{OS: "linux", Architecture: "arm64"}, time.Now().UTC(), nil)
	index := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{
		Add: armImage,
		Descriptor: v1.Descriptor{
			Platform: &v1.Platform{OS: "linux", Architecture: "arm64"},
		},
	})
	ref, err := name.ParseReference(refString, name.Insecure)
	if err != nil {
		t.Fatalf("ParseReference returned error: %v", err)
	}
	if err := remote.WriteIndex(ref, index, remote.WithContext(ctx)); err != nil {
		t.Fatalf("WriteIndex returned error: %v", err)
	}

	cache := newPullTestCache(t, host)
	_, err = cache.Pull(ctx, PullRequest{
		Reference: refString,
		Platform:  Platform{OS: "linux", Architecture: "amd64"},
	})
	if err == nil {
		t.Fatal("Pull returned nil error, want platform mismatch")
	}
	if !errors.Is(err, &Error{Kind: ErrorKindNotFound}) {
		t.Fatalf("Pull error = %v, want not found", err)
	}
	if !strings.Contains(err.Error(), refString) || !strings.Contains(err.Error(), "linux/amd64") {
		t.Fatalf("Pull error does not include ref and platform: %v", err)
	}
	metadata, err := cache.LoadMetadata()
	if err != nil {
		t.Fatalf("LoadMetadata returned error: %v", err)
	}
	if len(metadata.Images) != 0 {
		t.Fatalf("metadata was updated after failed pull: %#v", metadata.Images)
	}
}

func newPullTestCache(t *testing.T, insecureRegistry string) *Cache {
	t.Helper()
	cache, err := New(Config{
		Root:               filepath.Join(t.TempDir(), "images"),
		InsecureRegistries: []string{insecureRegistry},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	return cache
}

func newRegistryTestImage(t *testing.T, platform Platform, createdAt time.Time, labels map[string]string) v1.Image {
	t.Helper()
	img, err := random.Image(1024, 2)
	if err != nil {
		t.Fatalf("random.Image returned error: %v", err)
	}
	configFile, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("ConfigFile returned error: %v", err)
	}
	configFile.OS = platform.OS
	configFile.Architecture = platform.Architecture
	configFile.Variant = platform.Variant
	configFile.Created = v1.Time{Time: createdAt}
	configFile.Config.Labels = labels
	img, err = mutate.ConfigFile(img, configFile)
	if err != nil {
		t.Fatalf("mutate.ConfigFile returned error: %v", err)
	}
	img = mutate.MediaType(img, types.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, types.OCIConfigJSON)
	return img
}

func pushTestImage(t *testing.T, ctx context.Context, refString string, img v1.Image) {
	t.Helper()
	ref, err := name.ParseReference(refString, name.Insecure)
	if err != nil {
		t.Fatalf("ParseReference returned error: %v", err)
	}
	if err := remote.Write(ref, img, remote.WithContext(ctx)); err != nil {
		t.Fatalf("remote.Write returned error: %v", err)
	}
}
