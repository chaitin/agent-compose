package imagecache

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestListFiltersMetadataByQuery(t *testing.T) {
	cache := newCacheWithImages(t, sampleImages())
	result, err := cache.List(context.Background(), ListRequest{Query: "app"})
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(result.Images) != 2 {
		t.Fatalf("List returned %d images, want 2: %#v", len(result.Images), result.Images)
	}

	result, err = cache.List(context.Background(), ListRequest{Query: "sha256:manifest-db"})
	if err != nil {
		t.Fatalf("List by digest returned error: %v", err)
	}
	if len(result.Images) != 1 || result.Images[0].RequestedRef != "registry.example/db:1.0" {
		t.Fatalf("List by digest = %#v", result.Images)
	}
}

func TestInspectFindsMetadataByRefsAndDigests(t *testing.T) {
	cache := newCacheWithImages(t, sampleImages())
	for _, query := range []string{
		"app",
		"registry.example/library/app:latest",
		"registry.example/library/app@sha256:manifest-app",
		"sha256:manifest-app",
		"sha256:config-app",
		"config-app",
	} {
		result, err := cache.Inspect(context.Background(), InspectRequest{Reference: query})
		if err != nil {
			t.Fatalf("Inspect(%q) returned error: %v", query, err)
		}
		if result.Image.ManifestDigest != "sha256:manifest-app" {
			t.Fatalf("Inspect(%q) = %#v", query, result.Image)
		}
	}
}

func TestInspectReturnsNotFoundError(t *testing.T) {
	cache := newCacheWithImages(t, sampleImages())
	_, err := cache.Inspect(context.Background(), InspectRequest{Reference: "missing:latest"})
	if err == nil {
		t.Fatal("Inspect returned nil error, want not found")
	}
	if !errors.Is(err, &Error{Kind: ErrorKindNotFound}) {
		t.Fatalf("Inspect error = %v, want not found", err)
	}
}

func TestRemoveDeletesSingleMetadataReference(t *testing.T) {
	cache := newCacheWithImages(t, []ImageMetadata{sampleImages()[2]})
	result, err := cache.Remove(context.Background(), RemoveRequest{Reference: "registry.example/db:1.0", PruneChildren: true})
	if err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}
	if len(result.UntaggedRefs) != 1 || result.UntaggedRefs[0] != "registry.example/db:1.0" {
		t.Fatalf("UntaggedRefs = %#v", result.UntaggedRefs)
	}
	if len(result.DeletedIDs) != 1 || result.DeletedIDs[0] != "sha256:config-db" {
		t.Fatalf("DeletedIDs = %#v", result.DeletedIDs)
	}
	if len(result.Warnings) != 2 {
		t.Fatalf("Warnings = %#v, want blob cleanup and prune warnings", result.Warnings)
	}
	list, err := cache.List(context.Background(), ListRequest{})
	if err != nil {
		t.Fatalf("List after remove returned error: %v", err)
	}
	if len(list.Images) != 0 {
		t.Fatalf("metadata still has images: %#v", list.Images)
	}
}

func TestRemoveConflictsWhenMultipleRefsShareImage(t *testing.T) {
	cache := newCacheWithImages(t, sampleImages())
	_, err := cache.Remove(context.Background(), RemoveRequest{Reference: "app"})
	if err == nil {
		t.Fatal("Remove returned nil error, want conflict")
	}
	if !errors.Is(err, &Error{Kind: ErrorKindConflict}) {
		t.Fatalf("Remove error = %v, want conflict", err)
	}
	list, err := cache.List(context.Background(), ListRequest{Query: "manifest-app"})
	if err != nil {
		t.Fatalf("List after conflict returned error: %v", err)
	}
	if len(list.Images) != 2 {
		t.Fatalf("conflicting remove changed metadata: %#v", list.Images)
	}
}

func TestRemoveForceDeletesAllRefsSharingImage(t *testing.T) {
	cache := newCacheWithImages(t, sampleImages())
	result, err := cache.Remove(context.Background(), RemoveRequest{Reference: "app", Force: true})
	if err != nil {
		t.Fatalf("Remove force returned error: %v", err)
	}
	if len(result.UntaggedRefs) != 2 {
		t.Fatalf("UntaggedRefs = %#v, want 2 refs", result.UntaggedRefs)
	}
	if len(result.DeletedIDs) != 1 || result.DeletedIDs[0] != "sha256:config-app" {
		t.Fatalf("DeletedIDs = %#v", result.DeletedIDs)
	}
	list, err := cache.List(context.Background(), ListRequest{})
	if err != nil {
		t.Fatalf("List after force remove returned error: %v", err)
	}
	if len(list.Images) != 1 || list.Images[0].RequestedRef != "registry.example/db:1.0" {
		t.Fatalf("remaining images = %#v", list.Images)
	}
}

func newCacheWithImages(t *testing.T, images []ImageMetadata) *Cache {
	t.Helper()
	cache := newTestCache(t)
	if err := cache.SaveMetadata(MetadataFile{Images: images}); err != nil {
		t.Fatalf("SaveMetadata returned error: %v", err)
	}
	return cache
}

func sampleImages() []ImageMetadata {
	pulledAt := time.Date(2026, 6, 11, 2, 3, 4, 0, time.UTC)
	return []ImageMetadata{
		{
			CacheKey:       "sha256:config-app",
			RequestedRef:   "app",
			NormalizedRef:  "registry.example/library/app:latest",
			RepoTags:       []string{"registry.example/library/app:latest"},
			RepoDigests:    []string{"registry.example/library/app@sha256:manifest-app"},
			ManifestDigest: "sha256:manifest-app",
			ConfigDigest:   "sha256:config-app",
			Platform:       Platform{OS: "linux", Architecture: "amd64"},
			MediaType:      "application/vnd.oci.image.manifest.v1+json",
			Labels:         map[string]string{"role": "app"},
			SizeBytes:      12,
			PulledAt:       pulledAt,
		},
		{
			CacheKey:       "sha256:config-app",
			RequestedRef:   "registry.example/library/app:stable",
			NormalizedRef:  "registry.example/library/app:stable",
			RepoTags:       []string{"registry.example/library/app:stable"},
			RepoDigests:    []string{"registry.example/library/app@sha256:manifest-app"},
			ManifestDigest: "sha256:manifest-app",
			ConfigDigest:   "sha256:config-app",
			Platform:       Platform{OS: "linux", Architecture: "amd64"},
			PulledAt:       pulledAt,
		},
		{
			CacheKey:       "sha256:config-db",
			RequestedRef:   "registry.example/db:1.0",
			NormalizedRef:  "registry.example/db:1.0",
			RepoTags:       []string{"registry.example/db:1.0"},
			RepoDigests:    []string{"registry.example/db@sha256:manifest-db"},
			ManifestDigest: "sha256:manifest-db",
			ConfigDigest:   "sha256:config-db",
			Platform:       Platform{OS: "linux", Architecture: "amd64"},
			PulledAt:       pulledAt,
		},
	}
}
