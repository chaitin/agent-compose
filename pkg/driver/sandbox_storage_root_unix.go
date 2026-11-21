//go:build unix

package driver

import (
	"os"
	"syscall"
)

func checkStorageRootAncestorsOnHost(path string) error {
	return checkStorageRootAncestors(path, uint32(os.Geteuid()), lstatStorageRootEntry)
}

func lstatStorageRootEntry(path string) (storageRootEntry, error) {
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		return storageRootEntry{}, err
	}
	mode := uint32(st.Mode)
	return storageRootEntry{
		isDir: mode&syscall.S_IFMT == syscall.S_IFDIR,
		mode:  mode,
		uid:   st.Uid,
	}, nil
}
