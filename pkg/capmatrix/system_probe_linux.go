//go:build linux

package capmatrix

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// landlockCreateRulesetVersion is the flag value that asks the Landlock
// syscall for its ABI version instead of creating a ruleset:
// LANDLOCK_CREATE_RULESET_VERSION = 1 << 0.
const landlockCreateRulesetVersion = 1

const (
	mechanismSeccompSyscall       = "seccomp_syscall"
	mechanismSeccompUserNotify    = "seccomp_user_notify"
	mechanismLandlock             = "landlock_lsm"
	mechanismCgroupV2             = "cgroup_v2_hierarchy"
	mechanismUserNamespacesProbe  = "user_namespaces"
	cgroupV2RootPath              = "/sys/fs/cgroup"
	cgroupV2ControllersPath       = "/sys/fs/cgroup/cgroup.controllers"
	maxUserNamespacesSysctlPath   = "/proc/sys/user/max_user_namespaces"
	probeMissingSeccompSyscall    = "the kernel was built without CONFIG_SECCOMP or seccomp is unavailable"
	probeMissingSeccompUserNotify = "seccomp user-space notification (SECCOMP_RET_USER_NOTIF) is unavailable; a mediated egress path cannot intercept syscalls"
)

// probeSystemCapabilities answers the host-mechanism questions by asking the
// kernel directly. Nothing here reads an OS version string.
func probeSystemCapabilities() ([]ObservedCapability, error) {
	return []ObservedCapability{
		probeSeccomp(),
		probeSeccompUserNotify(),
		probeLandlock(),
		probeCgroupV2(),
		probeUserNamespaces(),
	}, nil
}

func probeSeccomp() ObservedCapability {
	mode, err := unix.PrctlRetInt(unix.PR_GET_SECCOMP, 0, 0, 0, 0)
	if err == nil {
		return ObservedCapability{
			Dimension: ObservedSeccomp,
			Enforced:  true,
			State:     StateEnforced,
			Mechanism: mechanismSeccompSyscall,
			Observed:  "prctl(PR_GET_SECCOMP) answered mode " + strconv.Itoa(mode) + ", so the kernel's seccomp syscall is present",
			Source:    SourceMeasured,
		}
	}
	return ObservedCapability{
		Dimension: ObservedSeccomp,
		State:     StateUnsupported,
		Mechanism: ReasonUnsupported,
		Observed:  "prctl(PR_GET_SECCOMP) failed with " + err.Error() + "; the kernel cannot enforce seccomp",
		Missing:   probeMissingSeccompSyscall,
		Source:    SourceMeasured,
	}
}

func probeSeccompUserNotify() ObservedCapability {
	action := uint32(unix.SECCOMP_RET_USER_NOTIF)
	available, detail := seccompGetActionAvailable(action)
	if !available {
		return ObservedCapability{
			Dimension: ObservedSeccompUserNotify,
			State:     StateUnsupported,
			Mechanism: ReasonUnsupported,
			Observed:  "seccomp(SECCOMP_GET_ACTION_AVAIL, SECCOMP_RET_USER_NOTIF) failed with " + detail,
			Missing:   probeMissingSeccompUserNotify,
			Source:    SourceMeasured,
		}
	}
	return ObservedCapability{
		Dimension: ObservedSeccompUserNotify,
		Enforced:  true,
		State:     StateEnforced,
		Mechanism: mechanismSeccompUserNotify,
		Observed:  "seccomp(SECCOMP_GET_ACTION_AVAIL, SECCOMP_RET_USER_NOTIF) reported the action available",
		Source:    SourceMeasured,
	}
}

// seccompGetActionAvailable performs the pure-kernel availability query:
// seccomp(SECCOMP_GET_ACTION_AVAIL, 0, &action). It never installs a filter.
func seccompGetActionAvailable(action uint32) (bool, string) {
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, uintptr(unix.SECCOMP_GET_ACTION_AVAIL), 0, uintptr(unsafe.Pointer(&action)))
	runtime.KeepAlive(action)
	if errno != 0 {
		return false, errno.Error()
	}
	return true, ""
}

func probeLandlock() ObservedCapability {
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, uintptr(landlockCreateRulesetVersion))
	runtime.KeepAlive(landlockCreateRulesetVersion)
	if errno != 0 {
		reason := "landlock_create_ruleset(LANDLOCK_CREATE_RULESET_VERSION) failed with " + errno.Error()
		missing := "the kernel does not provide Landlock; filesystem confinement is unavailable"
		if errors.Is(errno, unix.EOPNOTSUPP) {
			missing = "Landlock is compiled in but disabled (the landlock LSM is not enabled at boot)"
		}
		return ObservedCapability{
			Dimension: ObservedLandlock,
			State:     StateUnsupported,
			Mechanism: ReasonUnsupported,
			Observed:  reason,
			Missing:   missing,
			Source:    SourceMeasured,
		}
	}
	version := int(abi)
	return ObservedCapability{
		Dimension: ObservedLandlock,
		Enforced:  true,
		State:     StateEnforced,
		Mechanism: mechanismLandlock,
		Observed:  "landlock_create_ruleset(LANDLOCK_CREATE_RULESET_VERSION) reported ABI " + strconv.Itoa(version),
		Source:    SourceMeasured,
	}
}

func probeCgroupV2() ObservedCapability {
	if _, err := os.Stat(cgroupV2ControllersPath); err != nil {
		return ObservedCapability{
			Dimension: ObservedCgroupV2,
			State:     StateUnsupported,
			Mechanism: ReasonUnsupported,
			Observed:  "stat(" + cgroupV2ControllersPath + ") failed with " + err.Error() + "; the cgroup v2 unified hierarchy is not mounted",
			Missing:   "the host must mount the cgroup v2 unified hierarchy at " + cgroupV2RootPath,
			Source:    SourceMeasured,
		}
	}
	if err := unix.Access(cgroupV2RootPath, unix.W_OK); err != nil {
		return ObservedCapability{
			Dimension:     ObservedCgroupV2,
			State:         StateDegraded,
			Mechanism:     mechanismCgroupV2,
			Preconditions: []string{"the daemon must be able to write to the cgroup v2 hierarchy root " + cgroupV2RootPath},
			Observed:      "the cgroup v2 hierarchy is mounted but access(" + cgroupV2RootPath + ", W_OK) failed with " + err.Error(),
			Missing:       "the cgroup v2 hierarchy is read-only for the daemon; per-sandbox resource limits cannot be written",
			Source:        SourceMeasured,
		}
	}
	return ObservedCapability{
		Dimension: ObservedCgroupV2,
		Enforced:  true,
		State:     StateEnforced,
		Mechanism: mechanismCgroupV2,
		Observed:  "the cgroup v2 hierarchy is mounted at " + cgroupV2RootPath + " and is writable by the daemon",
		Source:    SourceMeasured,
	}
}

// probeUserNamespaces asks the kernel for its own configured limit on user
// namespaces. The engine deliberately does not call unshare(CLONE_NEWUSER) in
// the daemon process: that would move the daemon's own threads into a new user
// namespace and cannot be undone. A disposable child would be needed for a
// "do it" probe, and the kernel's limit is the availability answer without
// mutating the daemon.
func probeUserNamespaces() ObservedCapability {
	raw, err := os.ReadFile(maxUserNamespacesSysctlPath)
	if err != nil {
		return ObservedCapability{
			Dimension: ObservedUserNamespaces,
			State:     StateUnsupported,
			Mechanism: ReasonUnsupported,
			Observed:  "read(" + maxUserNamespacesSysctlPath + ") failed with " + err.Error() + "; user-namespace availability cannot be established",
			Missing:   "the kernel does not expose " + maxUserNamespacesSysctlPath,
			Source:    SourceMeasured,
		}
	}
	limit, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if parseErr != nil || limit <= 0 {
		return ObservedCapability{
			Dimension: ObservedUserNamespaces,
			State:     StateUnsupported,
			Mechanism: ReasonUnsupported,
			Observed:  "the kernel reports user.max_user_namespaces=" + strings.TrimSpace(string(raw)) + ", so unprivileged user namespaces are disabled",
			Missing:   "the kernel's user.max_user_namespaces limit must be greater than zero",
			Source:    SourceMeasured,
		}
	}
	return ObservedCapability{
		Dimension: ObservedUserNamespaces,
		Enforced:  true,
		State:     StateEnforced,
		Mechanism: mechanismUserNamespacesProbe,
		Observed:  "the kernel reports user.max_user_namespaces=" + strconv.Itoa(limit),
		Source:    SourceMeasured,
	}
}
