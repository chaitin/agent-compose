//go:build cgo

package driver

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	typesimage "github.com/docker/docker/api/types/image"
)

func TestBuildOCIPlatformUsesInspectMetadata(t *testing.T) {
	platform := buildOCIPlatform(typesimage.InspectResponse{
		Architecture: "arm64",
		Variant:      "v8",
		Os:           "linux",
		OsVersion:    "6.9",
	})
	if platform == nil {
		t.Fatalf("buildOCIPlatform returned nil")
		return
	}
	if platform.Architecture != "arm64" {
		t.Fatalf("architecture = %q, want %q", platform.Architecture, "arm64")
	}
	if platform.Variant != "v8" {
		t.Fatalf("variant = %q, want %q", platform.Variant, "v8")
	}
	if platform.OS != "linux" {
		t.Fatalf("os = %q, want %q", platform.OS, "linux")
	}
	if platform.OSVersion != "6.9" {
		t.Fatalf("os version = %q, want %q", platform.OSVersion, "6.9")
	}
}

func TestBuildOCIPlatformFallsBackToDefaultLinuxAmd64(t *testing.T) {
	platform := buildOCIPlatform(typesimage.InspectResponse{})
	if platform == nil {
		t.Fatalf("buildOCIPlatform returned nil")
		return
	}
	if platform.Architecture != "amd64" {
		t.Fatalf("architecture = %q, want %q", platform.Architecture, "amd64")
	}
	if platform.OS != "linux" {
		t.Fatalf("os = %q, want %q", platform.OS, "linux")
	}
}

func TestNormalizeSavedOCILayoutAddsMissingPlatform(t *testing.T) {
	layoutDir := t.TempDir()
	index := ociIndex{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.index.v1+json",
		Manifests: []ociDescriptor{{
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Digest:    "sha256:deadbeef",
			Size:      123,
		}},
	}
	indexBytes, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(layoutDir, "oci-layout"), []byte("{\"imageLayoutVersion\":\"1.0.0\"}\n"), 0o644); err != nil {
		t.Fatalf("write oci-layout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(layoutDir, "index.json"), indexBytes, 0o644); err != nil {
		t.Fatalf("write index.json: %v", err)
	}

	inspect := typesimage.InspectResponse{Architecture: "arm64", Variant: "v8", Os: "linux", OsVersion: "6.9"}
	if err := normalizeSavedOCILayout(layoutDir, inspect, "agent-compose-guest:latest"); err != nil {
		t.Fatalf("normalizeSavedOCILayout: %v", err)
	}

	patchedBytes, err := os.ReadFile(filepath.Join(layoutDir, "index.json"))
	if err != nil {
		t.Fatalf("read patched index.json: %v", err)
	}
	var patched ociIndex
	if err := json.Unmarshal(patchedBytes, &patched); err != nil {
		t.Fatalf("decode patched index.json: %v", err)
	}
	if len(patched.Manifests) != 1 {
		t.Fatalf("manifest count = %d, want 1", len(patched.Manifests))
	}
	platform := patched.Manifests[0].Platform
	if platform == nil {
		t.Fatalf("platform = nil, want value")
		return
	}
	if platform.Architecture != "arm64" {
		t.Fatalf("architecture = %q, want %q", platform.Architecture, "arm64")
	}
	if platform.OS != "linux" {
		t.Fatalf("os = %q, want %q", platform.OS, "linux")
	}
	if platform.Variant != "v8" {
		t.Fatalf("variant = %q, want %q", platform.Variant, "v8")
	}
	if platform.OSVersion != "6.9" {
		t.Fatalf("os version = %q, want %q", platform.OSVersion, "6.9")
	}
	if got := patched.Manifests[0].Annotations["org.opencontainers.image.ref.name"]; got != "agent-compose-guest:latest" {
		t.Fatalf("ref annotation = %q, want %q", got, "agent-compose-guest:latest")
	}
}

func TestExtractOCILayoutRootfsAppliesLayers(t *testing.T) {
	layoutDir := t.TempDir()
	makeTestOCILayout(t, layoutDir, "agent-compose-guest:latest", []testLayerSpec{
		{
			entries: []testTarEntry{{name: "etc/config.txt", body: "base\n"}},
		},
		{
			entries: []testTarEntry{{name: "usr/bin/tool", body: "tool\n", mode: 0o755}},
		},
	})

	rootfsDir := filepath.Join(t.TempDir(), "rootfs")
	if err := os.MkdirAll(rootfsDir, 0o755); err != nil {
		t.Fatalf("mkdir rootfs: %v", err)
	}
	if err := extractOCILayoutRootfs(layoutDir, rootfsDir, "agent-compose-guest:latest"); err != nil {
		t.Fatalf("extractOCILayoutRootfs: %v", err)
	}

	configBytes, err := os.ReadFile(filepath.Join(rootfsDir, "etc", "config.txt"))
	if err != nil {
		t.Fatalf("read etc/config.txt: %v", err)
	}
	if got := string(configBytes); got != "base\n" {
		t.Fatalf("etc/config.txt = %q, want %q", got, "base\n")
	}

	toolPath := filepath.Join(rootfsDir, "usr", "bin", "tool")
	toolInfo, err := os.Stat(toolPath)
	if err != nil {
		t.Fatalf("stat usr/bin/tool: %v", err)
	}
	if toolInfo.Mode().Perm() != 0o755 {
		t.Fatalf("usr/bin/tool mode = %o, want %o", toolInfo.Mode().Perm(), 0o755)
	}
}

func TestExtractOCILayoutRootfsHonorsWhiteouts(t *testing.T) {
	layoutDir := t.TempDir()
	makeTestOCILayout(t, layoutDir, "agent-compose-guest:latest", []testLayerSpec{
		{
			entries: []testTarEntry{
				{name: "etc/remove-me.txt", body: "remove\n"},
				{name: "etc/keep.txt", body: "keep\n"},
				{name: "var/cache/obsolete.txt", body: "obsolete\n"},
				{name: "var/cache/stays.txt", body: "stays\n"},
			},
		},
		{
			entries: []testTarEntry{
				{name: "etc/.wh.remove-me.txt"},
				{name: "var/cache/.wh..wh..opq"},
				{name: "var/cache/fresh.txt", body: "fresh\n"},
			},
		},
	})

	rootfsDir := filepath.Join(t.TempDir(), "rootfs")
	if err := os.MkdirAll(rootfsDir, 0o755); err != nil {
		t.Fatalf("mkdir rootfs: %v", err)
	}
	if err := extractOCILayoutRootfs(layoutDir, rootfsDir, "agent-compose-guest:latest"); err != nil {
		t.Fatalf("extractOCILayoutRootfs: %v", err)
	}

	if _, err := os.Stat(filepath.Join(rootfsDir, "etc", "remove-me.txt")); !os.IsNotExist(err) {
		t.Fatalf("etc/remove-me.txt exists after whiteout, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(rootfsDir, "var", "cache", "obsolete.txt")); !os.IsNotExist(err) {
		t.Fatalf("var/cache/obsolete.txt exists after opaque whiteout, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(rootfsDir, "var", "cache", "stays.txt")); !os.IsNotExist(err) {
		t.Fatalf("var/cache/stays.txt exists after opaque whiteout, err=%v", err)
	}

	keepBytes, err := os.ReadFile(filepath.Join(rootfsDir, "etc", "keep.txt"))
	if err != nil {
		t.Fatalf("read etc/keep.txt: %v", err)
	}
	if got := string(keepBytes); got != "keep\n" {
		t.Fatalf("etc/keep.txt = %q, want %q", got, "keep\n")
	}
	freshBytes, err := os.ReadFile(filepath.Join(rootfsDir, "var", "cache", "fresh.txt"))
	if err != nil {
		t.Fatalf("read var/cache/fresh.txt: %v", err)
	}
	if got := string(freshBytes); got != "fresh\n" {
		t.Fatalf("var/cache/fresh.txt = %q, want %q", got, "fresh\n")
	}
}

type testLayerSpec struct {
	entries []testTarEntry
}

type testTarEntry struct {
	name string
	body string
	mode os.FileMode
}

func makeTestOCILayout(t *testing.T, layoutDir, imageRef string, layers []testLayerSpec) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(layoutDir, "blobs", "sha256"), 0o755); err != nil {
		t.Fatalf("mkdir blobs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(layoutDir, "oci-layout"), []byte("{\"imageLayoutVersion\":\"1.0.0\"}\n"), 0o644); err != nil {
		t.Fatalf("write oci-layout: %v", err)
	}

	manifest := ociManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config: ociDescriptor{
			MediaType: "application/vnd.oci.image.config.v1+json",
			Digest:    "sha256:config",
			Size:      2,
		},
	}
	for _, layer := range layers {
		data := makeLayerTar(t, layer.entries)
		desc, err := addOCIBlobFromBytes(data, filepath.Join(layoutDir, "blobs", "sha256"), "application/vnd.oci.image.layer.v1.tar")
		if err != nil {
			t.Fatalf("write layer blob: %v", err)
		}
		manifest.Layers = append(manifest.Layers, desc)
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	manifestDesc, err := addOCIBlobFromBytes(manifestBytes, filepath.Join(layoutDir, "blobs", "sha256"), manifest.MediaType)
	if err != nil {
		t.Fatalf("write manifest blob: %v", err)
	}

	index := ociIndex{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.index.v1+json",
		Manifests: []ociDescriptor{{
			MediaType:   manifest.MediaType,
			Digest:      manifestDesc.Digest,
			Size:        manifestDesc.Size,
			Annotations: map[string]string{"org.opencontainers.image.ref.name": imageRef},
		}},
	}
	indexBytes, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(layoutDir, "index.json"), indexBytes, 0o644); err != nil {
		t.Fatalf("write index.json: %v", err)
	}
}

func makeLayerTar(t *testing.T, entries []testTarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, entry := range entries {
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{Name: entry.name, Mode: int64(mode), Size: int64(len(entry.body))}
		if strings.HasSuffix(entry.name, "/") {
			header.Typeflag = tar.TypeDir
			header.Size = 0
		} else {
			header.Typeflag = tar.TypeReg
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("write header %s: %v", entry.name, err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(entry.body)); err != nil {
				t.Fatalf("write body %s: %v", entry.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	return buf.Bytes()
}
