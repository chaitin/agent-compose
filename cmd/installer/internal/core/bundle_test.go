package core

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyChecksum(t *testing.T) {
	data := []byte("bundle")
	sum := sha256.Sum256(data)
	checksums := []byte(fmt.Sprintf("%x  %s\n", sum, bundleAsset))
	if err := verifyChecksum(bundleAsset, data, checksums); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(bundleAsset, []byte("changed"), checksums); err == nil {
		t.Fatal("expected checksum mismatch")
	}
}

func TestExtractBundleSkipsUnknownAndTraversalMembers(t *testing.T) {
	archive := makeBundleArchive(t, map[string]string{
		"agent-compose-installer/docker-compose.yml":  "services: {}\n",
		"agent-compose-installer/.env.example":        "AUTH_PASSWORD=\nAUTH_SECRET=\n",
		"agent-compose-installer/images/manifest.env": "AGENT_COMPOSE_IMAGE=registry.example/agent-compose:v1\n",
		"agent-compose-installer/future-member.yml":   "future: true\n",
		"../outside": "unsafe\n",
	})
	destination := t.TempDir()
	if err := extractBundle(archive, destination); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(destination, "agent-compose-installer")
	for _, name := range []string{"docker-compose.yml", ".env.example", filepath.Join("images", "manifest.env")} {
		if !regularFile(filepath.Join(root, name)) {
			t.Fatalf("required member %s was not extracted", name)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "future-member.yml")); !os.IsNotExist(err) {
		t.Fatalf("unknown member was extracted: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(destination, "..", "outside")); !os.IsNotExist(err) {
		t.Fatalf("traversal member escaped the destination: %v", err)
	}
}

func TestExtractBundleRejectsNonRegularKnownMember(t *testing.T) {
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "agent-compose-installer/docker-compose.yml", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd", Mode: 0o777}); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractBundle(archive.Bytes(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "unexpected non-regular file") {
		t.Fatalf("extractBundle error = %v", err)
	}
}

func TestExtractBundleAcceptsLegacyReleaseMembers(t *testing.T) {
	archive := makeBundleArchive(t, map[string]string{
		"agent-compose-installer/docker-compose.yml":  "services: {}\n",
		"agent-compose-installer/.env.example":        "AUTH_PASSWORD=\nAUTH_SECRET=\n",
		"agent-compose-installer/images/manifest.env": "AGENT_COMPOSE_IMAGE=registry.example/agent-compose:v1\n",
		"agent-compose-installer/README.md":           "legacy release notes\n",
		"agent-compose-installer/install.sh":          "#!/bin/sh\n",
	})
	destination := t.TempDir()
	if err := extractBundle(archive, destination); err != nil {
		t.Fatal(err)
	}
	loaded, err := openBundle(filepath.Join(destination, "agent-compose-installer"), nil)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Close()
}

func TestOpenBundleRejectsUnknownPayloadVersion(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "docker-compose.yml"), "services: {}\n", 0o644)
	writeTestFile(t, filepath.Join(dir, ".env.example"), "AUTH_PASSWORD=\n", 0o644)
	writeTestFile(t, filepath.Join(dir, "images", "manifest.env"), "INSTALLER_PAYLOAD_VERSION=2\n", 0o644)
	if _, err := openBundle(dir, nil); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("openBundle error = %v", err)
	}
}

func TestBundleLoaderUsesReleaseBaseURL(t *testing.T) {
	archive := makeTestBundleArchive(t)
	sum := sha256.Sum256(archive)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/mirror/" + bundleAsset:
			_, _ = response.Write(archive)
		case "/mirror/SHASUMS256.txt":
			_, _ = fmt.Fprintf(response, "%x  %s\n", sum, bundleAsset)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	options := DefaultOptions()
	options.ReleaseBaseURL = server.URL + "/mirror"
	loaded, err := (bundleLoader{client: server.Client()}).Load(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if !regularFile(filepath.Join(loaded.Dir, "docker-compose.yml")) {
		t.Fatal("release bundle was not extracted")
	}
}

func makeTestBundleArchive(t *testing.T) []byte {
	t.Helper()
	return makeBundleArchive(t, map[string]string{
		"agent-compose-installer/docker-compose.yml":  "services: {}\n",
		"agent-compose-installer/.env.example":        "AUTH_PASSWORD=\nAUTH_SECRET=\n",
		"agent-compose-installer/images/manifest.env": "INSTALLER_PAYLOAD_VERSION=1\n",
	})
}

func makeBundleArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range files {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func writeTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
