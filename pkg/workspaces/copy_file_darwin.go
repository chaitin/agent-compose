package workspaces

import (
	"golang.org/x/sys/unix"
	"os"
)

func cloneWorkspaceFile(source *os.File, destination string) error {
	return unix.Fclonefileat(int(source.Fd()), unix.AT_FDCWD, destination, 0)
}
