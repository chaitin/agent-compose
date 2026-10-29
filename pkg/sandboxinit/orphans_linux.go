//go:build linux

package sandboxinit

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// killMarkedProcesses terminates processes that inherited the init marker but
// are no longer reachable through their original process group. The manager
// only knows the exec leader; a daemonized descendant may have its own group,
// so /proc is the only complete answer inside the PID namespace.
func killMarkedProcesses(marker string) error {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	prefix := markerEnv + "=" + marker
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		data, err := os.ReadFile("/proc/" + entry.Name() + "/environ")
		if err != nil {
			continue
		}
		for _, item := range strings.Split(string(data), "\x00") {
			if item == prefix {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				break
			}
		}
	}
	return nil
}
