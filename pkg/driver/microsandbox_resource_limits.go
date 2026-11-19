//go:build linux && cgo && microsandboxcgo

package driver

import (
	"fmt"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"
)

// requireReusedMicrosandboxResourceLimits refuses to reuse a VM whose size
// differs from the declaration (see requireReusedRuntimeResourceLimits). The VM
// keeps the vCPUs and memory it was created with, so a declaration made or
// changed afterwards needs a new VM. A stored configuration that cannot be read
// fails closed for a declaring sandbox: the size it would run with is unknown.
func requireReusedMicrosandboxResourceLimits(handle *microsandbox.SandboxHandle, limits *SandboxResourceLimits) error {
	if !limits.Declared() || handle == nil {
		return nil
	}
	config, err := handle.Config()
	if err != nil {
		return fmt.Errorf("%w: read the size of existing microsandbox VM %s: %w", ErrSandboxResourceLimitsUnavailable, handle.Name(), err)
	}
	actual := SandboxResourceLimits{CPUMillis: int64(config.CPUs) * 1000, MemoryMiB: config.MemoryMiB}
	return requireReusedRuntimeResourceLimits("microsandbox VM "+handle.Name(), limits, actual)
}
