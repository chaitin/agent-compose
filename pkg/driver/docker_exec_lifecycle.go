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
)

const (
	dockerExecMarkerEnv = "AGENT_COMPOSE_INTERNAL_DOCKER_EXEC_ID"
)

// Docker has no kill-exec API. Every exec therefore carries a driver-owned
// marker inherited by its descendants. A separate control exec can terminate
// exactly that process tree without stopping a KEEP_RUNNING sandbox.
func newDockerExecMarker() string {
	return newExecMarker()
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

// dockerExecTarget identifies one docker exec instance to terminate.
type dockerExecTarget struct {
	ContainerID string
	ExecID      string
	Marker      string
}

func (r *dockerRuntime) terminateDockerExec(ctx context.Context, dockerClient *client.Client, target dockerExecTarget) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), execTerminationTimeout)
	defer cancel()

	control, err := dockerClient.ContainerExecCreate(cleanupCtx, target.ContainerID, containerapi.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          markedExecTerminationCommand(dockerExecMarkerEnv, target.Marker),
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
	execInfo, err := r.waitForExecExit(cleanupCtx, dockerClient, target.ExecID)
	if err != nil {
		return err
	}
	if execInfo.Running {
		return fmt.Errorf("docker exec %s is still running after termination", target.ExecID)
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
