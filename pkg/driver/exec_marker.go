package driver

import (
	"github.com/google/uuid"
)

// guestExecMarkerEnv tags every process a guest exec starts. Driver kill APIs
// do not reliably reach a whole exec tree: BoxLite signals only the exec
// leader, and Microsandbox signals the leader's process group, which misses
// descendants that started their own group (the guest runtime launches
// commands detached). The marker is inherited across those boundaries, so a
// control exec can find and terminate exactly that tree.
const guestExecMarkerEnv = "AGENT_COMPOSE_INTERNAL_EXEC_MARKER"

// markedExecTerminationScript terminates every process whose environment
// carries the marker: SIGTERM first so runtimes and agents can shut down,
// then SIGKILL for anything still left. It exits non-zero when processes
// survive both rounds, so callers can report termination as unconfirmed.
//
// Arguments: $1 is the marker value, $2 the environment variable name.
const markedExecTerminationScript = `
marker=$1
expected=$2=$marker

matching_pids() {
  for path in /proc/[0-9]*/environ; do
    [ -r "$path" ] || continue
    # The group silences the shell's own error when the open fails, as it
    # does for kernel threads that a VM guest exposes in /proc.
    if { tr '\000' '\n' <"$path"; } 2>/dev/null | grep -Fqx "$expected"; then
      pid=${path#/proc/}
      printf '%s\n' "${pid%/environ}"
    fi
  done
}

signal_until_gone() {
  signal=$1
  attempts=$2
  attempt=0
  while [ "$attempt" -lt "$attempts" ]; do
    pids=$(matching_pids)
    [ -z "$pids" ] && return 0
    for pid in $pids; do
      kill "-$signal" "$pid" 2>/dev/null || true
    done
    attempt=$((attempt + 1))
    sleep 0.1
  done
  [ -z "$(matching_pids)" ]
}

signal_until_gone TERM 20 || signal_until_gone KILL 30
`

func newExecMarker() string {
	return uuid.NewString()
}

// markedExecTerminationCommand returns the control command that terminates
// the process tree tagged with markerEnv=marker.
func markedExecTerminationCommand(markerEnv, marker string) []string {
	return []string{"sh", "-c", markedExecTerminationScript, "agent-compose-exec-cleanup", marker, markerEnv}
}

// execEnvMapWithMarker returns a copy of env that tags the exec with marker.
// A caller-supplied marker is replaced so the driver alone decides which tree
// a termination targets.
func execEnvMapWithMarker(env map[string]string, marker string) map[string]string {
	marked := make(map[string]string, len(env)+1)
	for name, value := range env {
		marked[name] = value
	}
	marked[guestExecMarkerEnv] = marker
	return marked
}
