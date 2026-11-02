//go:build linux

package sandboxinit

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

// egressChildFD is the file descriptor init passes to the trampoline through
// os/exec.ExtraFiles. The trampoline sends its notification listener back over
// this socketpair and then closes it before exec.
const egressChildFD = 3

// RunExecChild is the re-exec entry point between init and the workload. Go has
// no fork hook, so the seccomp filter that must apply to the workload's first
// syscall is installed here, after the fork and immediately before execve. The
// filter survives exec and every descendant inherits it.
//
// The negotiated handoff is best effort in audit mode: a guest that cannot
// install the filter still runs the workload (and reports the miss), because
// observation must never become an availability failure. Enforce mode is
// rejected earlier, so it cannot reach this path.
func RunExecChild(args []string) error {
	if len(args) == 0 {
		return errors.New("sandbox init exec child requires a command")
	}
	mode, err := ParseEgressMode(os.Getenv(EgressModeEnvVar))
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	if mode == EgressModeAudit || mode == EgressModeEnforce {
		if err := installEgressFilterForMode(mode); err != nil {
			if mode == EgressModeEnforce {
				return err
			}
			// A filter installed without a reachable supervisor makes every
			// later connect fail with ENOSYS, which is worse than a missed
			// audit. Once the listener exists, the handoff is mandatory.
			var handoffErr *egressHandoffError
			if errors.As(err, &handoffErr) {
				return err
			}
			slog.Warn("sandbox init egress audit is unavailable; running the workload unfiltered", "error", err)
		}
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("resolve sandbox init exec child command %q: %w", args[0], err)
	}
	argv := append([]string(nil), args...)
	if err := syscall.Exec(path, argv, os.Environ()); err != nil {
		return fmt.Errorf("exec sandbox init child %q: %w", path, err)
	}
	return nil
}

// installEgressFilterForMode installs the connect filter, drops the
// capabilities that would let the workload tamper with init or regain a
// mediated syscall surface, and hands the listener to init. It must run on the
// thread that will exec the workload: seccomp filters are per-thread.
func installEgressFilterForMode(mode EgressMode) error {
	sock := os.NewFile(egressChildFD, "sandbox-init-egress")
	if sock == nil {
		return errors.New("sandbox init egress handoff descriptor is missing")
	}
	defer func() { _ = sock.Close() }()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no_new_privs before the egress filter: %w", err)
	}
	listenerFD, err := installEgressFilter()
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(listenerFD) }()
	if err := dropEgressUnsafeCapabilities(); err != nil {
		if mode == EgressModeEnforce {
			return fmt.Errorf("drop workload capabilities before mediated egress: %w", err)
		}
		slog.Warn("sandbox init egress audit could not drop workload capabilities", "error", err)
	}
	if err := sendListener(sock, listenerFD); err != nil {
		return &egressHandoffError{err: err}
	}
	return nil
}

// egressUnsafeCapabilities are removed from the workload's bounding set. A
// sandbox root process that kept CAP_SYS_PTRACE could read init's memory or
// escape the mediated socket path; NET_ADMIN/NET_RAW/BPF/io_uring-adjacent
// capabilities would let it build a second egress path.
var egressUnsafeCapabilities = []uintptr{
	unix.CAP_SYS_PTRACE,
	unix.CAP_SYS_ADMIN,
	unix.CAP_NET_ADMIN,
	unix.CAP_NET_RAW,
	unix.CAP_BPF,
	unix.CAP_PERFMON,
	unix.CAP_SYS_MODULE,
}

// dropEgressUnsafeCapabilities lowers the bounding set, which is what a root
// exec regains its permitted capabilities from. EINVAL means this kernel does
// not know the capability, so there is nothing to drop.
func dropEgressUnsafeCapabilities() error {
	var firstErr error
	for _, capability := range egressUnsafeCapabilities {
		err := unix.Prctl(unix.PR_CAPBSET_DROP, capability, 0, 0, 0)
		if err == nil || errors.Is(err, unix.EINVAL) {
			continue
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// egressHandoffError reports a failure after the filter was installed. The
// trampoline must not exec the workload in that state: its connects would have
// no supervisor and fail with ENOSYS.
type egressHandoffError struct {
	err error
}

func (e *egressHandoffError) Error() string {
	return fmt.Sprintf("hand the sandbox init egress listener to init: %v", e.err)
}

func (e *egressHandoffError) Unwrap() error {
	return e.err
}
