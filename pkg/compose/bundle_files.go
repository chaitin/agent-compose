package compose

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type BundleFile struct {
	Path    string
	Content []byte
	SHA256  string
}

func CollectBundleFiles(manifest string, normalized *NormalizedProjectSpec) ([]BundleFile, string, error) {
	if normalized == nil {
		return nil, "", &ValidationError{Message: "normalized spec is required"}
	}
	absManifest, err := filepath.Abs(strings.TrimSpace(manifest))
	if err != nil {
		return nil, "", fmt.Errorf("resolve manifest %q: %w", manifest, err)
	}
	projectDir := filepath.Dir(absManifest)
	paths := []string{filepath.Base(absManifest)}
	for _, service := range normalized.Services {
		paths = appendBundlePath(paths, service.Entry)
		paths = appendBundlePath(paths, schemaFileReference(service.InputSchema))
		paths = appendBundlePath(paths, schemaFileReference(service.OutputSchema))
		paths = appendBundlePath(paths, schemaFileReference(service.ErrorSchema))
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	files := make([]BundleFile, 0, len(paths))
	hash := sha256.New()
	for _, relPath := range paths {
		clean, err := cleanBundleRelativePath(relPath)
		if err != nil {
			return nil, "", err
		}
		content, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(clean)))
		if err != nil {
			return nil, "", fmt.Errorf("read bundle file %s: %w", clean, err)
		}
		sum := sha256.Sum256(content)
		hash.Write([]byte(clean))
		hash.Write([]byte{0})
		hash.Write([]byte(hex.EncodeToString(sum[:])))
		hash.Write([]byte{0})
		files = append(files, BundleFile{
			Path:    clean,
			Content: content,
			SHA256:  hex.EncodeToString(sum[:]),
		})
	}
	return files, "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func appendBundlePath(paths []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[") {
		return paths
	}
	return append(paths, value)
}

func schemaFileReference(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[") {
		return ""
	}
	return value
}

func cleanBundleRelativePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("bundle file path is required")
	}
	if filepath.IsAbs(value) {
		return "", fmt.Errorf("bundle file path %q must be relative", value)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("bundle file path %q escapes project directory", value)
	}
	return clean, nil
}
