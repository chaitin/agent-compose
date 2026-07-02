package app

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/samber/do/v2"
	"google.golang.org/protobuf/types/known/emptypb"

	"agent-compose/pkg/capability"
	appconfig "agent-compose/pkg/config"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

// capabilityGatewaySource supplies the page-configured OctoBus connection.
// *ConfigStore satisfies it; tests can substitute a fake.
type capabilityGatewaySource interface {
	GetCapabilityGateway(ctx context.Context) (CapabilityGatewaySettings, error)
}

type CapabilityProvider interface {
	Status(context.Context) capability.Status
	ListCapsets(context.Context) ([]capability.Capset, error)
	Catalog(context.Context, string) (capability.Catalog, error)
	CapabilityGuide(ctx context.Context, capsetID string) ([]byte, error)
	ProxyTarget() string
}

type capabilityIntegration interface{ CapabilityProvider }

// capabilityProvider reads the OctoBus connection from source on every call, so
// page edits take effect without a restart. An empty addr means disabled.
// proxyTarget is the deployment-fixed, guest-reachable proxy address.
type capabilityProvider struct {
	source      capabilityGatewaySource
	proxyTarget string
}

func NewCapabilityProvider(di do.Injector) (capabilityIntegration, error) {
	conf := do.MustInvoke[*appconfig.Config](di)
	return &capabilityProvider{
		source:      do.MustInvoke[*ConfigStore](di),
		proxyTarget: strings.TrimSpace(conf.CapGRPCTarget),
	}, nil
}

// client builds an OctoBus client from the current settings. ok is false when
// the gateway is not configured (empty addr) or settings are unreadable.
func (p *capabilityProvider) client(ctx context.Context) (*capability.Client, bool) {
	if p == nil || p.source == nil {
		return nil, false
	}
	settings, err := p.source.GetCapabilityGateway(ctx)
	if err != nil || strings.TrimSpace(settings.Addr) == "" {
		return nil, false
	}
	return capability.NewClient(capability.Config{Addr: settings.Addr, Token: settings.Token}), true
}

func (p *capabilityProvider) Status(ctx context.Context) capability.Status {
	client, ok := p.client(ctx)
	if !ok {
		return capability.Status{Configured: false, OK: false, Status: "not_configured"}
	}
	return client.Status(ctx)
}

func (p *capabilityProvider) ListCapsets(ctx context.Context) ([]capability.Capset, error) {
	client, ok := p.client(ctx)
	if !ok {
		return []capability.Capset{}, nil
	}
	return client.ListCapsets(ctx)
}

func (p *capabilityProvider) Catalog(ctx context.Context, capsetID string) (capability.Catalog, error) {
	client, ok := p.client(ctx)
	if !ok {
		return capability.Catalog{}, capability.ErrNotConfigured
	}
	return client.Catalog(ctx, capsetID)
}

func (p *capabilityProvider) CapabilityGuide(ctx context.Context, capsetID string) ([]byte, error) {
	client, ok := p.client(ctx)
	if !ok {
		return nil, capability.ErrNotConfigured
	}
	return client.CatalogMarkdown(ctx, capsetID)
}

func (p *capabilityProvider) ProxyTarget() string {
	return p.proxyTarget
}

func (s *Service) GetCapabilityStatus(ctx context.Context, req *connect.Request[agentcomposev1.GetCapabilityStatusRequest]) (*connect.Response[agentcomposev1.CapabilityStatusResponse], error) {
	_ = req
	status := s.cap.Status(ctx)
	proxyListenConfigured := s.config != nil && strings.TrimSpace(s.config.CapGRPCListen) != ""
	proxyTargetConfigured := strings.TrimSpace(s.cap.ProxyTarget()) != ""
	return connect.NewResponse(&agentcomposev1.CapabilityStatusResponse{
		Configured:            status.Configured,
		Ok:                    status.OK,
		Status:                status.Status,
		ServiceCount:          status.ServiceCount,
		Error:                 status.Error,
		RuntimeConfigured:     proxyListenConfigured && proxyTargetConfigured,
		ProxyListenConfigured: proxyListenConfigured,
		ProxyTargetConfigured: proxyTargetConfigured,
	}), nil
}

func (s *Service) ListCapabilitySets(ctx context.Context, req *connect.Request[agentcomposev1.ListCapabilitySetsRequest]) (*connect.Response[agentcomposev1.ListCapabilitySetsResponse], error) {
	_ = req
	capsets, err := s.cap.ListCapsets(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	resp := &agentcomposev1.ListCapabilitySetsResponse{}
	for _, item := range capsets {
		if !item.Enabled {
			continue
		}
		resp.Capsets = append(resp.Capsets, &agentcomposev1.CapabilitySet{
			Id:          item.ID,
			Name:        item.Name,
			Description: item.Description,
			Enabled:     item.Enabled,
		})
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) GetCapabilityCatalog(ctx context.Context, req *connect.Request[agentcomposev1.GetCapabilityCatalogRequest]) (*connect.Response[agentcomposev1.GetCapabilityCatalogResponse], error) {
	catalog, err := s.cap.Catalog(ctx, req.Msg.GetCapsetId())
	if err != nil {
		return nil, capabilityConnectError(err)
	}
	return connect.NewResponse(toProtoCapabilityCatalog(catalog)), nil
}

func (s *Service) GetCapabilityGatewayConfig(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.CapabilityGatewayConfig], error) {
	_ = req
	settings, err := s.configDB.GetCapabilityGateway(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(toProtoCapabilityGatewayConfig(settings)), nil
}

func (s *Service) UpdateCapabilityGatewayConfig(ctx context.Context, req *connect.Request[agentcomposev1.UpdateCapabilityGatewayConfigRequest]) (*connect.Response[agentcomposev1.CapabilityGatewayConfig], error) {
	token := strings.TrimSpace(req.Msg.GetToken())
	saved, err := s.configDB.SaveCapabilityGateway(ctx, CapabilityGatewaySettings{
		Addr:  req.Msg.GetAddr(),
		Token: token,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(toProtoCapabilityGatewayConfig(saved)), nil
}

func toProtoCapabilityGatewayConfig(settings CapabilityGatewaySettings) *agentcomposev1.CapabilityGatewayConfig {
	return &agentcomposev1.CapabilityGatewayConfig{
		Addr:     settings.Addr,
		TokenSet: strings.TrimSpace(settings.Token) != "",
	}
}

func capabilityConnectError(err error) error {
	switch {
	case errors.Is(err, capability.ErrNotConfigured):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, capability.ErrInvalidCatalog):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeUnavailable, err)
	}
}

func toProtoCapabilityCatalog(item capability.Catalog) *agentcomposev1.GetCapabilityCatalogResponse {
	resp := &agentcomposev1.GetCapabilityCatalogResponse{
		CapsetId:    item.CapsetID,
		Name:        item.Name,
		Description: item.Description,
	}
	for _, method := range item.Methods {
		resp.Methods = append(resp.Methods, toProtoCapabilityMethod(method))
	}
	return resp
}

func toProtoCapabilityMethod(item capability.Method) *agentcomposev1.CapabilityMethod {
	resp := &agentcomposev1.CapabilityMethod{
		ServiceId:               item.ServiceID,
		InstanceId:              item.InstanceID,
		RuntimeMode:             item.RuntimeMode,
		MethodFullName:          item.MethodFullName,
		RequestMessageFullName:  item.RequestMessageFullName,
		ResponseMessageFullName: item.ResponseMessageFullName,
		BackendInstanceStatus:   item.BackendInstanceStatus,
	}
	for _, endpoint := range item.Endpoints {
		resp.Endpoints = append(resp.Endpoints, &agentcomposev1.CapabilityEndpoint{
			Protocol:     endpoint.Protocol,
			Endpoint:     endpoint.Endpoint,
			MethodPath:   endpoint.MethodPath,
			Metadata:     endpoint.Metadata,
			ToolName:     endpoint.ToolName,
			Procedure:    endpoint.Procedure,
			HttpMethod:   endpoint.HTTPMethod,
			ContentTypes: endpoint.ContentTypes,
		})
	}
	return resp
}
