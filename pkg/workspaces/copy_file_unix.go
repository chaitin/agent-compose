//go:build linux || darwin

package workspaces

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func cloneUnsupported(err error) bool {
	var pathError *os.PathError
	if errors.As(err, &pathError) && pathError.Op == "close" {
		return false
	}
	return errors.Is(err, errors.ErrUnsupported) || errors.Is(err, unix.EXDEV) || errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL)
}

func openWorkspaceCopySource(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
}
