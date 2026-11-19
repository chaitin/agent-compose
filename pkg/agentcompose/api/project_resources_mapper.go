package api

import (
	"fmt"

	"github.com/chaitin/agent-compose/pkg/compose"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// SandboxResourcesSpecToProto carries a normalized policy.resources block over
// the API in the form the compose file authors it. The daemon renders it back
// to YAML and normalizes it again, so the wire form must reproduce exactly the
// normalized value the client hashed: millicores become a decimal core count
// and MiB become a Mi or Gi quantity, both of which normalize back losslessly.
// An undeclared block, or one that constrains nothing, is nil.
func SandboxResourcesSpecToProto(resources *compose.NormalizedSandboxResourcesSpec) *agentcomposev2.SandboxResourcesSpec {
	if resources == nil || (resources.CPUMillis <= 0 && resources.MemoryMiB == 0) {
		return nil
	}
	spec := &agentcomposev2.SandboxResourcesSpec{}
	if resources.CPUMillis > 0 {
		cpus := float64(resources.CPUMillis) / 1000
		spec.Cpus = &cpus
	}
	if resources.MemoryMiB > 0 {
		spec.Memory = sandboxMemoryQuantity(resources.MemoryMiB)
	}
	return spec
}

func sandboxMemoryQuantity(mib uint32) string {
	if mib%1024 == 0 {
		return fmt.Sprintf("%dGi", mib/1024)
	}
	return fmt.Sprintf("%dMi", mib)
}

// sandboxResourcesYAMLShape renders the wire block as the compose file's
// policy.resources mapping. A nil block renders nothing, so a spec that does
// not declare resources keeps its earlier YAML shape and hash.
func sandboxResourcesYAMLShape(resources *agentcomposev2.SandboxResourcesSpec) map[string]any {
	if resources == nil {
		return nil
	}
	shape := map[string]any{}
	if resources.Cpus != nil {
		shape["cpus"] = resources.GetCpus()
	}
	if memory := resources.GetMemory(); memory != "" {
		shape["memory"] = memory
	}
	// An empty block is kept so the daemon reports the same "declare cpus,
	// memory, or both" error the compose file would, instead of silently
	// dropping a block the client sent.
	return shape
}
