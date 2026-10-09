package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/compose"
	"github.com/chaitin/agent-compose/pkg/egress"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// ResolveNetworkDeclaration returns the outbound network policy the sandbox's
// agent declared, or nil when the agent declared none. It reads the managed
// agent's own canonical spec, which is the same declaration the engine
// compiles, so a report built from it cannot describe a policy the engine does
// not have.
//
// A sandbox that is not associated with a managed agent has no project
// declaration to report and returns nil, which a caller renders as undeclared
// (D3), never as an enforced policy.
func (r *SandboxRunTargetResolver) ResolveNetworkDeclaration(ctx context.Context, sandbox *domain.Sandbox) (*egress.NetworkDeclaration, error) {
	agentID := sandboxTagValue(sandbox, domain.AgentSandboxTagID)
	if agentID == "" {
		return nil, nil
	}
	agents, err := r.store.ListProjectAgentsByIDs(ctx, []string{agentID})
	if err != nil {
		return nil, fmt.Errorf("resolve network declaration for sandbox %s: %w", sandbox.Summary.ID, err)
	}
	agent, found := agents[agentID]
	if !found {
		return nil, nil
	}
	return networkDeclarationFromAgentSpecJSON(agent.SpecJSON)
}

// networkDeclarationFromAgentSpecJSON decodes the declaration out of a managed
// agent's canonical spec JSON. An absent sandbox block or network block is an
// undeclared policy (nil), not an empty default-deny policy.
func networkDeclarationFromAgentSpecJSON(raw string) (*egress.NetworkDeclaration, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var spec compose.NormalizedAgentSpec
	if err := json.Unmarshal([]byte(trimmed), &spec); err != nil {
		return nil, fmt.Errorf("decode agent network declaration: %w", err)
	}
	if spec.Sandbox == nil || spec.Sandbox.Network == nil {
		return nil, nil
	}
	declaration := spec.Sandbox.Network.EgressDeclaration()
	return &declaration, nil
}
