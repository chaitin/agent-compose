package capability

import (
	"context"
	"strings"

	clientcap "agent-compose/pkg/capability"
)

type Provider interface {
	Status(context.Context) clientcap.Status
	ListCapsets(context.Context) ([]clientcap.Capset, error)
	Catalog(context.Context, string) (clientcap.Catalog, error)
	CapabilityGuide(ctx context.Context, capsetID string) ([]byte, error)
	ProxyTarget() string
}

// GatewayProvider reads the OctoBus connection from source on every call, so
// page edits take effect without a restart. An empty addr means disabled.
type GatewayProvider struct {
	source      GatewaySource
	proxyTarget string
}

func NewGatewayProvider(source GatewaySource, proxyTarget string) *GatewayProvider {
	return &GatewayProvider{
		source:      source,
		proxyTarget: strings.TrimSpace(proxyTarget),
	}
}

// client builds an OctoBus client from the current settings. ok is false when
// the gateway is not configured (empty addr) or settings are unreadable.
func (p *GatewayProvider) client(ctx context.Context) (*clientcap.Client, bool) {
	if p == nil || p.source == nil {
		return nil, false
	}
	settings, err := p.source.GetCapabilityGateway(ctx)
	settings = settings.Trimmed()
	if err != nil || !settings.Configured() {
		return nil, false
	}
	return clientcap.NewClient(clientcap.Config{Addr: settings.Addr, Token: settings.Token}), true
}

func (p *GatewayProvider) Status(ctx context.Context) clientcap.Status {
	client, ok := p.client(ctx)
	if !ok {
		return clientcap.Status{Configured: false, OK: false, Status: DefaultNotConfiguredStatus}
	}
	return client.Status(ctx)
}

func (p *GatewayProvider) ListCapsets(ctx context.Context) ([]clientcap.Capset, error) {
	client, ok := p.client(ctx)
	if !ok {
		return []clientcap.Capset{}, nil
	}
	return client.ListCapsets(ctx)
}

func (p *GatewayProvider) Catalog(ctx context.Context, capsetID string) (clientcap.Catalog, error) {
	client, ok := p.client(ctx)
	if !ok {
		return clientcap.Catalog{}, clientcap.ErrNotConfigured
	}
	return client.Catalog(ctx, capsetID)
}

func (p *GatewayProvider) CapabilityGuide(ctx context.Context, capsetID string) ([]byte, error) {
	client, ok := p.client(ctx)
	if !ok {
		return nil, clientcap.ErrNotConfigured
	}
	return client.CatalogMarkdown(ctx, capsetID)
}

func (p *GatewayProvider) ProxyTarget() string {
	if p == nil {
		return ""
	}
	return p.proxyTarget
}
