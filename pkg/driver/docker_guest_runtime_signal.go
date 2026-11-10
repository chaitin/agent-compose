package driver

import (
	"context"
	"fmt"

	containerapi "github.com/docker/docker/api/types/container"
)

// SignalGuestRuntime sends a signal to one ready guest runtime through a Docker
// control exec that is independent of the execution being stopped.
func (r *dockerRuntime) SignalGuestRuntime(ctx context.Context, sandbox *Sandbox, vmState VMState, executionID string, signal RuntimeSignal) error {
	command, err := guestRuntimeSignalCommand(executionID, signal)
	if err != nil {
		return err
	}
	dockerClient, err := r.newClient()
	if err != nil {
		return err
	}
	defer func() { _ = dockerClient.Close() }()
	containerInfo, ok, err := r.findContainer(ctx, dockerClient, sandbox, vmState)
	if err != nil {
		return err
	}
	if !ok || containerInfo.State == nil || !containerInfo.State.Running {
		return ErrGuestRuntimeGone
	}
	if dockerSandboxUsesInit(containerInfo) {
		result, err := r.initExecWithStream(ctx, sandbox, ExecSpec{
			Command: command[0],
			Args:    append([]string(nil), command[1:]...),
			Cwd:     "/",
		}, nil)
		if err != nil {
			return err
		}
		return guestRuntimeSignalExitError(result.ExitCode)
	}
	control, err := dockerClient.ContainerExecCreate(ctx, containerInfo.ID, containerapi.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          command,
		User:         "0",
		WorkingDir:   "/",
	})
	if err != nil {
		return fmt.Errorf("create Docker guest runtime signal control: %w", err)
	}
	attach, err := dockerClient.ContainerExecAttach(ctx, control.ID, containerapi.ExecAttachOptions{})
	if err != nil {
		return fmt.Errorf("start Docker guest runtime signal control %s: %w", control.ID, err)
	}
	if err := drainDockerControlExec(attach); err != nil {
		return fmt.Errorf("read Docker guest runtime signal control %s: %w", control.ID, err)
	}
	controlInfo, err := r.waitForExecExit(ctx, dockerClient, control.ID)
	if err != nil {
		return fmt.Errorf("wait for Docker guest runtime signal control %s: %w", control.ID, err)
	}
	return guestRuntimeSignalExitError(controlInfo.ExitCode)
}
