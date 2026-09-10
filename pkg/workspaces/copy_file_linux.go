package workspaces

import (
	"golang.org/x/sys/unix"
	"os"
)

func cloneWorkspaceFile(source *os.File, destination string) error {
	target, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cloneErr := unix.IoctlFileClone(int(target.Fd()), int(source.Fd()))
	closeErr := target.Close()
	// A close error must never be hidden by falling back after an unsupported ioctl.
	if closeErr != nil {
		return closeErr
	}
	return cloneErr
}
