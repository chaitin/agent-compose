package driver

import (
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

// SandboxInitMode describes how the init process owns the workload.
type SandboxInitMode string

const (
	// SandboxInitModePID1 means init is the container/Pod PID 1.
	SandboxInitModePID1 SandboxInitMode = "pid1"
	// SandboxInitModeGuestSupervisor means the VM's own PID 1 remains in
	// place and init supervises workload processes under it.
	SandboxInitModeGuestSupervisor SandboxInitMode = "guest-supervisor"
	// SandboxInitModeUnsupported means the driver has no init integration.
	SandboxInitModeUnsupported SandboxInitMode = "unsupported"
)

// SandboxInitControl describes how the daemon reaches init.
type SandboxInitControl string

const (
	// SandboxInitControlUnixSocket means the control socket is exposed to the
	// daemon through a shared bind mount or volume.
	SandboxInitControlUnixSocket SandboxInitControl = "unix-socket"
	// SandboxInitControlGuestExec means the daemon runs a small init client
	// through the driver's native exec and bridges the protocol over stdio.
	SandboxInitControlGuestExec SandboxInitControl = "guest-exec"
	// SandboxInitControlVSock means a host supervisor reaches init over a
	// virtio-vsock channel, as in OpenShell's microVM design.
	SandboxInitControlVSock SandboxInitControl = "vsock"
	// SandboxInitControlNone means there is no control transport.
	SandboxInitControlNone SandboxInitControl = "none"
)

// SandboxInitCapability is the driver-neutral description of whether and how
// a driver can run ac-init. The zero value is unsupported: a driver
// must opt in explicitly rather than inherit a claimed capability.
type SandboxInitCapability struct {
	Driver      string
	Mode        SandboxInitMode
	Control     SandboxInitControl
	Supported   bool
	Limitations []string
}

// SandboxInitCapabilityFor returns the Stage-1 capability of one runtime
// driver. Docker uses a host-visible unix socket; Kubernetes and Microsandbox
// bridge the protocol through a guest exec running `ac-init --client`; BoxLite
// keeps its host-visible socket path until its own runtime smoke is available.
func SandboxInitCapabilityFor(driver string) SandboxInitCapability {
	switch resolveRuntimeDriver(driver) {
	case RuntimeDriverDocker:
		return SandboxInitCapability{
			Driver:    RuntimeDriverDocker,
			Mode:      SandboxInitModePID1,
			Control:   SandboxInitControlUnixSocket,
			Supported: true,
		}
	case RuntimeDriverK8s:
		return SandboxInitCapability{
			Driver:    RuntimeDriverK8s,
			Mode:      SandboxInitModePID1,
			Control:   SandboxInitControlGuestExec,
			Supported: true,
		}
	case RuntimeDriverMicrosandbox:
		return SandboxInitCapability{
			Driver:    RuntimeDriverMicrosandbox,
			Mode:      SandboxInitModePID1,
			Control:   SandboxInitControlGuestExec,
			Supported: true,
		}
	case RuntimeDriverBoxlite:
		return SandboxInitCapability{
			Driver:    RuntimeDriverBoxlite,
			Mode:      SandboxInitModePID1,
			Control:   SandboxInitControlUnixSocket,
			Supported: true,
		}
	default:
		return SandboxInitCapability{
			Driver:    strings.TrimSpace(driver),
			Mode:      SandboxInitModeUnsupported,
			Control:   SandboxInitControlNone,
			Supported: false,
			Limitations: []string{
				"unknown runtime driver",
			},
		}
	}
}

// SandboxInitSupported reports whether enabling sandbox init actually changes
// this driver's process model. Callers use it to warn instead of silently
// accepting a flag that has no effect on the selected driver.
func SandboxInitSupported(driver string) bool {
	return SandboxInitCapabilityFor(driver).Supported
}

// Persisted sandbox init states. The value belongs to the runtime, not to the
// daemon's current flag: a sandbox created with init keeps using it after the
// flag is turned off, and a legacy sandbox that is stopped and recreated keeps
// its legacy process model after the flag is turned on.
const (
	SandboxInitStateEnabled  = "enabled"
	SandboxInitStateDisabled = "disabled"
)

// SandboxInitStateFor resolves the process model a driver should use for a
// sandbox. A recorded state wins; an empty state falls back to the daemon flag,
// which is the only answer available for runtimes created before the state was
// persisted.
func SandboxInitStateFor(config *appconfig.Config, vmState VMState) string {
	switch strings.TrimSpace(vmState.SandboxInitState) {
	case SandboxInitStateEnabled:
		return SandboxInitStateEnabled
	case SandboxInitStateDisabled:
		return SandboxInitStateDisabled
	}
	if config != nil && config.SandboxInitEnabled {
		return SandboxInitStateEnabled
	}
	return SandboxInitStateDisabled
}

// SandboxInitEnabledFor reports whether a runtime should use init under the
// recorded or configured state.
func SandboxInitEnabledFor(config *appconfig.Config, vmState VMState) bool {
	return SandboxInitStateFor(config, vmState) == SandboxInitStateEnabled
}
