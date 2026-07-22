//go:build linux && cgo && microsandboxcgo

package driver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func validateMicrosandboxLegacyDockerOwnedPath(home, path string) error {
	return validateMicrosandboxPathUnder(home, "docker-disks", path)
}

func validateMicrosandboxRootfsOwnedPath(home, path string) error {
	return validateMicrosandboxPathUnder(home, "rootfs-disks", path)
}

func validateMicrosandboxAnyOwnedPath(home, path string) error {
	clean, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rootfsRoot, err := filepath.Abs(filepath.Join(home, "rootfs-disks"))
	if err != nil {
		return err
	}
	if microsandboxPathWithinRoot(rootfsRoot, clean) && rootfsRoot != clean {
		return validateMicrosandboxRootfsOwnedPath(home, path)
	}
	return validateMicrosandboxLegacyDockerOwnedPath(home, path)
}

func validateMicrosandboxPathUnder(home, directory, path string) error {
	root, err := filepath.Abs(filepath.Join(home, directory))
	if err != nil {
		return err
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if !microsandboxPathWithinRoot(root, target) || filepath.Clean(root) == filepath.Clean(target) {
		return fmt.Errorf("microsandbox owned path %s escapes %s root", path, directory)
	}
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("microsandbox owned path %s is a symlink", path)
	}

	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve microsandbox %s root: %w", directory, err)
	}
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return fmt.Errorf("resolve microsandbox owned path %s: %w", path, err)
	}
	if !microsandboxPathWithinRoot(canonicalRoot, canonicalTarget) || filepath.Clean(canonicalRoot) == filepath.Clean(canonicalTarget) {
		return fmt.Errorf("microsandbox owned path %s escapes %s root through a symlink", path, directory)
	}
	return nil
}

func microsandboxPathWithinRoot(root, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
