package adapters

import (
	"context"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	"github.com/chaitin/agent-compose/pkg/capability"
)

type projectAwareCapabilityProvider struct {
	*capabilities.DynamicProvider
	targets ProjectOctoBusServerResolver
}

// ProjectOctoBusServerResolver selects one project-scoped OctoBus upstream.
type ProjectOctoBusServerResolver interface {
	ResolveOctoBusServer(context.Context, string, string, string) (ResolvedProjectOctoBusServer, error)
}

func (p *projectAwareCapabilityProvider) CapabilityGuideForScope(ctx context.Context, scope capabilities.GuideScope, declaration string) ([]byte, error) {
	parsed, err := capability.ParseCapsetDeclaration(declaration)
	if err != nil {
		return nil, err
	}
	if !parsed.Qualified() {
		return p.DynamicProvider.CapabilityGuideForScope(ctx, scope, parsed.CapsetID)
	}
	target, err := p.targets.ResolveOctoBusServer(ctx, scope.ProjectID, scope.AgentID, declaration)
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
	return &projectAwareCapabilityProvider{DynamicProvider: global, targets: targets}
}
