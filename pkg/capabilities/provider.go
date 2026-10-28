package capabilities

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/capability"
	domain "github.com/chaitin/agent-compose/pkg/model"
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

// GuideScope identifies who a capability guide is rendered for: the project
// agent whose project-scoped OctoBus declarations may be used, and optionally
// the user the current run acts for. Upstream gateways resolve a project to
// its owner themselves, so Username may stay empty.
type GuideScope struct {
	ProjectID string
	AgentID   string
	Username  string
}

// GuideScopeFromSandbox derives the canonical project and agent scope shared
// by capability guide generation and proxy authorization.
func GuideScopeFromSandbox(sandbox *domain.Sandbox) GuideScope {
	if sandbox == nil {
		return GuideScope{}
	}
	return GuideScope{
		ProjectID: sandboxTagValue(sandbox, "project", "project_id"),
		AgentID:   sandboxTagValue(sandbox, domain.AgentSandboxTagID),
	}
}

func sandboxTagValue(sandbox *domain.Sandbox, names ...string) string {
	for _, name := range names {
		for _, tag := range sandbox.Summary.Tags {
			if strings.TrimSpace(tag.Name) == name {
				return strings.TrimSpace(tag.Value)
			}
		}
	}
	return ""
}

// ScopedGuideProvider extends the global provider with project-aware guide
// routing. Callers should use CapabilityGuideForScope so legacy providers keep
// working for unqualified capset IDs.
type ScopedGuideProvider interface {
	CapabilityGuideForScope(context.Context, GuideScope, string) ([]byte, error)
}

// errProjectOctoBusNotConfigured reports that a qualified capset declaration
// selects a project OctoBus server, but the provider in use has no
// project-server resolver: a legacy Provider or a bare DynamicProvider used
// without the project-aware adapter.
var errProjectOctoBusNotConfigured = errors.New("project OctoBus capability guide provider is not configured")

// CapabilityGuideForScope routes every declaration through a scoped provider
// when one is available: qualified declarations select a project OctoBus
// server, unqualified ones keep using the daemon-wide gateway but carry the
// scope. Providers without scope support keep the legacy global behavior.
func CapabilityGuideForScope(ctx context.Context, provider Provider, scope GuideScope, declaration string) ([]byte, error) {
	declaration = strings.TrimSpace(declaration)
	parsed, err := capability.ParseCapsetDeclaration(declaration)
	if err != nil {
		return nil, fmt.Errorf("invalid capset declaration: %w", err)
	}
	scoped, ok := provider.(ScopedGuideProvider)
	if !parsed.Qualified() {
		if ok {
			return scoped.CapabilityGuideForScope(ctx, scope, declaration)
		}
		return provider.CapabilityGuide(ctx, declaration)
	}
	if !ok {
		return nil, errProjectOctoBusNotConfigured
	}
	return scoped.CapabilityGuideForScope(ctx, scope, declaration)
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
	return p.clientWithScope(ctx, capability.Scope{})
}

// clientWithScope is client with the optional project/user context a directory
// read attaches. A zero scope sends no context headers.
func (p *DynamicProvider) clientWithScope(ctx context.Context, scope capability.Scope) (*capability.Client, bool) {
	if p == nil || p.source == nil {
		return nil, false
	}
	settings, err := p.source.GetCapabilityGateway(ctx)
	if err != nil || strings.TrimSpace(settings.Addr) == "" {
		return nil, false
	}
	return capability.NewClient(capability.Config{Addr: settings.Addr, Token: settings.Token, Scope: scope}), true
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

// CapabilityGuideForScope renders an unqualified capset guide through the
// daemon-wide gateway while carrying the sandbox's project and user context.
// Qualified declarations never belong to the gateway, so they keep failing the
// same way they did before this provider gained scope support.
func (p *DynamicProvider) CapabilityGuideForScope(ctx context.Context, scope GuideScope, declaration string) ([]byte, error) {
	parsed, err := capability.ParseCapsetDeclaration(declaration)
	if err != nil {
		return nil, fmt.Errorf("invalid capset declaration: %w", err)
	}
	if parsed.Qualified() {
		return nil, errProjectOctoBusNotConfigured
	}
	client, ok := p.clientWithScope(ctx, capability.Scope{ProjectID: scope.ProjectID, Username: scope.Username})
	if !ok {
		return nil, capability.ErrNotConfigured
	}
	return client.CatalogMarkdown(ctx, parsed.CapsetID)
}

func (p *DynamicProvider) ProxyTarget() string {
	if p == nil {
		return ""
	}
	return p.proxyTarget
}
