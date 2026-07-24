package adapters

import (
	"context"

	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/capability"
)

type projectAwareCapabilityProvider struct {
	capabilities.Provider
	targets ProjectOctoBusServerResolver
}

// ProjectOctoBusServerResolver selects one project-scoped OctoBus upstream.
type ProjectOctoBusServerResolver interface {
	ResolveOctoBusServer(context.Context, string, string, string) (ResolvedProjectOctoBusServer, error)
}

func (p *projectAwareCapabilityProvider) CapabilityGuideForScope(ctx context.Context, scope capabilities.GuideScope, declaration string) ([]byte, error) {
	target, err := p.targets.ResolveOctoBusServer(ctx, scope.ManagedProjectID, scope.ManagedAgentID, declaration)
	if err != nil {
		return nil, err
	}
	guide, err := capability.NewClient(capability.Config{Addr: target.Server.URL, Token: target.Server.Token}).CatalogMarkdown(ctx, target.CapsetID)
	if err != nil {
		return nil, err
	}
	return capabilities.QualifyCapabilityGuide(guide, declaration, target.CapsetID), nil
}

func NewCapabilityProvider(source capabilities.GatewaySource, targets ProjectOctoBusServerResolver, proxyTarget string) capabilities.Provider {
	global := capabilities.NewDynamicProvider(source, proxyTarget)
	return &projectAwareCapabilityProvider{Provider: global, targets: targets}
}
