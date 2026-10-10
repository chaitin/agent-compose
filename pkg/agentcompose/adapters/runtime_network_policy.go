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
// optional: a composition without one cannot know a declaration exists, so the
// driver-boundary policy stays nil (D3) instead of being invented.
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

// policy returns the driver-boundary policy for session, or nil when the
// sandbox declared no network policy (D3: undeclared is not deny). A declared
// policy always yields a non-nil policy, permissive ones included, so a caller
// can tell "undeclared" from "declared allow" rather than guessing.
func (d driverSandboxNetworkPolicy) policy(ctx context.Context, session *domain.Sandbox) (*driverpkg.SandboxNetworkPolicy, error) {
	if d.declarations == nil || session == nil {
		return nil, nil
	}
	declaration, err := d.declarations.ResolveNetworkDeclaration(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox network declaration for %s: %w", session.Summary.ID, err)
	}
	if declaration == nil {
		return nil, nil
	}
	// The engine-owned endpoints come from the same addresses the engine hands
	// this sandbox, never a daemon-global or invented endpoint.
	telemetryEndpoint := ""
	if d.config != nil {
		telemetryEndpoint = d.config.AgentTelemetry.Endpoint
	}
	engineEndpoints, err := egress.EngineEndpoints(llms.GuestRuntimeBaseURL(d.config, session), telemetryEndpoint)
	if err != nil {
		return nil, fmt.Errorf("derive sandbox engine network endpoints for %s: %w", session.Summary.ID, err)
	}
	// The compose declaration carries no deny domains yet, so the engine
	// supplies none rather than inventing an exemption list.
	policy, err := driverpkg.SandboxNetworkPolicyFromDeclaration(declaration, engineEndpoints, nil)
	if err != nil {
		return nil, fmt.Errorf("compile sandbox network policy for %s: %w", session.Summary.ID, err)
	}
	return &policy, nil
}
