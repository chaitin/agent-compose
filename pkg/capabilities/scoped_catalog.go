package capabilities

import (
	"context"
	"fmt"

	"github.com/chaitin/agent-compose/pkg/capability"
)

// CatalogForScope reads the structured directory with the guide's trusted scope.
func (p *DynamicProvider) CatalogForScope(ctx context.Context, scope GuideScope, declaration string) (capability.Catalog, error) {
	parsed, err := capability.ParseCapsetDeclaration(declaration)
	if err != nil {
		return capability.Catalog{}, fmt.Errorf("invalid capset declaration: %w", err)
	}
	if parsed.Qualified() {
		return capability.Catalog{}, errProjectOctoBusNotConfigured
	}
	client, ok := p.clientWithScope(ctx, capability.Scope{ProjectID: scope.ProjectID, Username: scope.Username})
	if !ok {
		return capability.Catalog{}, capability.ErrNotConfigured
	}
	return client.Catalog(ctx, parsed.CapsetID)
}
