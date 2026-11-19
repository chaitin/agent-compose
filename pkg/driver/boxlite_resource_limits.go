package driver

import "math"

// boxliteBoxResourceLimits converts the size BoxLite's box metadata records for
// an existing box (cpus as cores, memory in MiB) into the units
// requireReusedRuntimeResourceLimits compares.
func boxliteBoxResourceLimits(cpus float64, memoryMiB int) SandboxResourceLimits {
	actual := SandboxResourceLimits{}
	if cpus > 0 {
		actual.CPUMillis = int64(math.Round(cpus * 1000))
	}
	if memoryMiB > 0 {
		actual.MemoryMiB = uint32(memoryMiB)
	}
	return actual
}
