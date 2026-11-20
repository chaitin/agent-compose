//go:build unix

package runinputs

import "golang.org/x/sys/unix"

// freeSpace reports the bytes available to unprivileged writers and the
// filesystem size.
func freeSpace(path string) (uint64, uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, 0, err
	}
	blockSize := uint64(stat.Bsize) //nolint:gosec // block sizes are small positive values
	return stat.Bavail * blockSize, stat.Blocks * blockSize, nil
}
