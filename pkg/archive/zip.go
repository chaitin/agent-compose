package archive

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrPathEscapes classifies a relative path that leaves its root, so callers
// can report the failure in their own vocabulary.
var ErrPathEscapes = errors.New("path escapes root")

// ExtractPolicy bounds one archive expansion. MaxExpandedBytes is required;
// zero disables an optional guard.
type ExtractPolicy struct {
	// MaxExpandedBytes caps the total expanded size.
	MaxExpandedBytes int64
	// MaxEntries caps the number of archive entries, because a small archive
	// can still declare millions of them. 0 disables the cap.
	MaxEntries int
	// MaxCompressionRatio rejects an archive that expands more than this many
	// times its compressed size, once the expansion reaches
	// CompressionRatioFloorBytes. 0 disables the guard.
	MaxCompressionRatio int64
	// CompressionRatioFloorBytes keeps small, legitimately compressible
	// content (minified bundles, fixtures) from tripping the ratio guard. 0
	// applies the ratio to any expansion.
	CompressionRatioFloorBytes int64
}

// ExtractZip expands archivePath into destination, rejecting entries that
// escape the destination or are not plain files and directories. File modes
// keep their permission bits with group and other writes removed, so scripts
// stay executable without inheriting unsafe or special bits.
func ExtractZip(archivePath, destination string, policy ExtractPolicy) error {
	if policy.MaxExpandedBytes <= 0 {
		return fmt.Errorf("archive extract policy requires a positive expanded byte limit")
	}
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	if policy.MaxEntries > 0 && len(reader.File) > policy.MaxEntries {
		return fmt.Errorf("zip contains %d files, max %d", len(reader.File), policy.MaxEntries)
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return fmt.Errorf("create extraction root: %w", err)
	}
	// declared sizes are checked before reading anything, while the expanded
	// total tracks what the archive actually produced: a lying header must not
	// be able to bypass the limit.
	var declared uint64
	var expanded int64
	var compressed int64
	for _, file := range reader.File {
		declared += file.UncompressedSize64
		if declared > uint64(policy.MaxExpandedBytes) {
			return fmt.Errorf("zip expanded size exceeds %d bytes", policy.MaxExpandedBytes)
		}
		if file.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("zip symlink %s is not supported", file.Name)
		}
		relative, err := cleanRelative(file.Name)
		if err != nil {
			return fmt.Errorf("zip entry %q escapes destination", file.Name)
		}
		if relative == "." {
			continue
		}
		target := filepath.Join(destination, relative)
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		written, extractErr := extractEntry(file, target, expanded, policy.MaxExpandedBytes)
		expanded += written
		if extractErr != nil {
			return extractErr
		}
		compressed += int64(file.CompressedSize64)
		if err := checkCompressionRatio(policy, expanded, compressed); err != nil {
			return err
		}
	}
	return nil
}

// SafeJoinRelative resolves a relative path inside root, rejecting absolute
// paths and parent traversal, including paths that use Windows separators
// inside an archive. It returns root for an empty or current-directory path.
func SafeJoinRelative(root, relative string) (string, error) {
	clean, err := cleanRelative(strings.TrimSpace(relative))
	if err != nil {
		return "", err
	}
	if clean == "." {
		return filepath.Clean(root), nil
	}
	return filepath.Join(root, clean), nil
}

func cleanRelative(relative string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(relative, "\\", "/")))
	switch {
	case clean == "." || clean == "":
		return ".", nil
	case filepath.IsAbs(clean), clean == "..", strings.HasPrefix(clean, ".."+string(filepath.Separator)):
		return "", ErrPathEscapes
	}
	return clean, nil
}

func extractEntry(file *zip.File, target string, expanded, limit int64) (int64, error) {
	remaining := limit - expanded
	if remaining <= 0 {
		return 0, fmt.Errorf("zip expanded size exceeds %d bytes", limit)
	}
	src, err := file.Open()
	if err != nil {
		return 0, err
	}
	dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, sanitizeFileMode(file.Mode()))
	if err != nil {
		_ = src.Close()
		return 0, err
	}
	written, copyErr := io.Copy(dst, io.LimitReader(src, remaining+1))
	closeDstErr := dst.Close()
	closeSrcErr := src.Close()
	switch {
	case copyErr != nil:
		_ = os.Remove(target)
		return written, copyErr
	case closeDstErr != nil:
		_ = os.Remove(target)
		return written, closeDstErr
	case closeSrcErr != nil:
		_ = os.Remove(target)
		return written, closeSrcErr
	case written > remaining:
		_ = os.Remove(target)
		return written, fmt.Errorf("zip expanded size exceeds %d bytes", limit)
	}
	return written, nil
}

func checkCompressionRatio(policy ExtractPolicy, expanded, compressed int64) error {
	if policy.MaxCompressionRatio <= 0 || compressed <= 0 {
		return nil
	}
	if expanded < policy.CompressionRatioFloorBytes {
		return nil
	}
	if expanded > compressed*policy.MaxCompressionRatio {
		return fmt.Errorf("zip expands to %d bytes from %d compressed bytes, exceeding the %d:1 compression ratio limit", expanded, compressed, policy.MaxCompressionRatio)
	}
	return nil
}

func sanitizeFileMode(mode os.FileMode) os.FileMode {
	perm := mode.Perm()
	if perm == 0 {
		return 0o644
	}
	return perm &^ 0o022
}
