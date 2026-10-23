package adapters

import (
	"context"
	"fmt"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/egress"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// SandboxNetworkDeclarationResolver resolves the outbound network policy a
// sandbox's project agent declared, or nil when it declared none. It is
// optional: a composition without one cannot know an agent declaration exists,
// so only the daemon-wide policy applies (D3), instead of inventing one.
type SandboxNetworkDeclarationResolver interface {
	ResolveNetworkDeclaration(context.Context, *domain.Sandbox) (*egress.NetworkDeclaration, error)
}

// driverSandboxNetworkPolicy derives the driver-boundary network policy for one
// sandbox. It is the single seam that turns the declaration layer's normalized
// policy plus the engine-owned endpoints into the value every driver maps into
// its own enforcement mechanism, so no driver parses compose and no second
// policy representation exists.
type driverSandboxNetworkPolicy struct {
	config       *appconfig.Config
	declarations SandboxNetworkDeclarationResolver
}

// inputs resolves the declaration a sandbox's project made, layers it under the
// daemon-wide policy, and derives the engine-owned endpoints that must stay
// reachable. It is the single place both consumers start from — the
// driver-boundary policy and the policy the engine's forward proxy decides with —
// so the two can never disagree about the effective policy or which endpoints
// the engine owns. A nil result means neither layer declared a network policy
// (D3).
func (d driverSandboxNetworkPolicy) inputs(ctx context.Context, session *domain.Sandbox) (*egress.NetworkDeclaration, []egress.EngineEndpoint, error) {
	if session == nil {
		return nil, nil, nil
	}
	var declaration *egress.NetworkDeclaration
	if d.declarations != nil {
		resolved, err := d.declarations.ResolveNetworkDeclaration(ctx, session)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve sandbox network declaration for %s: %w", session.Summary.ID, err)
		}
		declaration = resolved
	}
	var global *egress.NetworkDeclaration
	if d.config != nil {
		global = d.config.GlobalNetworkPolicy
	}
	if declaration == nil && global == nil {
		return nil, nil, nil
	}
	declaration, err := egress.LayerNetworkDeclarations(global, declaration)
	if err != nil {
		return nil, nil, fmt.Errorf("combine the daemon and agent network policies for %s: %w", session.Summary.ID, err)
	}
	// The engine-owned endpoints come from the same addresses the engine hands
	// this sandbox, never a daemon-global or invented endpoint.
	telemetryEndpoint := ""
	if d.config != nil {
		telemetryEndpoint = d.config.AgentTelemetry.Endpoint
	}
	engineEndpoints, err := egress.EngineEndpoints(llms.GuestRuntimeBaseURL(d.config, session), telemetryEndpoint)
	if err != nil {
		return nil, nil, fmt.Errorf("derive sandbox engine network endpoints for %s: %w", session.Summary.ID, err)
	}
	return declaration, engineEndpoints, nil
}

// effectivePolicy compiles the policy this sandbox's declaration resolves to:
// the same compilation the forward proxy decides with, so a generation a
// decision records and the generation recorded for a runtime are comparable.
// An undeclared sandbox resolves to the pass-through policy (D3).
func (d driverSandboxNetworkPolicy) effectivePolicy(ctx context.Context, session *domain.Sandbox) (egress.Policy, error) {
	declaration, engineEndpoints, err := d.inputs(ctx, session)
	if err != nil {
		return egress.Policy{}, err
	}
	policy, err := egress.EffectiveNetworkPolicy(declaration, engineEndpoints)
	if err != nil {
		return egress.Policy{}, fmt.Errorf("compile the egress policy for sandbox %s: %w", session.Summary.ID, err)
	}
	return policy, nil
}

// resolve returns both the driver-boundary policy and the compiled policy the
// engine decides with, from one declaration read. A caller that needs both must
// not resolve twice: an apply landing between the two reads would attach one
// declaration to the driver and record another declaration's generation.
//
// The driver policy is nil when neither the sandbox nor the daemon declared a
// network policy (D3: undeclared is not deny), while an effective policy always
// yields a non-nil one, permissive ones included, so a caller can tell
// "undeclared" from "declared allow". The compiled policy is the pass-through
// policy in the undeclared case.
func (d driverSandboxNetworkPolicy) resolve(ctx context.Context, session *domain.Sandbox) (*driverpkg.SandboxNetworkPolicy, egress.Policy, error) {
	declaration, engineEndpoints, err := d.inputs(ctx, session)
	if err != nil {
		return nil, egress.Policy{}, err
	}
	effective, err := egress.EffectiveNetworkPolicy(declaration, engineEndpoints)
	if err != nil {
		return nil, egress.Policy{}, fmt.Errorf("compile the egress policy for sandbox %s: %w", session.Summary.ID, err)
	}
	if declaration == nil {
		return nil, effective, nil
	}
	policy, err := driverpkg.SandboxNetworkPolicyFromDeclaration(declaration, engineEndpoints, nil)
	if err != nil {
		return nil, egress.Policy{}, fmt.Errorf("compile sandbox network policy for %s: %w", session.Summary.ID, err)
	}
	return &policy, effective, nil
}
