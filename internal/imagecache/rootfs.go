package imagecache

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
)

const rootFSReadyFileName = ".rootfs.ready"

func (c *Cache) MaterializeRootFS(ctx context.Context, ref string) (MaterializationResult, error) {
	if err := ctx.Err(); err != nil {
		return MaterializationResult{}, NewError(ErrorKindUnavailable, "materialize rootfs", ref, err)
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
		return MaterializationResult{}, NewError(ErrorKindNotFound, "materialize rootfs", ref, fmt.Errorf("image not found"))
	}
	imageID := firstNonEmpty(image.ConfigDigest, image.CacheKey, image.ManifestDigest, image.NormalizedRef)
	if strings.TrimSpace(imageID) == "" {
		return MaterializationResult{}, NewError(ErrorKindInvalidReference, "materialize rootfs", ref, fmt.Errorf("image has no materializable identity"))
	}
	sourcePath := firstNonEmpty(image.LayoutCachePath, c.OCILayoutPath())
	if !isValidOCILayoutPath(sourcePath) {
		return MaterializationResult{}, NewError(ErrorKindNotFound, "materialize rootfs", ref, fmt.Errorf("source OCI layout is not ready at %s", sourcePath))
	}

	cacheDir := c.MaterializedImageDir(imageID)
	rootfsPath := c.MaterializedRootFSPath(imageID)
	readyFlag := filepath.Join(cacheDir, rootFSReadyFileName)
	result := MaterializationResult{
		ImageID:     imageID,
		ResolvedRef: firstImageRef(image.RepoDigests, image.NormalizedRef, image.ManifestDigest, image.CacheKey),
		LayoutPath:  c.MaterializedOCILayoutPath(imageID),
		RootFSPath:  rootfsPath,
	}
	if ReadyFlagExists(readyFlag) && isUsableRootFS(rootfsPath) {
		return result, nil
	}
	_ = os.Remove(readyFlag)

	if err := ensureDir(cacheDir); err != nil {
		return MaterializationResult{}, NewError(ErrorKindInternal, "materialize rootfs", ref, err)
	}
	tmpDir := filepath.Join(cacheDir, "rootfs.tmp")
	tmpRootFSPath := filepath.Join(tmpDir, "rootfs")
	_ = os.RemoveAll(tmpDir)
	if err := os.MkdirAll(tmpRootFSPath, 0o755); err != nil {
		return MaterializationResult{}, NewError(ErrorKindInternal, "materialize rootfs", ref, err)
	}
	if err := extractRootFSFromOCILayout(ctx, sourcePath, image.ManifestDigest, tmpRootFSPath); err != nil {
		_ = os.RemoveAll(tmpDir)
		return MaterializationResult{}, NewError(ErrorKindInternal, "materialize rootfs", ref, err)
	}
	_ = os.RemoveAll(rootfsPath)
	if err := os.Rename(tmpRootFSPath, rootfsPath); err != nil {
		_ = os.RemoveAll(tmpDir)
		return MaterializationResult{}, NewError(ErrorKindInternal, "materialize rootfs", ref, err)
	}
	_ = os.RemoveAll(tmpDir)
	if err := WriteReadyFlag(readyFlag); err != nil {
		return MaterializationResult{}, err
	}
	if idx := indexOfImage(metadata.Images, image); idx >= 0 {
		metadata.Images[idx].RootFSCachePath = rootfsPath
		if err := c.SaveMetadata(metadata); err != nil {
			return MaterializationResult{}, err
		}
	}
	return result, nil
}

func extractRootFSFromOCILayout(ctx context.Context, layoutPath, manifestDigest, dstDir string) error {
	index, err := layout.ImageIndexFromPath(layoutPath)
	if err != nil {
		return fmt.Errorf("open OCI layout %s: %w", layoutPath, err)
	}
	hash, err := v1.NewHash(manifestDigest)
	if err != nil {
		return fmt.Errorf("parse manifest digest %s: %w", manifestDigest, err)
	}
	image, err := index.Image(hash)
	if err != nil {
		return fmt.Errorf("open image %s: %w", manifestDigest, err)
	}
	layers, err := image.Layers()
	if err != nil {
		return fmt.Errorf("list image layers: %w", err)
	}
	for idx, layer := range layers {
		if err := ctx.Err(); err != nil {
			return err
		}
		reader, err := layer.Uncompressed()
		if err != nil {
			return fmt.Errorf("open layer %d: %w", idx, err)
		}
		if err := applyLayerTarArchive(ctx, reader, dstDir); err != nil {
			_ = reader.Close()
			return fmt.Errorf("apply layer %d: %w", idx, err)
		}
		if err := reader.Close(); err != nil {
			return fmt.Errorf("close layer %d: %w", idx, err)
		}
	}
	return nil
}

func applyLayerTarArchive(ctx context.Context, src io.Reader, dstDir string) error {
	reader := tar.NewReader(src)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if header == nil {
			continue
		}

		relPath, err := cleanLayerPath(header.Name)
		if err != nil {
			return fmt.Errorf("invalid tar entry %q: %w", header.Name, err)
		}
		if relPath == "" {
			continue
		}

		base := filepath.Base(relPath)
		dir := filepath.Dir(relPath)
		if base == ".wh..wh..opq" {
			if dir == "." {
				dir = ""
			}
			if err := clearRootFSDirectory(dstDir, dir); err != nil {
				return fmt.Errorf("apply opaque whiteout for %s: %w", header.Name, err)
			}
			continue
		}
		if strings.HasPrefix(base, ".wh.") {
			target := strings.TrimPrefix(base, ".wh.")
			if target == "" {
				continue
			}
			targetRel := filepath.Join(dir, target)
			if dir == "." {
				targetRel = target
			}
			targetPath, err := safeRootFSPath(dstDir, targetRel)
			if err != nil {
				return fmt.Errorf("apply whiteout for %s: %w", header.Name, err)
			}
			if err := os.RemoveAll(targetPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove whiteout target %s: %w", targetRel, err)
			}
			continue
		}

		targetPath, err := safeRootFSPath(dstDir, relPath)
		if err != nil {
			return fmt.Errorf("resolve %s: %w", header.Name, err)
		}
		if err := ensureSafeParentDir(dstDir, relPath); err != nil {
			return fmt.Errorf("prepare parent for %s: %w", header.Name, err)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := ensureNoSymlinkAt(targetPath); err != nil {
				return fmt.Errorf("create dir %s: %w", relPath, err)
			}
			if err := os.MkdirAll(targetPath, header.FileInfo().Mode().Perm()); err != nil {
				return fmt.Errorf("create dir %s: %w", relPath, err)
			}
		case tar.TypeReg, 0:
			if err := os.RemoveAll(targetPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove existing file %s: %w", relPath, err)
			}
			file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, header.FileInfo().Mode().Perm())
			if err != nil {
				return fmt.Errorf("create file %s: %w", relPath, err)
			}
			if _, err := io.Copy(file, reader); err != nil {
				_ = file.Close()
				return fmt.Errorf("write file %s: %w", relPath, err)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("close file %s: %w", relPath, err)
			}
		case tar.TypeSymlink:
			if err := os.RemoveAll(targetPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove existing symlink path %s: %w", relPath, err)
			}
			if err := os.Symlink(header.Linkname, targetPath); err != nil {
				return fmt.Errorf("create symlink %s -> %s: %w", relPath, header.Linkname, err)
			}
		case tar.TypeLink:
			linkRel, err := cleanLayerPath(header.Linkname)
			if err != nil || linkRel == "" {
				return fmt.Errorf("invalid hardlink target %q: %w", header.Linkname, err)
			}
			linkPath, err := safeRootFSPath(dstDir, linkRel)
			if err != nil {
				return fmt.Errorf("resolve hardlink target %s: %w", header.Linkname, err)
			}
			if err := os.RemoveAll(targetPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove existing hardlink path %s: %w", relPath, err)
			}
			if err := os.Link(linkPath, targetPath); err != nil {
				return fmt.Errorf("create hardlink %s -> %s: %w", relPath, linkRel, err)
			}
		case tar.TypeXGlobalHeader, tar.TypeXHeader, tar.TypeGNULongLink, tar.TypeGNULongName:
			continue
		default:
			return fmt.Errorf("unsupported tar entry type %q for %s", string(header.Typeflag), relPath)
		}
	}
}

func cleanLayerPath(value string) (string, error) {
	value = filepath.Clean(strings.TrimPrefix(strings.TrimSpace(value), "./"))
	if value == "." || value == "" {
		return "", nil
	}
	if filepath.IsAbs(value) || value == ".." || strings.HasPrefix(value, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes rootfs")
	}
	return value, nil
}

func safeRootFSPath(root, rel string) (string, error) {
	cleanRel, err := cleanLayerPath(rel)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, cleanRel)
	resolvedRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolvedTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if resolvedTarget != resolvedRoot && !strings.HasPrefix(resolvedTarget, resolvedRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes rootfs")
	}
	return target, nil
}

func ensureSafeParentDir(root, rel string) error {
	parent := filepath.Dir(rel)
	if parent == "." || parent == "" {
		return nil
	}
	parts := strings.Split(parent, string(os.PathSeparator))
	currentRel := ""
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		currentRel = filepath.Join(currentRel, part)
		path, err := safeRootFSPath(root, currentRel)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("parent %s is a symlink", currentRel)
			}
			if !info.IsDir() {
				return fmt.Errorf("parent %s is not a directory", currentRel)
			}
			continue
		}
		if !os.IsNotExist(err) {
			return err
		}
		if err := os.Mkdir(path, 0o755); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nil
}

func ensureNoSymlinkAt(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", path)
	}
	return nil
}

func clearRootFSDirectory(root, rel string) error {
	path, err := safeRootFSPath(root, rel)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(path, 0o755)
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", rel)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func isUsableRootFS(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
