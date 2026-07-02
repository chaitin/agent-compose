package imagecache

import (
	"context"
	"testing"
	"time"
)

const testManifestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testConfigDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestNormalizeReferenceDefaultsDockerStyleReference(t *testing.T) {
	info, err := NormalizeReference("busybox", "")
	if err != nil {
		t.Fatalf("NormalizeReference returned error: %v", err)
	}
	if info.RequestedRef != "busybox" {
		t.Fatalf("RequestedRef = %q, want busybox", info.RequestedRef)
	}
	if info.NormalizedRef != "index.docker.io/library/busybox:latest" {
		t.Fatalf("NormalizedRef = %q", info.NormalizedRef)
	}
	if info.Repository != "index.docker.io/library/busybox" || info.Identifier != "latest" || info.IsDigest {
		t.Fatalf("reference info = %#v", info)
	}
}

func TestNormalizeReferenceUsesConfiguredDefaultRegistry(t *testing.T) {
	info, err := NormalizeReference("team/app", "registry.example")
	if err != nil {
		t.Fatalf("NormalizeReference returned error: %v", err)
	}
	if info.NormalizedRef != "registry.example/team/app:latest" {
		t.Fatalf("NormalizedRef = %q", info.NormalizedRef)
	}
}

func TestNormalizeReferenceKeepsFullyQualifiedReference(t *testing.T) {
	info, err := NormalizeReference("registry.example/team/app:1.2.3", "mirror.example")
	if err != nil {
		t.Fatalf("NormalizeReference returned error: %v", err)
	}
	if info.NormalizedRef != "registry.example/team/app:1.2.3" {
		t.Fatalf("NormalizedRef = %q", info.NormalizedRef)
	}
}

func TestNormalizeReferenceParsesDigestReference(t *testing.T) {
	info, err := NormalizeReference("registry.example/team/app@"+testManifestDigest, "")
	if err != nil {
		t.Fatalf("NormalizeReference returned error: %v", err)
	}
	if !info.IsDigest || info.Identifier != testManifestDigest || info.NormalizedRef != "registry.example/team/app@"+testManifestDigest {
		t.Fatalf("reference info = %#v", info)
	}
}

func TestNewImageMetadataCompletesFieldsAndPreservesRequestedRef(t *testing.T) {
	createdAt := time.Date(2026, 6, 11, 3, 4, 5, 0, time.UTC)
	pulledAt := createdAt.Add(time.Minute)
	metadata, err := NewImageMetadata(MetadataInput{
		RequestedRef:    "team/app",
		ManifestDigest:  testManifestDigest,
		ConfigDigest:    testConfigDigest,
		Platform:        Platform{OS: "linux", Architecture: "amd64"},
		MediaType:       "application/vnd.oci.image.manifest.v1+json",
		Labels:          map[string]string{"name": "app"},
		SizeBytes:       123,
		CreatedAt:       createdAt,
		PulledAt:        pulledAt,
		DefaultRegistry: "registry.example",
		AdditionalTags:  []string{"registry.example/team/app:stable"},
	})
	if err != nil {
		t.Fatalf("NewImageMetadata returned error: %v", err)
	}
	if metadata.RequestedRef != "team/app" || metadata.NormalizedRef != "registry.example/team/app:latest" {
		t.Fatalf("metadata refs = %#v", metadata)
	}
	if metadata.CacheKey != testConfigDigest || metadata.ManifestDigest != testManifestDigest || metadata.ConfigDigest != testConfigDigest {
		t.Fatalf("metadata digests = %#v", metadata)
	}
	if len(metadata.RepoTags) != 2 || metadata.RepoTags[0] != "registry.example/team/app:latest" {
		t.Fatalf("RepoTags = %#v", metadata.RepoTags)
	}
	if len(metadata.RepoDigests) != 1 || metadata.RepoDigests[0] != "registry.example/team/app@"+testManifestDigest {
		t.Fatalf("RepoDigests = %#v", metadata.RepoDigests)
	}
	if metadata.Platform.Architecture != "amd64" || metadata.Labels["name"] != "app" || metadata.SizeBytes != 123 || metadata.CreatedAt != createdAt || metadata.PulledAt != pulledAt {
		t.Fatalf("metadata fields = %#v", metadata)
	}
}

func TestMetadataReloadKeepsLookupStableAndRequestedRef(t *testing.T) {
	cache := newTestCache(t)
	metadata, err := NewImageMetadata(MetadataInput{
		RequestedRef:    "team/app",
		ManifestDigest:  testManifestDigest,
		ConfigDigest:    testConfigDigest,
		DefaultRegistry: "registry.example",
	})
	if err != nil {
		t.Fatalf("NewImageMetadata returned error: %v", err)
	}
	if err := cache.SaveMetadata(MetadataFile{Images: []ImageMetadata{metadata}}); err != nil {
		t.Fatalf("SaveMetadata returned error: %v", err)
	}

	reloaded, err := cache.LoadMetadata()
	if err != nil {
		t.Fatalf("LoadMetadata returned error: %v", err)
	}
	if len(reloaded.Images) != 1 || reloaded.Images[0].RequestedRef != "team/app" {
		t.Fatalf("reloaded metadata = %#v", reloaded)
	}
	for _, query := range []string{"team/app", "registry.example/team/app:latest", testManifestDigest, testConfigDigest} {
		result, err := cache.Inspect(context.Background(), InspectRequest{Reference: query})
		if err != nil {
			t.Fatalf("Inspect(%q) returned error after reload: %v", query, err)
		}
		if result.Image.RequestedRef != "team/app" {
			t.Fatalf("Inspect(%q) requested ref = %q", query, result.Image.RequestedRef)
		}
	}
}
