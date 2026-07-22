//go:build unix

package core

import (
	"os/exec"
	"syscall"
)

// cancelProcessTree makes cancellation reach the descendants that inherited the
// command's output pipe, not just the process that was started. Running the
// child in its own process group turns the negated pid into a group signal.
func cancelProcessTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
}
