package driver

import "strings"

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
// driver. Docker is the only driver wired end to end today. The other drivers
// have a documented integration path but are deliberately reported as
// unsupported until their launch/control transport is implemented and tested:
// a feature flag must not silently change their process model.
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
			Supported: false,
			Limitations: []string{
				"stage 1 has no Pod command/init-client integration",
				"the control socket is not host-visible; a guest-exec bridge is required",
			},
		}
	case RuntimeDriverMicrosandbox:
		return SandboxInitCapability{
			Driver:    RuntimeDriverMicrosandbox,
			Mode:      SandboxInitModePID1,
			Control:   SandboxInitControlVSock,
			Supported: false,
			Limitations: []string{
				"stage 1 has not wired microsandbox.WithInit or the control bind mount",
				"the SDK agent/exec behavior with a custom PID 1 is unverified",
			},
		}
	case RuntimeDriverBoxlite:
		return SandboxInitCapability{
			Driver:    RuntimeDriverBoxlite,
			Mode:      SandboxInitModePID1,
			Control:   SandboxInitControlVSock,
			Supported: false,
			Limitations: []string{
				"stage 1 has not wired the BoxLite entrypoint or control bind mount",
				"the host bind-mount/control transport is unverified on real KVM",
			},
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
