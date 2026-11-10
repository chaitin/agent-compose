//go:build linux

package driver

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// extractOCILayer unpacks one OCI layer blob into root.
//
// Layer entries and symlink targets come from an untrusted image. Every path
// is resolved inside root before it is created or removed: lexical escapes are
// rejected, and symlink components are resolved with root acting as the
// chroot, so neither an absolute nor a relative link can redirect a write
// outside the merged rootfs.
func extractOCILayer(root, blob string) error {
	file, err := os.Open(blob)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	var reader io.Reader = file
	header := make([]byte, 2)
	if n, _ := io.ReadFull(file, header); n == 2 && header[0] == 0x1f && header[1] == 0x8b {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		gz, err := gzip.NewReader(file)
		if err != nil {
			return err
		}
		defer func() { _ = gz.Close() }()
		reader = gz
	} else if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}

	tarReader := tar.NewReader(reader)
	for {
		entry, err := tarReader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := extractOCITarEntry(root, tarReader, entry); err != nil {
			return fmt.Errorf("layer entry %q: %w", entry.Name, err)
		}
	}
}

func extractOCITarEntry(root string, body io.Reader, entry *tar.Header) error {
	name, err := cleanLayerPath(entry.Name)
	if err != nil {
		return err
	}
	if name == "." {
		return nil
	}

	base := path.Base(name)
	if base == ".wh..wh..opq" {
		return clearOpaqueLayerDirectory(root, path.Dir(name))
	}
	if strings.HasPrefix(base, ".wh.") {
		return removeWhiteoutTarget(root, name, base)
	}

	target, err := resolveLayerParent(root, name)
	if err != nil {
		return err
	}
	switch entry.Typeflag {
	case tar.TypeDir:
		if err := removeFinalSymlink(target); err != nil {
			return err
		}
		if err := os.MkdirAll(target, os.FileMode(entry.Mode)); err != nil {
			return err
		}
	case tar.TypeReg, tar.TypeRegA: //nolint:staticcheck // Legacy tar archives encode regular files as NUL.
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		// A regular file replaces an existing symlink instead of writing
		// through it, matching tar's overwrite semantics and keeping the write
		// inside the merged rootfs.
		if err := removeFinalSymlink(target); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(entry.Mode))
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, body)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	case tar.TypeSymlink:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		// Keep the image's link text verbatim. Later entries resolve it with
		// root as the chroot, so an escaping target cannot redirect a write.
		if err := os.Symlink(entry.Linkname, target); err != nil {
			return err
		}
	case tar.TypeLink:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		source, err := resolveLayerParent(root, entry.Linkname)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		if err := os.Link(source, target); err != nil {
			return err
		}
	case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
		// Device nodes and FIFOs are not part of the image rootfs contract;
		// the guest runtime creates them as needed.
		return nil
	default:
		return nil
	}

	_ = os.Lchown(target, entry.Uid, entry.Gid)
	if entry.Typeflag != tar.TypeSymlink {
		if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink == 0 {
			_ = os.Chmod(target, os.FileMode(entry.Mode))
		}
	}
	return nil
}

// cleanLayerPath normalizes a tar entry name to a slash path relative to the
// rootfs. Absolute names are treated as root-relative, and names that escape
// the rootfs are rejected rather than silently clamped.
func cleanLayerPath(name string) (string, error) {
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("layer path contains NUL: %q", name)
	}
	cleaned := path.Clean(name)
	if path.IsAbs(cleaned) {
		cleaned = strings.TrimPrefix(cleaned, "/")
	}
	if cleaned == "" || cleaned == "." {
		return ".", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("layer path escapes rootfs: %q", name)
	}
	return cleaned, nil
}

// resolveLayerPath resolves name under root, following symlinks with root as
// the chroot. An absolute link target restarts at root; a relative link target
// resolves against the link's directory. A link that would leave root is an
// error, and so is a lexical .. that would climb past root.
func resolveLayerPath(root, name string) (string, error) {
	cleanRoot := filepath.Clean(root)
	resolved := cleanRoot
	cleaned, err := cleanLayerPath(name)
	if err != nil {
		return "", err
	}
	pending := strings.Split(cleaned, "/")
	links := 0
	for len(pending) > 0 {
		component := pending[0]
		pending = pending[1:]
		switch component {
		case "", ".":
			continue
		case "..":
			if resolved == cleanRoot {
				return "", fmt.Errorf("layer path escapes rootfs: %q", name)
			}
			resolved = filepath.Dir(resolved)
			if !withinRoot(cleanRoot, resolved) {
				return "", fmt.Errorf("layer path escapes rootfs: %q", name)
			}
			continue
		}

		next := filepath.Join(resolved, component)
		info, err := os.Lstat(next)
		if err != nil {
			if os.IsNotExist(err) {
				// Nothing below a missing component can be a symlink yet.
				resolved = next
				continue
			}
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = next
			continue
		}

		links++
		if links > 255 {
			return "", fmt.Errorf("too many symlinks resolving %q", name)
		}
		link, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		link = filepath.ToSlash(link)
		if path.IsAbs(link) {
			resolved = cleanRoot
			link = strings.TrimPrefix(path.Clean(link), "/")
		} else {
			resolved = filepath.Dir(next)
			link = path.Clean(link)
		}
		pending = append(strings.Split(link, "/"), pending...)
	}
	if !withinRoot(cleanRoot, resolved) {
		return "", fmt.Errorf("layer path escapes rootfs: %q", name)
	}
	return resolved, nil
}

// resolveLayerParent resolves the parent of name and returns the resulting
// final path without following a symlink in the final component. Callers use
// it for operations that must act on the entry itself, such as replacing a
// symlink with a regular file.
func resolveLayerParent(root, name string) (string, error) {
	cleaned, err := cleanLayerPath(name)
	if err != nil {
		return "", err
	}
	if cleaned == "." {
		return filepath.Clean(root), nil
	}
	dir, base := path.Split(cleaned)
	parent, err := resolveLayerPath(root, dir)
	if err != nil {
		return "", err
	}
	target := filepath.Join(parent, base)
	if !withinRoot(filepath.Clean(root), target) {
		return "", fmt.Errorf("layer path escapes rootfs: %q", name)
	}
	return target, nil
}

func removeWhiteoutTarget(root, name, base string) error {
	targetName := strings.TrimPrefix(base, ".wh.")
	if targetName == "" || targetName == "." || targetName == ".." {
		return fmt.Errorf("invalid whiteout entry %q", name)
	}
	target, err := resolveLayerParent(root, path.Join(path.Dir(name), targetName))
	if err != nil {
		return err
	}
	return os.RemoveAll(target)
}

func clearOpaqueLayerDirectory(root, name string) error {
	dir, err := resolveLayerPath(root, name)
	if err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func removeFinalSymlink(target string) error {
	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	return os.Remove(target)
}

func withinRoot(root, target string) bool {
	return target == root || strings.HasPrefix(target, root+string(os.PathSeparator))
}
