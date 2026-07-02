package imagecache

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/v1/layout"
)

const ociLayoutReadyFileName = ".ready"

func (c *Cache) MaterializeOCILayout(ctx context.Context, ref string) (MaterializationResult, error) {
	if err := ctx.Err(); err != nil {
		return MaterializationResult{}, NewError(ErrorKindUnavailable, "materialize oci layout", ref, err)
	}
	unlock, err := c.Lock()
	if err != nil {
		return MaterializationResult{}, err
	}
	defer func() { _ = unlock() }()

	metadata, err := c.LoadMetadata()
	if err != nil {
		return MaterializationResult{}, err
	}
	image, ok := lookupImage(metadata.Images, ref)
	if !ok {
		return MaterializationResult{}, NewError(ErrorKindNotFound, "materialize oci layout", ref, fmt.Errorf("image not found"))
	}
	imageID := firstNonEmpty(image.ConfigDigest, image.CacheKey, image.ManifestDigest, image.NormalizedRef)
	if strings.TrimSpace(imageID) == "" {
		return MaterializationResult{}, NewError(ErrorKindInvalidReference, "materialize oci layout", ref, fmt.Errorf("image has no materializable identity"))
	}
	resolvedRef := firstImageRef(image.RepoDigests, image.NormalizedRef, image.ManifestDigest, image.CacheKey)
	sourcePath := firstNonEmpty(image.LayoutCachePath, c.OCILayoutPath())
	if !isValidOCILayoutPath(sourcePath) {
		return MaterializationResult{}, NewError(ErrorKindNotFound, "materialize oci layout", ref, fmt.Errorf("source OCI layout is not ready at %s", sourcePath))
	}

	cacheDir := c.MaterializedImageDir(imageID)
	layoutPath := c.MaterializedOCILayoutPath(imageID)
	readyFlag := filepath.Join(cacheDir, ociLayoutReadyFileName)
	result := MaterializationResult{
		ImageID:     imageID,
		ResolvedRef: resolvedRef,
		LayoutPath:  layoutPath,
	}
	if ReadyFlagExists(readyFlag) && isValidOCILayoutPath(layoutPath) {
		return result, nil
	}
	_ = os.Remove(readyFlag)

	if err := ensureDir(cacheDir); err != nil {
		return MaterializationResult{}, NewError(ErrorKindInternal, "materialize oci layout", ref, err)
	}
	tmpDir := filepath.Join(cacheDir, "oci.tmp")
	tmpLayoutPath := filepath.Join(tmpDir, "layout")
	_ = os.RemoveAll(tmpDir)
	if err := copyDirHardlinkFirst(ctx, sourcePath, tmpLayoutPath); err != nil {
		_ = os.RemoveAll(tmpDir)
		return MaterializationResult{}, NewError(ErrorKindInternal, "materialize oci layout", ref, err)
	}
	if !isValidOCILayoutPath(tmpLayoutPath) {
		_ = os.RemoveAll(tmpDir)
		return MaterializationResult{}, NewError(ErrorKindInternal, "materialize oci layout", ref, fmt.Errorf("materialized OCI layout is invalid"))
	}
	_ = os.RemoveAll(layoutPath)
	if err := os.Rename(tmpLayoutPath, layoutPath); err != nil {
		_ = os.RemoveAll(tmpDir)
		return MaterializationResult{}, NewError(ErrorKindInternal, "materialize oci layout", ref, err)
	}
	_ = os.RemoveAll(tmpDir)
	if err := WriteReadyFlag(readyFlag); err != nil {
		return MaterializationResult{}, err
	}
	return result, nil
}

func isValidOCILayoutPath(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	for _, name := range []string{"oci-layout", "index.json", "blobs"} {
		if _, err := os.Stat(filepath.Join(path, name)); err != nil {
			return false
		}
	}
	index, err := layout.ImageIndexFromPath(path)
	if err != nil {
		return false
	}
	_, err = index.IndexManifest()
	return err == nil
}

func copyDirHardlinkFirst(ctx context.Context, src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !srcInfo.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}
	if err := os.MkdirAll(dst, srcInfo.Mode().Perm()); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode().Type() == 0:
			return linkOrCopyFile(path, target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(linkTarget, target)
		default:
			return fmt.Errorf("unsupported OCI layout entry %s", path)
		}
	})
}

func linkOrCopyFile(src, dst string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = srcFile.Close() }()
	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dstFile, srcFile); err != nil {
		_ = dstFile.Close()
		return err
	}
	return dstFile.Close()
}

func firstImageRef(repoDigests []string, values ...string) string {
	for _, value := range repoDigests {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return firstNonEmpty(values...)
}
