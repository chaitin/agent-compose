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
	for _, pid := range markedProcessIDs(marker) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	return nil
}

// markedProcessesExist reports whether any live process still carries the
// marker. A detached runtime keeps the marker after its start session leader
// exits, and the egress listener must outlive that leader while such a process
// can still connect.
func markedProcessesExist(marker string) bool {
	return len(markedProcessIDs(marker)) > 0
}

func markedProcessIDs(marker string) []int {
	marker = strings.TrimSpace(marker)
	if marker == "" {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	prefix := markerEnv + "=" + marker
	var pids []int
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
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids
}
