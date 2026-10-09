package driver

import (
	"fmt"

	"github.com/chaitin/agent-compose/pkg/egress"
)

// This file is the bridge between the driver-boundary network policy and the
// engine's single allow/deny decision model. It deliberately delegates
// compilation and matching to pkg/egress instead of growing a second decision
// engine in the driver layer.

// SandboxNetworkEgressPolicy compiles a driver-boundary policy into the engine
// egress model through the declaration layer's compiler, so endpoint matching,
// engine-endpoint precedence, and the generation stamped on every decision are
// exactly the ones the rest of the engine uses.
func SandboxNetworkEgressPolicy(policy SandboxNetworkPolicy) (egress.Policy, error) {
	if err := policy.Validate(); err != nil {
		return egress.Policy{}, err
	}
	normalizedDefault, err := normalizeSandboxNetworkDefault(policy.Default)
	if err != nil {
		return egress.Policy{}, err
	}
	declaration := egress.NetworkDeclaration{Default: normalizedDefault}
	for _, endpoint := range policy.Allow {
		declaration.Allow = append(declaration.Allow, egress.AllowEntry(endpoint))
	}
	return egress.EffectiveNetworkPolicy(&declaration, policy.EngineEndpoints)
}

// DecideSandboxNetworkEgress evaluates one destination through the engine
// decision model, so a driver or a test never hand-rolls allow/deny logic.
func DecideSandboxNetworkEgress(policy SandboxNetworkPolicy, endpoint egress.Endpoint) (egress.Result, error) {
	compiled, err := SandboxNetworkEgressPolicy(policy)
	if err != nil {
		return egress.Result{}, err
	}
	normalized, err := egress.NewEndpoint(endpoint.Host, endpoint.Port, endpoint.Protocol)
	if err != nil {
		return egress.Result{}, fmt.Errorf("sandbox network endpoint: %w", err)
	}
	return egress.Decide(compiled, egress.NetworkRequest("", normalized)), nil
}
