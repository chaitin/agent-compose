package driver

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	dockertypes "github.com/docker/docker/api/types"
	containerapi "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/google/uuid"
)

const (
	dockerExecMarkerEnv = "AGENT_COMPOSE_INTERNAL_EXECUTION_ID"
)

// Docker has no kill-exec API. Every exec therefore carries a driver-owned
// marker inherited by its descendants. A separate control exec can terminate
// exactly that process tree without stopping a KEEP_RUNNING sandbox.
const dockerTerminateMarkedExecScript = `
marker=$1
expected=` + dockerExecMarkerEnv + `=$marker

matching_pids() {
  for path in /proc/[0-9]*/environ; do
    [ -r "$path" ] || continue
    if tr '\000' '\n' <"$path" 2>/dev/null | grep -Fqx "$expected"; then
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

func newDockerExecMarker() string {
	return uuid.NewString()
}

func dockerExecEnvWithMarker(env []string, marker string) []string {
	prefix := dockerExecMarkerEnv + "="
	marked := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			marked = append(marked, item)
		}
	}
	marked = append(marked, prefix+marker)
	sort.Strings(marked)
	return marked
}

func (r *dockerRuntime) terminateDockerExec(ctx context.Context, dockerClient *client.Client, containerID, execID, marker string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), execTerminationTimeout)
	defer cancel()

	control, err := dockerClient.ContainerExecCreate(cleanupCtx, containerID, containerapi.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          []string{"sh", "-c", dockerTerminateMarkedExecScript, "agent-compose-exec-cleanup", marker},
		User:         "0",
		WorkingDir:   "/",
	})
	if err != nil {
		return fmt.Errorf("create docker exec termination control: %w", err)
	}
	attach, err := dockerClient.ContainerExecAttach(cleanupCtx, control.ID, containerapi.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("start docker exec termination control %s: %w", control.ID, err)
	}
	if err := drainDockerControlExec(attach); err != nil {
		return fmt.Errorf("read docker exec termination control %s: %w", control.ID, err)
	}
	controlInfo, err := r.waitForExecExit(cleanupCtx, dockerClient, control.ID)
	if err != nil {
		return err
	}
	if controlInfo.ExitCode != 0 {
		return fmt.Errorf("docker exec termination control %s exited with code %d", control.ID, controlInfo.ExitCode)
	}
	execInfo, err := r.waitForExecExit(cleanupCtx, dockerClient, execID)
	if err != nil {
		return err
	}
	if execInfo.Running {
		return fmt.Errorf("docker exec %s is still running after termination", execID)
	}
	return nil
}

func drainDockerControlExec(attach dockertypes.HijackedResponse) error {
	defer attach.Close()
	_, err := stdcopy.StdCopy(io.Discard, io.Discard, attach.Reader)
	if err != nil && !isDockerStreamClosed(err) {
		return err
	}
	return nil
}
