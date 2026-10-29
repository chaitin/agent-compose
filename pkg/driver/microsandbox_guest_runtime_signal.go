//go:build linux && cgo && microsandboxcgo

package driver

import (
	"context"
	"fmt"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

// SignalGuestRuntime sends a signal through a short Microsandbox control exec. It
// intentionally does not retain or manage the execution's process tree.
func (r *microsandboxRuntime) SignalGuestRuntime(ctx context.Context, session *Sandbox, vmState VMState, executionID string, signal RuntimeSignal) error {
	command, err := guestRuntimeSignalCommand(executionID, signal)
	if err != nil {
		return err
	}
	if r.sandboxInitConfigured(session) {
		_, err := runSandboxInitExec(ctx, r.sandboxInitClient(session), session.Summary.ID, "/", ExecSpec{Command: command[0], Args: command[1:]}, nil)
		return err
	}
	if err := r.ensureReady(ctx); err != nil {
		return err
	}
	name := r.sandboxName(session, vmState)
	sandbox, err := r.connectSandbox(ctx, session, vmState, false)
	if err != nil {
		return fmt.Errorf("connect Microsandbox guest runtime signal control: %w", err)
	}
	defer r.releaseSandboxHandle(name, sandbox)
	output, err := sandbox.Exec(ctx, command[0], command[1:], microsandbox.WithExecCwd("/"))
	if err != nil {
		return fmt.Errorf("run Microsandbox guest runtime signal control: %w", err)
	}
	if output == nil {
		return fmt.Errorf("run Microsandbox guest runtime signal control: empty result")
	}
	return guestRuntimeSignalExitError(output.ExitCode())
}
