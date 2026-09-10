//go:build linux || darwin

package workspaces

import (
	"context"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceCloneFallbackDoesNotHideStorageFailures(t *testing.T) {
	for _, err := range []error{unix.EXDEV, unix.EOPNOTSUPP, unix.ENOTTY, unix.ENOSYS, unix.EINVAL} {
		if !cloneUnsupported(err) {
			t.Fatalf("unsupported clone classified as fatal: %v", err)
		}
	}
	for _, err := range []error{unix.ENOSPC, unix.EIO, unix.EACCES, unix.EROFS, &os.PathError{Op: "close", Err: unix.EINVAL}} {
		if cloneUnsupported(err) {
			t.Fatalf("storage failure hidden by fallback: %v", err)
		}
	}
}

func TestWorkspaceCopyRejectsFIFOWithoutOpeningIt(t *testing.T) {
	src := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(src, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := CopyRootDirectoryContentsContext(context.Background(), root, t.TempDir()); err == nil {
		t.Fatal("accepted FIFO")
	}
}
