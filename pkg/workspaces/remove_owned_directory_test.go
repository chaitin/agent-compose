//go:build linux || darwin

package workspaces

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A read-only directory whose only entry is a symlink is the shape that makes
// the standard library's RemoveAll report its internal errSymlink sentinel
// instead of ErrPermission on Go 1.26.2, and that error panics when formatted
// (golang/go#78490, fixed in Go 1.26.5). The owned tree must still be removed,
// without following the link out of it.
func TestRemoveOwnedDirectoryRepairsReadOnlySymlinkDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which ignores the file permissions this test needs")
	}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "keep")
	if err := os.WriteFile(sentinel, []byte("outside"), 0o400); err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(t.TempDir(), "generation")
	readonly := filepath.Join(owned, "readonly")
	if err := os.MkdirAll(readonly, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(readonly, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(readonly, 0o500); err != nil {
		t.Fatal(err)
	}
	// Cleanups run last in, first out: restore write permission before the
	// temporary root tries to empty a tree the assertion above may have left.
	t.Cleanup(func() { _ = os.Chmod(readonly, 0o700) })

	if err := RemoveOwnedDirectory(owned); err != nil {
		t.Fatalf("remove read-only tree holding a symlink: %v", err)
	}
	if _, err := os.Lstat(owned); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned tree remains after cleanup: %v", err)
	}
	info, err := os.Stat(sentinel)
	if err != nil || info.Mode().Perm() != 0o400 {
		t.Fatalf("cleanup followed the symlink: mode %v, error %v", info, err)
	}
}
