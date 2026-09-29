//go:build linux && cgo && boxlitecgo

package driver

import (
	"context"
	"fmt"
	"strings"
)

// SignalGuestRuntime sends a signal through a short BoxLite control exec. It uses
// a separately attached box handle so signaling can proceed concurrently with
// the execution being stopped.
func (r *cgoSandboxRuntime) SignalGuestRuntime(ctx context.Context, _ *Sandbox, vmState VMState, executionID string, signal RuntimeSignal) error {
	command, err := guestRuntimeSignalCommand(executionID, signal)
	if err != nil {
		return err
	}
	if strings.TrimSpace(vmState.BoxID) == "" {
		return ErrGuestRuntimeGone
	}
	box, err := r.getBox(ctx, vmState.BoxID)
	if err != nil {
		return fmt.Errorf("attach BoxLite guest runtime signal control: %w", err)
	}
	defer box.free()
	info, err := r.boxInfo(ctx, box)
	if err != nil {
		return err
	}
	if !info.State.Running {
		return ErrGuestRuntimeGone
	}
	result, err := r.executeBox(ctx, box, ExecSpec{Command: command[0], Args: command[1:], Cwd: "/"}, nil)
	if err != nil {
		return fmt.Errorf("run BoxLite guest runtime signal control: %w", err)
	}
	return guestRuntimeSignalExitError(result.ExitCode)
}
