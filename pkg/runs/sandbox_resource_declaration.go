package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/compose"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// ResolveResourceLimits returns the policy.resources block the sandbox's agent
// declared, or nil when it declared none. Like ResolveNetworkDeclaration it
// reads the managed agent's canonical spec, so the limit a driver applies is
// the one the project declares now.
//
// A sandbox that is not associated with a managed agent has no declaration and
// returns nil, which keeps the undeclared behavior.
func (r *SandboxRunTargetResolver) ResolveResourceLimits(ctx context.Context, sandbox *domain.Sandbox) (*compose.NormalizedSandboxResourcesSpec, error) {
	agentID := sandboxTagValue(sandbox, domain.AgentSandboxTagID)
	if agentID == "" {
		return nil, nil
	}
	agents, err := r.store.ListProjectAgentsByIDs(ctx, []string{agentID})
	if err != nil {
		return nil, fmt.Errorf("resolve resource limits for sandbox %s: %w", sandbox.Summary.ID, err)
	}
	agent, found := agents[agentID]
	if !found {
		return nil, nil
	}
	resources, err := resourceLimitsFromAgentSpecJSON(agent.SpecJSON)
	if err != nil {
		return nil, fmt.Errorf("agent %q: %w", agent.AgentName, err)
	}
	return resources, nil
}

// resourceLimitsFromAgentSpecJSON decodes the declaration out of a managed
// agent's canonical spec JSON. An absent policy block or resources block is
// undeclared (nil). The deprecated `sandbox` key is still read.
func resourceLimitsFromAgentSpecJSON(raw string) (*compose.NormalizedSandboxResourcesSpec, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var spec compose.NormalizedAgentSpec
	if err := json.Unmarshal([]byte(trimmed), &spec); err != nil {
		return nil, fmt.Errorf("decode agent resource limits: %w", err)
	}
	policy := spec.PolicyBlock()
	if policy == nil || policy.Resources == nil {
		return nil, nil
	}
	resources := *policy.Resources
	if resources.CPUMillis <= 0 && resources.MemoryMiB == 0 {
		return nil, nil
	}
	return &resources, nil
}
