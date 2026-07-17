package imagecache

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func TestMaterializeRootFSMergesLayersAndWhiteouts(t *testing.T) {
	cache := newTestCache(t)
	image := writeRootFSTestImage(t, cache, "team/app:latest",
		tarLayer(t,
			dirEntry("etc"),
			fileEntry("etc/keep", "old\n"),
			fileEntry("etc/remove", "remove\n"),
			dirEntry("etc/opaque"),
			fileEntry("etc/opaque/old", "old\n"),
		),
		tarLayer(t,
			fileEntry("etc/keep", "new\n"),
			fileEntry("etc/.wh.remove", ""),
			fileEntry("etc/opaque/.wh..wh..opq", ""),
			fileEntry("etc/opaque/new", "new\n"),
		),
	)

	result, err := cache.MaterializeRootFS(context.Background(), "team/app:latest")
	if err != nil {
		t.Fatalf("MaterializeRootFS returned error: %v", err)
	}
	if result.ImageID != image.ConfigDigest || result.RootFSPath != cache.MaterializedRootFSPath(image.ConfigDigest) {
		t.Fatalf("result = %#v, image = %#v", result, image)
	}
	if !ReadyFlagExists(filepath.Join(cache.MaterializedImageDir(image.ConfigDigest), rootFSReadyFileName)) {
		t.Fatalf("rootfs ready flag was not written")
	}
	assertFileContent(t, filepath.Join(result.RootFSPath, "etc/keep"), "new\n")
	assertFileMissing(t, filepath.Join(result.RootFSPath, "etc/remove"))
	assertFileMissing(t, filepath.Join(result.RootFSPath, "etc/opaque/old"))
	assertFileContent(t, filepath.Join(result.RootFSPath, "etc/opaque/new"), "new\n")

	metadata, err := cache.LoadMetadata()
	if err != nil {
		t.Fatalf("LoadMetadata returned error: %v", err)
	}
	if len(metadata.Images) != 1 || metadata.Images[0].RootFSCachePath != result.RootFSPath {
		t.Fatalf("metadata rootfs path was not updated: %#v", metadata.Images)
	}
}

func TestMaterializeRootFSHandlesSymlinkHardlinkAndReadyHit(t *testing.T) {
	cache := newTestCache(t)
	image := writeRootFSTestImage(t, cache, "team/app:latest",
		tarLayer(t,
			dirEntry("bin"),
			fileEntry("bin/tool", "tool\n"),
			symlinkEntry("bin/tool-link", "tool"),
			hardlinkEntry("bin/tool-hard", "bin/tool"),
		),
	)

	first, err := cache.MaterializeRootFS(context.Background(), image.ConfigDigest)
	if err != nil {
		t.Fatalf("first MaterializeRootFS returned error: %v", err)
	}
	linkTarget, err := os.Readlink(filepath.Join(first.RootFSPath, "bin/tool-link"))
	if err != nil {
		t.Fatalf("Readlink returned error: %v", err)
	}
	if linkTarget != "tool" {
		t.Fatalf("symlink target = %q", linkTarget)
	}
	assertSameInode(t, filepath.Join(first.RootFSPath, "bin/tool"), filepath.Join(first.RootFSPath, "bin/tool-hard"))
	sentinelPath := filepath.Join(first.RootFSPath, "sentinel")
	if err := os.WriteFile(sentinelPath, []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	second, err := cache.MaterializeRootFS(context.Background(), "team/app:latest")
	if err != nil {
		t.Fatalf("second MaterializeRootFS returned error: %v", err)
	}
	if second.ImageID != first.ImageID || second.ResolvedRef != first.ResolvedRef || second.LayoutPath != first.LayoutPath || second.RootFSPath != first.RootFSPath {
		t.Fatalf("second result = %#v, want %#v", second, first)
	}
	assertFileContent(t, sentinelPath, "keep\n")
}

func TestMaterializeRootFSRejectsPathEscape(t *testing.T) {
	cache := newTestCache(t)
	image := writeRootFSTestImage(t, cache, "team/app:latest",
		tarLayer(t, fileEntry("../escape", "bad\n")),
	)

	_, err := cache.MaterializeRootFS(context.Background(), "team/app:latest")
	if err == nil {
		t.Fatal("MaterializeRootFS returned nil error, want path escape failure")
	}
	if !errors.Is(err, &Error{Kind: ErrorKindInternal}) {
		t.Fatalf("MaterializeRootFS error = %v, want internal", err)
	}
	assertFileMissing(t, filepath.Join(filepath.Dir(cache.Root()), "escape"))
	if ReadyFlagExists(filepath.Join(cache.MaterializedImageDir(image.ConfigDigest), rootFSReadyFileName)) {
		t.Fatalf("ready flag was written after failed extraction")
	}
}

func TestMaterializeRootFSRejectsSymlinkParentEscape(t *testing.T) {
	cache := newTestCache(t)
	writeRootFSTestImage(t, cache, "team/app:latest",
		tarLayer(t, symlinkEntry("out", t.TempDir())),
		tarLayer(t, fileEntry("out/escape", "bad\n")),
	)

	_, err := cache.MaterializeRootFS(context.Background(), "team/app:latest")
	if err == nil {
		t.Fatal("MaterializeRootFS returned nil error, want symlink parent failure")
	}
	if !errors.Is(err, &Error{Kind: ErrorKindInternal}) {
		t.Fatalf("MaterializeRootFS error = %v, want internal", err)
	}
}

type tarEntry struct {
	name     string
	body     string
	typeflag byte
	linkname string
}

func fileEntry(name, body string) tarEntry {
	return tarEntry{name: name, body: body, typeflag: tar.TypeReg}
}

func dirEntry(name string) tarEntry {
	return tarEntry{name: name, typeflag: tar.TypeDir}
}

func symlinkEntry(name, linkname string) tarEntry {
	return tarEntry{name: name, typeflag: tar.TypeSymlink, linkname: linkname}
}

func hardlinkEntry(name, linkname string) tarEntry {
	return tarEntry{name: name, typeflag: tar.TypeLink, linkname: linkname}
}

func tarLayer(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	for _, entry := range entries {
		mode := int64(0o644)
		if entry.typeflag == tar.TypeDir {
			mode = 0o755
		}
		header := &tar.Header{
			Name:     entry.name,
			Mode:     mode,
			Size:     int64(len(entry.body)),
			Typeflag: entry.typeflag,
			Linkname: entry.linkname,
			ModTime:  time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC),
		}
		if entry.typeflag != tar.TypeReg {
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatalf("WriteHeader(%s) returned error: %v", entry.name, err)
		}
		if header.Size > 0 {
			if _, err := writer.Write([]byte(entry.body)); err != nil {
				t.Fatalf("Write(%s) returned error: %v", entry.name, err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close tar writer returned error: %v", err)
	}
	return buf.Bytes()
}

func writeRootFSTestImage(t *testing.T, cache *Cache, requestedRef string, layerBytes ...[]byte) ImageMetadata {
	t.Helper()
	layers := make([]v1.Layer, 0, len(layerBytes))
	for _, data := range layerBytes {
		layerBytes := append([]byte(nil), data...)
		layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(layerBytes)), nil
		})
		if err != nil {
			t.Fatalf("LayerFromOpener returned error: %v", err)
		}
		layers = append(layers, layer)
	}
	img, err := mutate.AppendLayers(empty.Image, layers...)
	if err != nil {
		t.Fatalf("AppendLayers returned error: %v", err)
	}
	configFile, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("ConfigFile returned error: %v", err)
	}
	createdAt := time.Date(2026, 6, 11, 12, 1, 2, 0, time.UTC)
	configFile.OS = "linux"
	configFile.Architecture = "amd64"
	configFile.Created = v1.Time{Time: createdAt}
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

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("ReadFile(%s) = %q, want %q", path, string(data), want)
	}
}

func assertFileMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		t.Fatalf("%s exists, want missing", path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("Lstat(%s) returned error: %v", path, err)
	}
}

func assertSameInode(t *testing.T, first, second string) {
	t.Helper()
	firstInfo, err := os.Stat(first)
	if err != nil {
		t.Fatalf("Stat(%s) returned error: %v", first, err)
	}
	secondInfo, err := os.Stat(second)
	if err != nil {
		t.Fatalf("Stat(%s) returned error: %v", second, err)
	}
	firstStat, ok := firstInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("Stat(%s) did not return syscall.Stat_t", first)
	}
	secondStat, ok := secondInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("Stat(%s) did not return syscall.Stat_t", second)
	}
	if firstStat.Ino != secondStat.Ino || firstStat.Dev != secondStat.Dev {
		t.Fatalf("%s and %s are not hardlinks to the same inode", first, second)
	}
}
