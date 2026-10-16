//go:build linux

package driver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// identifyBoxliteShimProcess reports whether pid runs the boxlite runtime's own
// shim binary, which is the only process this package is allowed to signal: the
// shim is spawned as a boxlite child from the runtime's own bin directory.
//
// Only the program the process runs is examined - its executable, or the argv[0]
// boxlite handed it - never the rest of the command line. Those arguments carry
// free-form data (box ids, image names, host paths), so matching them would
// accept an unrelated process that merely mentions boxlite in one, and the pid
// comes from a file that outlives the process it names.
func identifyBoxliteShimProcess(pid int) (bool, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("read command line of boxlite shim process %d: %w", pid, err)
	}
	argv0, _, _ := strings.Cut(string(raw), "\x00")
	if isBoxliteShimBinary(argv0) {
		return true, nil
	}
	// A shim started through a wrapper keeps its own pid but not its argv[0], so
	// the binary it executes decides as well. The kernel reports a binary
	// replaced on disk while the shim ran as "<path> (deleted)".
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("read executable of boxlite shim process %d: %w", pid, err)
	}
	return isBoxliteShimBinary(strings.TrimSuffix(exe, " (deleted)")), nil
}

// isBoxliteShimBinary reports whether path names the boxlite runtime's shim
// binary. The comparison is on the file name, so a runtime that provides the
// shim on PATH or through a symlink still identifies it.
func isBoxliteShimBinary(path string) bool {
	return filepath.Base(strings.TrimSpace(path)) == boxliteShimExecutableName
}

// terminateBoxliteShim kills the shim and, when it leads one, its whole process
// group: the bwrap jail around the VM inherits the /dev/kvm handle and exits
// with the process it wraps.
func terminateBoxliteShim(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("boxlite shim pid %d is not a valid process id", pid)
	}
	if pgid, err := unix.Getpgid(pid); err == nil && pgid == pid {
		if err := unix.Kill(-pgid, unix.SIGKILL); err == nil || errors.Is(err, unix.ESRCH) {
			return nil
		}
	}
	if err := unix.Kill(pid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
		return fmt.Errorf("kill boxlite shim process %d: %w", pid, err)
	}
	return nil
}

// boxliteProcessAlive reports whether pid still runs. A SIGKILLed shim becomes
// a zombie until boxlite reaps it, and a zombie holds no KVM handle, so it
// counts as gone rather than as a signal that did not work.
//
// Anything other than "no such process" counts as alive: reporting success for a
// process this process cannot inspect would hide the leaked handle the reaper
// exists to release.
func boxliteProcessAlive(pid int) bool {
	if err := unix.Kill(pid, 0); err != nil {
		return !errors.Is(err, unix.ESRCH)
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	// The state field follows comm, which is parenthesised and may itself contain
	// spaces and ')', so parse from the last one.
	index := strings.LastIndex(string(raw), ")")
	if index < 0 || index+2 >= len(raw) {
		return true
	}
	return raw[index+2] != 'Z'
}
