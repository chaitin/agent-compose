package capabilities

import (
	"context"
	"strings"

	"agent-compose/pkg/capability"
	domain "agent-compose/pkg/model"
)

// GatewaySource supplies the page-configured OctoBus connection.
type GatewaySource interface {
	GetCapabilityGateway(ctx context.Context) (domain.CapabilityGatewaySettings, error)
}

type Provider interface {
	Status(context.Context) capability.Status
	ListCapsets(context.Context) ([]capability.Capset, error)
	Catalog(context.Context, string) (capability.Catalog, error)
	CapabilityGuide(ctx context.Context, capsetID string) ([]byte, error)
	ProxyTarget() string
}

func ProxyTarget(provider Provider) string {
	if provider == nil {
		return ""
	}
	return provider.ProxyTarget()
}

// DynamicProvider reads the OctoBus connection from source on every call, so
// page edits take effect without a restart. An empty addr means disabled.
// proxyTarget is the deployment-fixed, guest-reachable proxy address.
type DynamicProvider struct {
	source      GatewaySource
	proxyTarget string
}

func NewDynamicProvider(source GatewaySource, proxyTarget string) *DynamicProvider {
	return &DynamicProvider{
		source:      source,
		proxyTarget: strings.TrimSpace(proxyTarget),
	}
}

// client builds an OctoBus client from the current settings. ok is false when
// the gateway is not configured (empty addr) or settings are unreadable.
func (p *DynamicProvider) client(ctx context.Context) (*capability.Client, bool) {
	if p == nil || p.source == nil {
		return nil, false
	}
	settings, err := p.source.GetCapabilityGateway(ctx)
	if err != nil || strings.TrimSpace(settings.Addr) == "" {
		return nil, false
	}
	return capability.NewClient(capability.Config{Addr: settings.Addr, Token: settings.Token}), true
}

func (p *DynamicProvider) Status(ctx context.Context) capability.Status {
	client, ok := p.client(ctx)
	if !ok {
		return capability.Status{Configured: false, OK: false, Status: "not_configured"}
	}
	return client.Status(ctx)
}

func (p *DynamicProvider) ListCapsets(ctx context.Context) ([]capability.Capset, error) {
	client, ok := p.client(ctx)
	if !ok {
		return []capability.Capset{}, nil
	}
	return client.ListCapsets(ctx)
}

func (p *DynamicProvider) Catalog(ctx context.Context, capsetID string) (capability.Catalog, error) {
	client, ok := p.client(ctx)
	if !ok {
		return capability.Catalog{}, capability.ErrNotConfigured
	}
	return client.Catalog(ctx, capsetID)
}

func (p *DynamicProvider) CapabilityGuide(ctx context.Context, capsetID string) ([]byte, error) {
	client, ok := p.client(ctx)
	if !ok {
		return nil, capability.ErrNotConfigured
	}
	return client.CatalogMarkdown(ctx, capsetID)
}

func (p *DynamicProvider) ProxyTarget() string {
	if p == nil {
		return ""
	}
	return p.proxyTarget
}
