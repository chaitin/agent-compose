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
	if err := r.terminateDockerProcessTree(cleanupCtx, dockerClient, target.ContainerID, target.Marker); err != nil {
		return err
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

// terminateDockerExecOnFailure terminates an exec whose stream ended in error.
// KillOnlyOnCancel kills only the exec leader (the exec shares its marker with
// the detached tree that must survive); otherwise the whole marker tree is
// terminated.
func (r *dockerRuntime) terminateDockerExecOnFailure(ctx context.Context, dockerClient *client.Client, spec ExecSpec, target dockerExecTarget) error {
	if spec.KillOnlyOnCancel {
		return r.terminateDockerExecLeader(ctx, dockerClient, target.ContainerID, target.ExecID, target.Marker)
	}
	return r.terminateDockerExec(ctx, dockerClient, target)
}

// terminateDockerExecLeader kills only the exec's own process (the leader),
// leaving any detached descendants running. It backs KillOnlyOnCancel, where
// the exec is a control-plane process (detach start/attach) whose detached
// tree must survive a daemon disconnect. Unlike terminateDockerExec it must
// not match the whole marker tree: the start exec shares its marker (the
// execution id) with the supervisor and task, so a full marker termination
// would take them down with the exec. The leader is found by container-side
// process inspection (ContainerExecInspect.Pid is the host-namespace pid, which
// is not addressable from inside the container's private pid namespace).
func (r *dockerRuntime) terminateDockerExecLeader(ctx context.Context, dockerClient *client.Client, containerID, execID, marker string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), execTerminationTimeout)
	defer cancel()
	info, err := dockerClient.ContainerExecInspect(cleanupCtx, execID)
	if err != nil {
		return fmt.Errorf("inspect docker exec %s to terminate: %w", execID, err)
	}
	if info.Running {
		control, err := dockerClient.ContainerExecCreate(cleanupCtx, containerID, containerapi.ExecOptions{
			AttachStdout: true,
			AttachStderr: true,
			Cmd:          markedExecLeaderTerminationCommand(dockerExecMarkerEnv, marker),
			User:         "0",
			WorkingDir:   "/",
		})
		if err != nil {
			return fmt.Errorf("create docker exec leader termination: %w", err)
		}
		attach, err := dockerClient.ContainerExecAttach(cleanupCtx, control.ID, containerapi.ExecAttachOptions{})
		if err != nil {
			return fmt.Errorf("start docker exec leader termination %s: %w", control.ID, err)
		}
		if err := drainDockerControlExec(attach); err != nil {
			return fmt.Errorf("read docker exec leader termination %s: %w", control.ID, err)
		}
	}
	// The leader is gone; the exec reports itself exited once its process is
	// reaped. A detached descendant in its own process group is not affected.
	if _, err := r.waitForExecExit(cleanupCtx, dockerClient, execID); err != nil {
		return err
	}
	return nil
}

// terminateDockerProcessTree runs the marker termination control exec against a
// container. It has no dependence on a specific exec handle, so it also backs
// ProcessTreeTerminator for a detached task whose supervisor died.
func (r *dockerRuntime) terminateDockerProcessTree(ctx context.Context, dockerClient *client.Client, containerID, marker string) error {
	control, err := dockerClient.ContainerExecCreate(ctx, containerID, containerapi.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          markedExecTerminationCommand(dockerExecMarkerEnv, marker),
		User:         "0",
		WorkingDir:   "/",
	})
	if err != nil {
		return fmt.Errorf("create docker exec termination control: %w", err)
	}
	attach, err := dockerClient.ContainerExecAttach(ctx, control.ID, containerapi.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("start docker exec termination control %s: %w", control.ID, err)
	}
	if err := drainDockerControlExec(attach); err != nil {
		return fmt.Errorf("read docker exec termination control %s: %w", control.ID, err)
	}
	controlInfo, err := r.waitForExecExit(ctx, dockerClient, control.ID)
	if err != nil {
		return err
	}
	if controlInfo.ExitCode != 0 {
		return fmt.Errorf("docker exec termination control %s exited with code %d", control.ID, controlInfo.ExitCode)
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
