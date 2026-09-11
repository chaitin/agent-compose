package api

import (
	"context"
	"encoding/json"
	"strings"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	"github.com/chaitin/agent-compose/pkg/capability"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

type CapabilityV2Handler struct {
	provider capabilities.Provider
	runtime  CapabilityRuntimeConfig
}

func NewCapabilityV2Handler(provider capabilities.Provider, runtime CapabilityRuntimeConfig) *CapabilityV2Handler {
	return &CapabilityV2Handler{provider: provider, runtime: runtime}
}
func (h *CapabilityV2Handler) GetCapabilityStatus(ctx context.Context, _ *connect.Request[agentcomposev2.GetCapabilityStatusRequest]) (*connect.Response[agentcomposev2.CapabilityStatusResponse], error) {
	status := h.provider.Status(ctx)
	listen := h.runtime != nil && strings.TrimSpace(h.runtime.CapProxyListen()) != ""
	target := strings.TrimSpace(h.provider.ProxyTarget()) != ""
	return connect.NewResponse(&agentcomposev2.CapabilityStatusResponse{Configured: status.Configured, Ok: status.OK, Status: status.Status, ServiceCount: status.ServiceCount, Error: status.Error, RuntimeConfigured: listen && target, ProxyListenConfigured: listen, ProxyTargetConfigured: target}), nil
}
func (h *CapabilityV2Handler) ListCapabilitySets(ctx context.Context, req *connect.Request[agentcomposev2.ListCapabilitySetsRequest]) (*connect.Response[agentcomposev2.ListCapabilitySetsResponse], error) {
	items, err := h.provider.ListCapsets(ctx)
	if err != nil {
		return nil, CapabilityConnectError(err)
	}
	enabled := items[:0]
	for _, item := range items {
		if item.Enabled {
			enabled = append(enabled, item)
		}
	}
	page, total, err := paginateList(enabled, req.Msg.GetOffset(), req.Msg.GetLimit())
	if err != nil {
		return nil, err
	}
	response := &agentcomposev2.ListCapabilitySetsResponse{Total: total}
	for _, item := range page {
		response.Capsets = append(response.Capsets, &agentcomposev2.CapabilitySet{Id: item.ID, Name: item.Name, Description: item.Description, Enabled: true})
	}
	return connect.NewResponse(response), nil
}
func (h *CapabilityV2Handler) GetCapabilityCatalog(ctx context.Context, req *connect.Request[agentcomposev2.GetCapabilityCatalogRequest]) (*connect.Response[agentcomposev2.GetCapabilityCatalogResponse], error) {
	item, err := h.provider.Catalog(ctx, req.Msg.GetCapsetId())
	if err != nil {
		return nil, CapabilityConnectError(err)
	}
	return connect.NewResponse(capabilityCatalogV2(item)), nil
}
func (h *CapabilityV2Handler) InvokeCapability(ctx context.Context, req *connect.Request[agentcomposev2.InvokeCapabilityRequest]) (*connect.Response[agentcomposev2.InvokeCapabilityResponse], error) {
	result, err := h.provider.InvokeConnect(ctx, capability.InvokeRequest{
		CapsetID:   req.Msg.GetCapsetId(),
		InstanceID: req.Msg.GetInstanceId(),
		ServiceID:  req.Msg.GetServiceId(),
		Method:     req.Msg.GetMethod(),
		Payload:    json.RawMessage(strings.TrimSpace(req.Msg.GetPayloadJson())),
	})
	if err != nil {
		return nil, CapabilityConnectError(err)
	}
	return connect.NewResponse(&agentcomposev2.InvokeCapabilityResponse{ResultJson: string(result)}), nil
}
func capabilityCatalogV2(item capability.Catalog) *agentcomposev2.GetCapabilityCatalogResponse {
	response := &agentcomposev2.GetCapabilityCatalogResponse{CapsetId: item.CapsetID, Name: item.Name, Description: item.Description}
	for _, m := range item.Methods {
		method := &agentcomposev2.CapabilityMethod{ServiceId: m.ServiceID, InstanceId: m.InstanceID, RuntimeMode: m.RuntimeMode, MethodFullName: m.MethodFullName, RequestMessageFullName: m.RequestMessageFullName, ResponseMessageFullName: m.ResponseMessageFullName, BackendInstanceStatus: m.BackendInstanceStatus}
		for _, e := range m.Endpoints {
			method.Endpoints = append(method.Endpoints, &agentcomposev2.CapabilityEndpoint{Protocol: e.Protocol, Endpoint: e.Endpoint, MethodPath: e.MethodPath, Metadata: e.Metadata, ToolName: e.ToolName, Procedure: e.Procedure, HttpMethod: e.HTTPMethod, ContentTypes: e.ContentTypes})
		}
		response.Methods = append(response.Methods, method)
	}
	return response
}
