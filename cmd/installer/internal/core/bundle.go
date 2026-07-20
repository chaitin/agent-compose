package core

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const bundleAsset = "agent-compose-installer.tar.gz"

type bundle struct {
	Dir      string
	Manifest *envFile
	cleanup  func()
}

func (b *bundle) Close() {
	if b != nil && b.cleanup != nil {
		b.cleanup()
	}
}

type bundleLoader struct {
	client *http.Client
}

func (l bundleLoader) Load(ctx context.Context, options Options) (*bundle, error) {
	if options.BundleDir != "" {
		return openBundle(options.BundleDir, nil)
	}
	client := l.client
	if client == nil {
		client = http.DefaultClient
	}
	base := strings.TrimSpace(options.ReleaseBaseURL)
	if base != "" {
		base = strings.TrimRight(base, "/") + "/"
	} else {
		base = "https://github.com/" + options.Repository + "/releases/"
		if options.Version == DefaultVersion {
			base += "latest/download/"
		} else {
			base += "download/" + options.Version + "/"
		}
	}
	archive, err := download(ctx, client, base+bundleAsset)
	if err != nil {
		return nil, fmt.Errorf("download deployment bundle: %w", err)
	}
	checksums, err := download(ctx, client, base+"SHASUMS256.txt")
	if err != nil {
		return nil, fmt.Errorf("download deployment checksums: %w", err)
	}
	if err := verifyChecksum(bundleAsset, archive, checksums); err != nil {
		return nil, err
	}
	tempDir, err := os.MkdirTemp("", "agent-compose-installer-bundle-*")
	if err != nil {
		return nil, fmt.Errorf("create bundle directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tempDir) }
	if err := extractBundle(archive, tempDir); err != nil {
		cleanup()
		return nil, err
	}
	root := filepath.Join(tempDir, "agent-compose-installer")
	return openBundle(root, cleanup)
}

func download(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_ = response.Body.Close()
		return nil, fmt.Errorf("GET %s returned %s", url, response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	closeErr := response.Body.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return nil, err
	}
	return data, nil
}

func verifyChecksum(name string, data, checksumFile []byte) error {
	want := ""
	for _, line := range strings.Split(string(checksumFile), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "./") != name {
			continue
		}
		want = fields[0]
	}
	if len(want) != sha256.Size*2 {
		return fmt.Errorf("checksums do not contain a valid entry for %s", name)
	}
	if _, err := hex.DecodeString(want); err != nil {
		return fmt.Errorf("invalid checksum for %s: %w", name, err)
	}
	got := fmt.Sprintf("%x", sha256.Sum256(data))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum verification failed for %s", name)
	}
	return nil
}

func extractBundle(data []byte, destination string) error {
	gzipReader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("open deployment bundle: %w", err)
	}
	defer func() { _ = gzipReader.Close() }()
	tarReader := tar.NewReader(gzipReader)
	allowed := map[string]os.FileMode{
		"agent-compose-installer/docker-compose.yml":     0o644,
		"agent-compose-installer/docker-compose.kvm.yml": 0o644,
		"agent-compose-installer/.env.example":           0o644,
		"agent-compose-installer/images/manifest.env":    0o644,
	}
	seen := map[string]bool{}
	for {
		header, nextErr := tarReader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return fmt.Errorf("read deployment bundle: %w", nextErr)
		}
		name := filepath.ToSlash(filepath.Clean(header.Name))
		if header.Typeflag == tar.TypeDir {
			if name != "agent-compose-installer" && name != "agent-compose-installer/images" {
				return fmt.Errorf("unexpected directory in deployment bundle: %s", name)
			}
			continue
		}
		mode, ok := allowed[name]
		if !ok || header.Typeflag != tar.TypeReg {
			return fmt.Errorf("unexpected file in deployment bundle: %s", name)
		}
		if seen[name] {
			return fmt.Errorf("duplicate file in deployment bundle: %s", name)
		}
		seen[name] = true
		target := filepath.Join(destination, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(file, io.LimitReader(tarReader, 8<<20))
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	for _, required := range []string{
		"agent-compose-installer/docker-compose.yml",
		"agent-compose-installer/.env.example",
		"agent-compose-installer/images/manifest.env",
	} {
		if !seen[required] {
			return fmt.Errorf("deployment bundle is missing %s", required)
		}
	}
	return nil
}

func openBundle(dir string, cleanup func()) (*bundle, error) {
	for _, name := range []string{"docker-compose.yml", ".env.example", "images/manifest.env"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("deployment bundle is missing regular file %s", name)
		}
	}
	manifestData, err := os.ReadFile(filepath.Join(dir, "images/manifest.env"))
	if err != nil {
		return nil, fmt.Errorf("read deployment manifest: %w", err)
	}
	manifest := parseEnvFile(manifestData)
	if version, ok := manifest.Get("INSTALLER_PAYLOAD_VERSION"); ok && version != "1" {
		return nil, fmt.Errorf("unsupported installer payload version %q", version)
	}
	return &bundle{Dir: dir, Manifest: manifest, cleanup: cleanup}, nil
}
