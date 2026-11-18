package runs

import (
	"context"
	"testing"

	"github.com/chaitin/agent-compose/pkg/compose"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestResolveResourceLimitsFromManagedAgentSpec(t *testing.T) {
	tests := []struct {
		name     string
		specJSON string
		want     *compose.NormalizedSandboxResourcesSpec
	}{
		{name: "declared", specJSON: `{"name":"worker","policy":{"stopped_runtime_policy":"remove","resources":{"cpu_millis":1500,"memory_mib":2048}}}`, want: &compose.NormalizedSandboxResourcesSpec{CPUMillis: 1500, MemoryMiB: 2048}},
		{name: "deprecated sandbox key", specJSON: `{"name":"worker","sandbox":{"stopped_runtime_policy":"remove","resources":{"memory_mib":512}}}`, want: &compose.NormalizedSandboxResourcesSpec{MemoryMiB: 512}},
		{name: "policy without resources", specJSON: `{"name":"worker","policy":{"stopped_runtime_policy":"remove"}}`, want: nil},
		{name: "no policy", specJSON: `{"name":"worker"}`, want: nil},
		{name: "empty resources object", specJSON: `{"name":"worker","policy":{"resources":{}}}`, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &networkDeclarationStoreStub{agents: map[string]domain.ProjectAgentRecord{
				"agent-1": {ID: "agent-1", ProjectID: "project-1", AgentName: "worker", SpecJSON: tt.specJSON},
			}}
			resolver, err := NewSandboxRunTargetResolver(store)
			if err != nil {
				t.Fatalf("NewSandboxRunTargetResolver: %v", err)
			}
			got, err := resolver.ResolveResourceLimits(context.Background(), sandboxWithTags("sandbox-1", domain.SandboxTag{Name: domain.AgentSandboxTagID, Value: "agent-1"}))
			if err != nil {
				t.Fatalf("ResolveResourceLimits: %v", err)
			}
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Fatalf("limits = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestResolveResourceLimitsWithoutManagedAgent(t *testing.T) {
	resolver, err := NewSandboxRunTargetResolver(&networkDeclarationStoreStub{})
	if err != nil {
		t.Fatalf("NewSandboxRunTargetResolver: %v", err)
	}
	got, err := resolver.ResolveResourceLimits(context.Background(), sandboxWithTags("sandbox-1"))
	if err != nil || got != nil {
		t.Fatalf("limits = %+v, %v; want undeclared for a sandbox without an agent", got, err)
	}
}
