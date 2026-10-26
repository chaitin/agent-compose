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
	declaration, err := networkDeclarationFromAgentSpecJSON(agent.SpecJSON)
	if err != nil {
		return nil, fmt.Errorf("agent %q: %w", agent.AgentName, err)
	}
	return declaration, nil
}

// networkDeclarationFromAgentSpecJSON decodes the declaration out of a managed
// agent's canonical spec JSON. An absent policy block or network block is an
// undeclared policy (nil), not an empty default-deny policy. The deprecated
// `sandbox` key is still read so agents persisted before the rename keep their
// declared policy.
func networkDeclarationFromAgentSpecJSON(raw string) (*egress.NetworkDeclaration, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if path, protocol, present := legacyNetworkProtocolIssue([]byte(trimmed)); present {
		return nil, fmt.Errorf("decode agent network declaration: %s carries the removed network entry protocol %q; network entries match any protocol now, so remove it from the project and re-apply the stored spec", path, protocol)
	}
	var spec compose.NormalizedAgentSpec
	if err := json.Unmarshal([]byte(trimmed), &spec); err != nil {
		return nil, fmt.Errorf("decode agent network declaration: %w", err)
	}
	policy := spec.PolicyBlock()
	if policy == nil || policy.Network == nil {
		return nil, nil
	}
	declaration, err := policy.Network.EgressDeclaration()
	if err != nil {
		return nil, fmt.Errorf("decode agent network declaration: %w", err)
	}
	return &declaration, nil
}

// persistedNetworkProtocol mirrors only the parts of a persisted agent spec
// that can carry the removed protocol field. Decoding an old canonical spec
// into the current normalized types drops the field, which would silently widen
// a named-protocol allowance to any protocol; this probe preserves the field
// long enough to refuse the stale declaration.
type persistedNetworkProtocol struct {
	Policy  *persistedPolicyBlockProtocol `json:"policy"`
	Sandbox *persistedPolicyBlockProtocol `json:"sandbox"`
}

type persistedPolicyBlockProtocol struct {
	Network *persistedNetworkBlockProtocol `json:"network"`
}

type persistedNetworkBlockProtocol struct {
	Allow []persistedNetworkEntryProtocol `json:"allow"`
	Deny  []persistedNetworkEntryProtocol `json:"deny"`
}

type persistedNetworkEntryProtocol struct {
	Protocol json.RawMessage `json:"protocol"`
}

// legacyNetworkProtocolIssue reports the first persisted entry whose removed
// protocol value changed the entry's meaning. The current normalized types do
// not have that field, so a plain json.Unmarshal would discard it and accept the
// declaration with ProtocolAny. The caller rejects the returned value, while
// entries whose value is the field's default "any" are skipped and dropped. A
// canonical spec written before the removal carries the field on every entry,
// so scanning must continue past "any" to find a later named value. Both the
// current policy key and its deprecated sandbox spelling are inspected.
func legacyNetworkProtocolIssue(raw []byte) (string, string, bool) {
	var probe persistedNetworkProtocol
	if err := json.Unmarshal(raw, &probe); err != nil {
		// The main decode reports malformed JSON; this probe only answers the
		// narrower compatibility question and must not mask that error.
		return "", "", false
	}
	blocks := []struct {
		name  string
		block *persistedPolicyBlockProtocol
	}{
		{name: "policy", block: probe.Policy},
		{name: "sandbox", block: probe.Sandbox},
	}
	for _, block := range blocks {
		if block.block == nil || block.block.Network == nil {
			continue
		}
		for index, entry := range block.block.Network.Allow {
			protocol, present := persistedProtocolValue(entry.Protocol)
			if !present || persistedProtocolIsAny(protocol) {
				continue
			}
			return fmt.Sprintf("%s.network.allow[%d].protocol", block.name, index), protocol, true
		}
		for index, entry := range block.block.Network.Deny {
			protocol, present := persistedProtocolValue(entry.Protocol)
			if !present || persistedProtocolIsAny(protocol) {
				continue
			}
			return fmt.Sprintf("%s.network.deny[%d].protocol", block.name, index), protocol, true
		}
	}
	return "", "", false
}

func persistedProtocolValue(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		// A non-string value cannot be the removed field's default; returning
		// its raw JSON makes the rejection name what the spec actually holds.
		return string(raw), true
	}
	return value, true
}

// persistedProtocolIsAny matches the value the removed field defaulted to. A
// canonical spec written before the removal always carries the field, so
// rejecting the default would break every upgrade instead of only the entries
// whose meaning changed.
func persistedProtocolIsAny(protocol string) bool {
	return strings.EqualFold(strings.TrimSpace(protocol), string(egress.ProtocolAny))
}
