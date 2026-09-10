//go:build linux || darwin

package workspaces

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

func tryLockTransientSnapshotFile(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}
