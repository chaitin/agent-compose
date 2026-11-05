//go:build linux

package sandboxinit

import (
	"fmt"
	"os"
	"path/filepath"
)

// validateControlDirectory rejects a control directory that group or other can
// write to. The socket itself is mode 0666 so a non-root native daemon can
// connect to a socket created by the container's root init; the directory is
// therefore the only local access gate and must not be replaceable by an
// unrelated user.
func validateControlDirectory(dir string) error {
	reason, err := insecureDirectoryReason(dir)
	if err != nil {
		return fmt.Errorf("inspect sandbox init control directory %s: %w", dir, err)
	}
	if reason != "" {
		return fmt.Errorf("refusing sandbox init control directory %s: %s", dir, reason)
	}
	return nil
}

// insecureDirectoryReason returns a non-empty explanation when path is missing,
// not a directory, or writable by group or other. It is the same rule for the
// control directory and the directory that holds the init binary.
func insecureDirectoryReason(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "the directory does not exist", nil
		}
		return "", err
	}
	if !info.IsDir() {
		return "it is not a directory", nil
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Sprintf("mode %04o is group- or world-writable; use 0700 or 0755 and mount init material read-only", info.Mode().Perm()), nil
	}
	return "", nil
}

// initExecutableDirectoryWarning reports an init binary directory that another
// local user could modify. Init does not own a separate mount of its own
// directory, so a writable directory is a real replacement risk; it is reported
// rather than fatal because a custom guest image may lay the binary out
// differently and the init process cannot repair its own mount.
func initExecutableDirectoryWarning() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(executable)
	reason, err := insecureDirectoryReason(dir)
	if err != nil || reason == "" {
		return ""
	}
	return fmt.Sprintf("sandbox init directory %s is unsafe: %s; mount it read-only", dir, reason)
}
