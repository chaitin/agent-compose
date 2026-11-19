package driver

import (
	"errors"
	"fmt"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

// mib is one MiB in bytes, the unit drivers that take a byte count need.
const mib = int64(1024 * 1024)

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
	CPUs       uint8
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
	return resources
}

// microVMSandboxResources sizes a microVM: the daemon-wide defaults, with each
// dimension the sandbox declared replacing its default. A microVM's memory is
// the whole guest's memory, so the guest kernel and runtime share the declared
// amount with the workload.
//
// A CPU declaration must be a whole number of vCPUs. Microsandbox only accepts
// an integer. BoxLite accepts a fractional value, but it boots ceil(n) vCPUs
// and caps them with a host cgroup cpu.max quota that it skips, logging only,
// when the host has no delegated CPU controller. That would start the sandbox
// with more CPU than it declared and nothing the daemon can observe, so a
// fractional declaration is refused on every microVM driver.
func microVMSandboxResources(config *appconfig.Config, limits *SandboxResourceLimits) (sandboxResources, error) {
	resources := configuredSandboxResources(config)
	if !limits.Declared() {
		return resources, nil
	}
	if limits.CPUMillis > 0 {
		if limits.CPUMillis%1000 != 0 {
			return sandboxResources{}, fmt.Errorf("%w: declared %d millicpus is not a whole number of vCPUs", ErrSandboxResourceLimitsUnavailable, limits.CPUMillis)
		}
		vcpus := limits.CPUMillis / 1000
		if vcpus > 255 {
			return sandboxResources{}, fmt.Errorf("%w: declared %d vCPUs exceeds the 255 vCPU microVM maximum", ErrSandboxResourceLimitsUnavailable, vcpus)
		}
		resources.CPUs = uint8(vcpus)
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
	case RuntimeDriverBoxlite, RuntimeDriverMicrosandbox:
		_, err = microVMSandboxResources(nil, limits)
	}
	return err
}

// requireReusedRuntimeResourceLimits refuses to reuse an existing runtime whose
// size differs from a declared limit. A runtime keeps the size it was created
// with, so reusing one created before the declaration existed or changed (an
// upgrade, a retained runtime resumed, a declaration edited while the sandbox
// runs) would run the sandbox without the limit it declares. The caller must
// remove the sandbox so the next start creates the runtime with the
// declaration; recreating it here would discard the runtime's writable state.
//
// Only declared dimensions are compared, and a zero actual value means the
// runtime is unlimited in that dimension. runtime names the runtime in the
// error, such as "docker container 3f2a...".
func requireReusedRuntimeResourceLimits(runtime string, limits *SandboxResourceLimits, actual SandboxResourceLimits) error {
	if !limits.Declared() {
		return nil
	}
	var mismatched []string
	if limits.CPUMillis > 0 && actual.CPUMillis != limits.CPUMillis {
		mismatched = append(mismatched, fmt.Sprintf("cpus %s (declared %s)", describeCPUMillis(actual.CPUMillis), describeCPUMillis(limits.CPUMillis)))
	}
	if limits.MemoryMiB > 0 && actual.MemoryMiB != limits.MemoryMiB {
		mismatched = append(mismatched, fmt.Sprintf("memory %s (declared %s)", describeMemoryMiB(actual.MemoryMiB), describeMemoryMiB(limits.MemoryMiB)))
	}
	if len(mismatched) == 0 {
		return nil
	}
	return fmt.Errorf("%w: existing %s was created with %s; remove the sandbox so its runtime is created again with the declared policy.resources", ErrSandboxResourceLimitsUnavailable, runtime, strings.Join(mismatched, ", "))
}

func describeCPUMillis(millis int64) string {
	switch {
	case millis <= 0:
		return "unlimited"
	case millis%1000 == 0:
		return fmt.Sprintf("%d", millis/1000)
	default:
		return fmt.Sprintf("%d.%03d", millis/1000, millis%1000)
	}
}

func describeMemoryMiB(memory uint32) string {
	if memory == 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%dMi", memory)
}
