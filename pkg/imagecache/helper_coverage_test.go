package imagecache

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

func TestImageCacheHelperCoverageWorkflows(t *testing.T) {
	cache, err := New(Config{
		Root:               filepath.Join(t.TempDir(), "images"),
		DefaultRegistry:    "registry.test",
		InsecureRegistries: []string{"https://registry.test/"},
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	ref, info, err := cache.parseRemoteReference("team/app:latest")
	if err != nil {
		t.Fatalf("parseRemoteReference returned error: %v", err)
	}
	if ref == nil || info.RequestedRef != "team/app:latest" || info.Repository == "" || info.Identifier != "latest" || info.IsDigest {
		t.Fatalf("reference/info = %#v/%#v", ref, info)
	}
	if _, _, err := cache.parseRemoteReference(""); err == nil {
		t.Fatalf("parseRemoteReference invalid ref returned nil error")
	}
	if !cache.isInsecureRegistry("http://registry.test") || cache.isInsecureRegistry("registry.other") {
		t.Fatalf("isInsecureRegistry returned unexpected values")
	}
	if options := cache.referenceOptions(true); len(options) < 3 {
		t.Fatalf("referenceOptions insecure = %#v", options)
	}

	platform := completePlatform(Platform{Variant: "v8"})
	if platform.OS == "" || platform.Architecture == "" || platform.Variant != "v8" {
		t.Fatalf("completePlatform = %#v", platform)
	}
	configPlatform := platformFromConfig(&v1.ConfigFile{
		OS:           "linux",
		Architecture: "arm64",
		Variant:      "v8",
		OSVersion:    "1",
		OSFeatures:   []string{"feature"},
	}, Platform{OS: "fallback", Architecture: "amd64", OSFeatures: []string{"fallback-feature"}, Features: []string{"cpu"}})
	if configPlatform.OS != "linux" || configPlatform.Architecture != "arm64" || len(configPlatform.OSFeatures) != 1 || len(configPlatform.Features) != 1 {
		t.Fatalf("platformFromConfig config = %#v", configPlatform)
	}
	if fallback := platformFromConfig(nil, Platform{OS: "linux", Architecture: "amd64"}); fallback.OS != "linux" {
		t.Fatalf("platformFromConfig nil = %#v", fallback)
	}
	if suffix := platformVariantSuffix(Platform{Variant: "v7"}); suffix != "/v7" {
		t.Fatalf("platformVariantSuffix = %q", suffix)
	}

	updates := make(chan v1.Update, 2)
	done := collectProgress(updates)
	updates <- v1.Update{Complete: 1, Total: 2}
	updates <- v1.Update{Error: errors.New("pull failed")}
	progress := finishProgress(updates, done)
	if len(progress) != 2 || progress[0].CurrentBytes != 1 || !strings.Contains(progress[1].Message, "pull failed") {
		t.Fatalf("progress = %#v", progress)
	}

	for _, tc := range []struct {
		name string
		err  error
		kind ErrorKind
	}{
		{name: "nil", err: nil, kind: ""},
		{name: "canceled", err: context.Canceled, kind: ErrorKindUnavailable},
		{name: "not found", err: &transport.Error{StatusCode: http.StatusNotFound}, kind: ErrorKindNotFound},
		{name: "bad request", err: &transport.Error{StatusCode: http.StatusBadRequest}, kind: ErrorKindInvalidReference},
		{name: "transport unavailable", err: &transport.Error{StatusCode: http.StatusInternalServerError}, kind: ErrorKindUnavailable},
		{name: "platform", err: errors.New("no matching manifest"), kind: ErrorKindNotFound},
		{name: "invalid", err: errors.New("invalid reference format"), kind: ErrorKindInvalidReference},
		{name: "other", err: errors.New("dial failed"), kind: ErrorKindUnavailable},
	} {
		mapped := mapPullError("pull", "team/app", Platform{OS: "linux", Architecture: "amd64", Variant: "v8"}, tc.err)
		if tc.err == nil {
			if mapped != nil {
				t.Fatalf("%s mapped error = %v, want nil", tc.name, mapped)
			}
			continue
		}
		if !IsKind(mapped, tc.kind) {
			t.Fatalf("%s mapped error = %v, want kind %s", tc.name, mapped, tc.kind)
		}
	}

	imageA := ImageMetadata{RequestedRef: "team/app:latest", NormalizedRef: "registry.test/team/app:latest", ConfigDigest: "sha256:a"}
	imageB := ImageMetadata{RequestedRef: "team/app:v2", NormalizedRef: "registry.test/team/app:v2", ConfigDigest: "sha256:b"}
	images := upsertMetadataImage([]ImageMetadata{imageA}, imageB)
	if len(images) != 2 {
		t.Fatalf("upsert append images = %#v", images)
	}
	imageA.ConfigDigest = "sha256:changed"
	images = upsertMetadataImage(images, imageA)
	if len(images) != 2 || images[0].ConfigDigest != "sha256:changed" {
		t.Fatalf("upsert replace images = %#v", images)
	}

	if isValidOCILayoutPath("") {
		t.Fatalf("empty OCI layout path returned valid")
	}
	if err := cache.ensureOCILayout(); err != nil {
		t.Fatalf("ensureOCILayout returned error: %v", err)
	}
	if _, err := layout.ImageIndexFromPath(cache.OCILayoutPath()); err != nil {
		t.Fatalf("ensureOCILayout did not create layout: %v", err)
	}
}

func TestImageCacheCopyDirHardlinkFirstCoverage(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	dst := filepath.Join(t.TempDir(), "dst")
	if err := os.MkdirAll(filepath.Join(src, "nested"), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "nested", "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.Symlink("nested/file.txt", filepath.Join(src, "link.txt")); err != nil {
		t.Fatalf("Symlink returned error: %v", err)
	}
	if err := copyDirHardlinkFirst(context.Background(), src, dst); err != nil {
		t.Fatalf("copyDirHardlinkFirst returned error: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dst, "nested", "file.txt")); err != nil || string(data) != "content" {
		t.Fatalf("copied file data=%q err=%v", string(data), err)
	}
	if link, err := os.Readlink(filepath.Join(dst, "link.txt")); err != nil || link != "nested/file.txt" {
		t.Fatalf("copied symlink=%q err=%v", link, err)
	}

	fileSrc := filepath.Join(t.TempDir(), "file-src")
	if err := os.WriteFile(fileSrc, []byte("file"), 0o644); err != nil {
		t.Fatalf("write file src: %v", err)
	}
	if err := copyDirHardlinkFirst(context.Background(), fileSrc, filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Fatalf("copyDirHardlinkFirst file source returned nil error")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copyDirHardlinkFirst(canceled, src, filepath.Join(t.TempDir(), "dst-canceled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("copyDirHardlinkFirst canceled err=%v", err)
	}
	if err := linkOrCopyFile(filepath.Join(src, "nested", "file.txt"), filepath.Join(t.TempDir(), "deep", "file.txt"), 0o644); err != nil {
		t.Fatalf("linkOrCopyFile returned error: %v", err)
	}
	if err := linkOrCopyFile(filepath.Join(src, "missing.txt"), filepath.Join(t.TempDir(), "missing", "file.txt"), 0o644); err == nil {
		t.Fatalf("linkOrCopyFile missing source returned nil error")
	}
	if got := firstImageRef([]string{"", "repo@sha256:abc"}, "fallback"); got != "repo@sha256:abc" {
		t.Fatalf("firstImageRef repo digest = %q", got)
	}
	if got := firstImageRef(nil, "", "fallback"); got != "fallback" {
		t.Fatalf("firstImageRef fallback = %q", got)
	}
}
