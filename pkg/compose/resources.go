package compose

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// SandboxResourcesSpec is the authored resource ceiling of one agent's
// sandboxes. Each field is optional on its own, but a declared block must name
// at least one of them: an empty block would read as a constraint while
// constraining nothing.
type SandboxResourcesSpec struct {
	// CPUs is the CPU ceiling in cores. Fractions are allowed down to a
	// thousandth of a core (0.5, 1.25), except on Microsandbox, whose VM size is
	// a whole number of vCPUs.
	CPUs *float64 `yaml:"cpus,omitempty" json:"cpus,omitempty"`
	// Memory is the memory ceiling as a binary quantity with a unit, such as
	// "512Mi" or "4Gi". A bare number is rejected because "4096" does not say
	// whether it means bytes or MiB.
	Memory string `yaml:"memory,omitempty" json:"memory,omitempty"`
}

// NormalizedSandboxResourcesSpec is the validated declaration in the units the
// drivers consume. A zero field is undeclared, so it keeps that dimension's
// undeclared behavior; omitempty keeps it out of the canonical JSON.
type NormalizedSandboxResourcesSpec struct {
	CPUMillis int64  `yaml:"cpu_millis,omitempty" json:"cpu_millis,omitempty"`
	MemoryMiB uint32 `yaml:"memory_mib,omitempty" json:"memory_mib,omitempty"`
}

const (
	// maxSandboxCPUMillis bounds a declaration to a plausible single host.
	// The daemon's SANDBOX_MAX_CPUS ceiling is the deployment's real limit;
	// this bound only rejects values no driver could honor.
	maxSandboxCPUMillis = 1024 * 1000
	// minSandboxMemoryMiB is the smallest memory ceiling accepted. A smaller
	// sandbox cannot start the guest runtime, so the declaration is rejected
	// rather than producing a sandbox that is OOM-killed while booting.
	minSandboxMemoryMiB = 64
	// maxSandboxMemoryMiB is 1 TiB, the largest value the MiB field carries
	// comfortably on every driver.
	maxSandboxMemoryMiB = 1024 * 1024
)

// NormalizeSandboxResourcesSpec validates a declared resource block and
// converts it to driver units. A nil block is undeclared and returns nil.
func NormalizeSandboxResourcesSpec(path string, resources *SandboxResourcesSpec) (*NormalizedSandboxResourcesSpec, error) {
	if resources == nil {
		return nil, nil
	}
	memory := strings.TrimSpace(resources.Memory)
	if resources.CPUs == nil && memory == "" {
		return nil, &ValidationError{Path: path, Message: "declare cpus, memory, or both; an empty resources block constrains nothing"}
	}
	normalized := &NormalizedSandboxResourcesSpec{}
	if resources.CPUs != nil {
		millis, err := sandboxCPUMillis(*resources.CPUs)
		if err != nil {
			return nil, &ValidationError{Path: path + ".cpus", Message: err.Error()}
		}
		normalized.CPUMillis = millis
	}
	if memory != "" {
		mib, err := parseSandboxMemoryMiB(memory)
		if err != nil {
			return nil, &ValidationError{Path: path + ".memory", Message: err.Error()}
		}
		normalized.MemoryMiB = mib
	}
	return normalized, nil
}

func sandboxCPUMillis(cpus float64) (int64, error) {
	if math.IsNaN(cpus) || math.IsInf(cpus, 0) || cpus <= 0 {
		return 0, fmt.Errorf("cpus must be a positive number of cores, got %v", cpus)
	}
	scaled := cpus * 1000
	millis := math.Round(scaled)
	if math.Abs(scaled-millis) > 1e-6 {
		return 0, fmt.Errorf("cpus %v is finer than a thousandth of a core", cpus)
	}
	if millis > maxSandboxCPUMillis {
		return 0, fmt.Errorf("cpus %v exceeds %d cores", cpus, maxSandboxCPUMillis/1000)
	}
	return int64(millis), nil
}

func parseSandboxMemoryMiB(value string) (uint32, error) {
	units := []struct {
		suffix string
		mib    uint64
	}{
		{suffix: "Gi", mib: 1024},
		{suffix: "Mi", mib: 1},
	}
	for _, unit := range units {
		number, ok := strings.CutSuffix(value, unit.suffix)
		if !ok {
			continue
		}
		amount, err := strconv.ParseUint(strings.TrimSpace(number), 10, 32)
		if err != nil {
			return 0, fmt.Errorf("memory %q must be a whole number followed by Mi or Gi", value)
		}
		mib := amount * unit.mib
		if mib < minSandboxMemoryMiB {
			return 0, fmt.Errorf("memory %q is below the %dMi minimum", value, minSandboxMemoryMiB)
		}
		if mib > maxSandboxMemoryMiB {
			return 0, fmt.Errorf("memory %q exceeds %dGi", value, maxSandboxMemoryMiB/1024)
		}
		return uint32(mib), nil
	}
	return 0, fmt.Errorf("memory %q needs a binary unit, for example 512Mi or 4Gi", value)
}

// validateSandboxResourcesForDriver rejects a declaration the agent's driver
// can never honor, so the project fails at apply time rather than at the first
// sandbox start. Microsandbox sizes its VM in whole vCPUs; rounding a
// fractional declaration up would hand the sandbox more CPU than it declared.
func validateSandboxResourcesForDriver(path, driver string, resources *NormalizedSandboxResourcesSpec) error {
	if resources == nil || resources.CPUMillis == 0 {
		return nil
	}
	if driver == DriverMicrosandbox && resources.CPUMillis%1000 != 0 {
		return &ValidationError{Path: path + ".cpus", Message: "microsandbox sizes its VM in whole vCPUs; declare an integer number of cpus"}
	}
	return nil
}
