package driver

import (
	"errors"
	"fmt"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

// SandboxResourceLimits is the driver-boundary form of an agent's
// policy.resources declaration. A zero field is undeclared for that dimension.
type SandboxResourceLimits struct {
	CPUMillis int64  `json:"cpu_millis,omitempty"`
	MemoryMiB uint32 `json:"memory_mib,omitempty"`
}

// Declared reports whether the value constrains at least one dimension.
func (l *SandboxResourceLimits) Declared() bool {
	return l != nil && (l.CPUMillis > 0 || l.MemoryMiB > 0)
}

// ErrSandboxResourceLimitsUnavailable reports that a sandbox declared a
// resource limit the selected runtime cannot apply or did not apply. A declared
// limit is a hard requirement, so the create fails instead of starting the
// sandbox without it.
var ErrSandboxResourceLimitsUnavailable = errors.New("sandbox resource limits cannot be enforced")

type sandboxResources struct {
	// CPUs is the whole-vCPU count, the unit every microVM SDK accepts.
	CPUs uint8
	// CPUMillis is the exact CPU size in thousandths of a core. It equals
	// CPUs*1000 unless the sandbox declared a fractional value.
	CPUMillis  int64
	MemoryMiB  uint32
	DiskSizeGB int32
}

func configuredSandboxResources(config *appconfig.Config) sandboxResources {
	resources := sandboxResources{
		CPUs:       appconfig.DefaultSandboxCPUs,
		MemoryMiB:  appconfig.DefaultSandboxMemoryMiB,
		DiskSizeGB: appconfig.DefaultSandboxDiskSizeGB,
	}
	if config != nil {
		if config.SandboxCPUs > 0 {
			resources.CPUs = config.SandboxCPUs
		}
		if config.SandboxMemoryMiB > 0 {
			resources.MemoryMiB = config.SandboxMemoryMiB
		}
		if config.SandboxDiskSizeGB > 0 {
			resources.DiskSizeGB = config.SandboxDiskSizeGB
		}
	}
	resources.CPUMillis = int64(resources.CPUs) * 1000
	return resources
}

// microVMSandboxResources sizes a microVM: the daemon-wide defaults, with each
// dimension the sandbox declared replacing its default. A microVM's memory is
// the whole guest's memory, so the guest kernel and runtime share the declared
// amount with the workload.
//
// wholeCPUs requires the CPU declaration to be a whole number of vCPUs. It is
// set for SDKs that only accept an integer; rounding up would hand the sandbox
// more than it declared, and rounding down could make it unbootable.
func microVMSandboxResources(config *appconfig.Config, limits *SandboxResourceLimits, wholeCPUs bool) (sandboxResources, error) {
	resources := configuredSandboxResources(config)
	if !limits.Declared() {
		return resources, nil
	}
	if limits.CPUMillis > 0 {
		vcpus := (limits.CPUMillis + 999) / 1000
		if vcpus > 255 {
			return sandboxResources{}, fmt.Errorf("%w: declared %d millicpus exceeds the 255 vCPU microVM maximum", ErrSandboxResourceLimitsUnavailable, limits.CPUMillis)
		}
		if wholeCPUs && limits.CPUMillis%1000 != 0 {
			return sandboxResources{}, fmt.Errorf("%w: declared %d millicpus is not a whole number of vCPUs", ErrSandboxResourceLimitsUnavailable, limits.CPUMillis)
		}
		resources.CPUs = uint8(vcpus)
		resources.CPUMillis = limits.CPUMillis
	}
	if limits.MemoryMiB > 0 {
		resources.MemoryMiB = limits.MemoryMiB
	}
	return resources, nil
}

// RequireSandboxResourceLimits fails closed when a sandbox declares a resource
// limit and the driver's capability declaration does not enforce
// resource_limits. It reads the same static facts the capability matrix
// reports, so the start gate and EngineService.GetCapabilities cannot disagree.
// An undeclared limit always passes: it keeps the undeclared behavior.
func RequireSandboxResourceLimits(driver string, limits *SandboxResourceLimits) error {
	if !limits.Declared() {
		return nil
	}
	facts, err := RuntimeCapabilityFactsFor(driver)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSandboxResourceLimitsUnavailable, err)
	}
	enforced, mechanism := false, ""
	for _, dimension := range facts.Dimensions {
		if dimension.Dimension == dimensionResourceLimits {
			enforced, mechanism = dimension.Enforced, dimension.Mechanism
			break
		}
	}
	if !enforced {
		return fmt.Errorf("%w: driver %s does not enforce resource_limits (%s)", ErrSandboxResourceLimitsUnavailable, driver, firstNonEmpty(mechanism, "undeclared"))
	}
	// The microVM SDK bounds are checked here as well as in the sandbox-host,
	// so the daemon refuses the start before it reaches the host and the
	// refusal keeps its classification across the RPC boundary.
	switch resolveRuntimeDriver(driver) {
	case RuntimeDriverBoxlite:
		_, err = microVMSandboxResources(nil, limits, false)
	case RuntimeDriverMicrosandbox:
		_, err = microVMSandboxResources(nil, limits, true)
	}
	return err
}
