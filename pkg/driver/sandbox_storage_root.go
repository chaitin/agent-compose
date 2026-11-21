package driver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrSandboxStorageRootUnsafe reports that a VM driver's storage root sits
// under a directory other accounts could use to substitute the path, so the
// driver refuses to touch its catalog. It is a deployment precondition:
// retrying unchanged cannot succeed until an operator fixes the permissions.
var ErrSandboxStorageRootUnsafe = errors.New("sandbox storage root is not protected from other accounts")

// storageRootEntry is the part of an lstat result the ancestor check reads.
type storageRootEntry struct {
	isDir bool
	// mode is the raw st_mode, so the permission and sticky bits compare the
	// same way the upstream check compares them.
	mode uint32
	uid  uint32
}

// storageRootLstat reads one path without following a final symlink.
type storageRootLstat func(path string) (storageRootEntry, error)

const (
	storageRootGroupOtherWrite = 0o022
	storageRootGroupOtherAll   = 0o077
	storageRootSticky          = 0o1000
)

// checkStorageRootAncestors applies Microsandbox's catalog ancestor rule
// (crates/db/lib/private_storage/storage.rs verify_ancestors, v0.7.14) to an
// absolute, symlink-free path. Walking from the filesystem root down, every
// component must be a directory owned by root or by euid. Until a private
// boundary is crossed it must also not be group- or other-writable unless it
// is sticky (like /tmp). A private boundary is a directory owned by euid with
// no group or other permission bits: past it, other accounts cannot reach the
// descendants, so their permissive bits no longer matter.
//
// The check runs here, before the driver calls into Microsandbox, because the
// upstream failure surfaces only as an opaque io error from whichever sandbox
// operation first touches the catalog.
func checkStorageRootAncestors(path string, euid uint32, lstat storageRootLstat) error {
	privateBoundary := false
	for _, ancestor := range storageRootAncestorsFromRoot(path) {
		entry, err := lstat(ancestor)
		if err != nil {
			return fmt.Errorf("inspect storage root ancestor %s: %w", ancestor, err)
		}
		trustedOwner := entry.uid == euid || entry.uid == 0
		switch {
		case !entry.isDir:
			return fmt.Errorf("%w: ancestor %s of %s is not a real directory", ErrSandboxStorageRootUnsafe, ancestor, path)
		case !trustedOwner:
			return fmt.Errorf("%w: ancestor %s of %s is owned by uid %d, not root or the driver's uid %d",
				ErrSandboxStorageRootUnsafe, ancestor, path, entry.uid, euid)
		case !privateBoundary && entry.mode&storageRootGroupOtherWrite != 0 && entry.mode&storageRootSticky == 0:
			return fmt.Errorf("%w: ancestor %s of %s has mode %04o, which lets other accounts replace its entries; "+
				"remove group and other write permission from it (chmod go-w), on the host directory when it is a bind mount",
				ErrSandboxStorageRootUnsafe, ancestor, path, entry.mode&0o7777)
		}
		if !privateBoundary {
			privateBoundary = entry.uid == euid && entry.mode&storageRootGroupOtherAll == 0
		}
	}
	return nil
}

// storageRootAncestorsFromRoot lists path and its ancestors, filesystem root
// first.
func storageRootAncestorsFromRoot(path string) []string {
	var reversed []string
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		reversed = append(reversed, current)
		if parent := filepath.Dir(current); parent == current {
			break
		}
	}
	ordered := make([]string, len(reversed))
	for i, ancestor := range reversed {
		ordered[len(reversed)-1-i] = ancestor
	}
	return ordered
}

// CheckMicrosandboxStorageRoot reports whether Microsandbox will accept home
// as its storage root. A home that does not exist yet is judged by its deepest
// existing ancestor: the driver creates the missing components itself, owned
// by its own uid with mode 0755, which the rule accepts.
func CheckMicrosandboxStorageRoot(home string) error {
	if home == "" {
		return nil
	}
	absolute, err := filepath.Abs(home)
	if err != nil {
		return fmt.Errorf("resolve microsandbox storage root %s: %w", home, err)
	}
	existing := absolute
	for {
		if _, statErr := os.Lstat(existing); statErr == nil {
			break
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect microsandbox storage root %s: %w", existing, statErr)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return nil
		}
		existing = parent
	}
	// Upstream canonicalizes before walking, so a symlinked component is judged
	// by the directory it resolves to rather than rejected as a non-directory.
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return fmt.Errorf("resolve microsandbox storage root %s: %w", existing, err)
	}
	return checkStorageRootAncestorsOnHost(resolved)
}
