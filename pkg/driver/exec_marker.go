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

// markedExecLeaderTerminationScript terminates only the exec leader, leaving
// any detached descendants running. It backs KillOnlyOnCancel, where the exec
// is a control-plane process (detach start/attach) whose marker is shared with
// the detached tree: the supervisor and task also carry the marker, so the
// full-tree script above would take them down with the exec.
//
// The leader is the marker-carrying process whose parent does not carry the
// marker (the root of the marker tree); a detached descendant's parent is a
// marker-carrying process, so it is never chosen. The leader and its
// non-detached child (e.g. the `start` subcommand) share the leader's process
// group, while the detached supervisor/task start their own, so killing the
// root plus every process in its group stops exactly the control-plane part.
//
// Arguments: $1 is the marker value, $2 the environment variable name.
const markedExecLeaderTerminationScript = `
marker=$1
expected=$2=$marker

matching_pids() {
  for path in /proc/[0-9]*/environ; do
    [ -r "$path" ] || continue
    if { tr '\000' '\n' <"$path"; } 2>/dev/null | grep -Fqx "$expected"; then
      pid=${path#/proc/}
      printf '%s\n' "${pid%/environ}"
    fi
  done
}

root=
for pid in $(matching_pids); do
  # /proc/PID/stat's comm field may itself contain spaces and parens, so drop
  # everything through the last ') ' and read the fields that follow:
  # state ppid pgrp session ...
  IFS= read -r stat < /proc/"$pid"/stat 2>/dev/null || continue
  rest=${stat##*) }
  set -- $rest
  ppid=$2
  if { tr '\000' '\n' <"/proc/$ppid/environ"; } 2>/dev/null | grep -Fqx "$expected"; then
    continue
  fi
  root=$pid
  break
done

if [ -n "$root" ]; then
  # The leader need not be its own process-group leader: a docker exec
  # non-TTY control process can inherit a group whose leader lives outside the
  # container's pid namespace (its pgrp then reads 0). Compare each candidate's
  # pgrp against the leader's own pgrp, not the leader's pid.
  root_pgrp=
  if IFS= read -r stat < /proc/"$root"/stat 2>/dev/null; then
    rest=${stat##*) }
    set -- $rest
    root_pgrp=$3
  fi
  for pid in $(matching_pids); do
    IFS= read -r stat < /proc/"$pid"/stat 2>/dev/null || continue
    rest=${stat##*) }
    set -- $rest
    pgrp=$3
    killit=
    if [ "$pid" = "$root" ]; then
      killit=1
    elif [ -n "$root_pgrp" ] && [ "$pgrp" = "$root_pgrp" ]; then
      killit=1
    fi
    if [ -n "$killit" ]; then
      kill -KILL "$pid" 2>/dev/null || true
    fi
  done
fi
`

func newExecMarker() string {
	return uuid.NewString()
}

// markedExecTerminationCommand returns the control command that terminates
// the process tree tagged with markerEnv=marker.
func markedExecTerminationCommand(markerEnv, marker string) []string {
	return []string{"sh", "-c", markedExecTerminationScript, "agent-compose-exec-cleanup", marker, markerEnv}
}

// markedExecLeaderTerminationCommand returns the control command that
// terminates only the exec leader for markerEnv=marker, leaving detached
// descendants running.
func markedExecLeaderTerminationCommand(markerEnv, marker string) []string {
	return []string{"sh", "-c", markedExecLeaderTerminationScript, "agent-compose-exec-leader-cleanup", marker, markerEnv}
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
