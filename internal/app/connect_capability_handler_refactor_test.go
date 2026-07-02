package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	appconfig "agent-compose/pkg/config"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

// fixedGatewaySource is a capabilityGatewaySource backed by static settings,
// so provider tests can point at a mock OctoBus without a real ConfigStore.
type fixedGatewaySource struct {
	settings CapabilityGatewaySettings
}

func (f fixedGatewaySource) GetCapabilityGateway(context.Context) (CapabilityGatewaySettings, error) {
	return f.settings, nil
}

func newTestCapabilityProvider(addr, proxyTarget string) *capabilityProvider {
	return &capabilityProvider{source: fixedGatewaySource{settings: CapabilityGatewaySettings{Addr: addr}}, proxyTarget: proxyTarget}
}

func TestCapabilityServiceStatusDoesNotExposeAddr(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/v1/status" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "services": 3})
	}))
	defer server.Close()

	service := &Service{cap: newTestCapabilityProvider(server.URL, "")}
	resp, err := service.GetCapabilityStatus(context.Background(), connect.NewRequest(&agentcomposev1.GetCapabilityStatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Msg.GetConfigured() || !resp.Msg.GetOk() || resp.Msg.GetServiceCount() != 3 {
		t.Fatalf("unexpected response %+v", resp.Msg)
	}
	if resp.Msg.GetError() != "" {
		t.Fatalf("unexpected error leak %q", resp.Msg.GetError())
	}
	if resp.Msg.GetRuntimeConfigured() || resp.Msg.GetProxyListenConfigured() || resp.Msg.GetProxyTargetConfigured() {
		t.Fatalf("runtime config should be false without daemon listen/target env: %+v", resp.Msg)
	}
}

func TestCapabilityServiceStatusReportsRuntimeProxyConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer server.Close()

	service := &Service{
		config: &appconfig.Config{CapGRPCListen: "127.0.0.1:9100"},
		cap:    newTestCapabilityProvider(server.URL, "agent-compose:9100"),
	}
	resp, err := service.GetCapabilityStatus(context.Background(), connect.NewRequest(&agentcomposev1.GetCapabilityStatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Msg.GetRuntimeConfigured() || !resp.Msg.GetProxyListenConfigured() || !resp.Msg.GetProxyTargetConfigured() {
		t.Fatalf("runtime config not reported: %+v", resp.Msg)
	}
}

func TestCapabilityServiceCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/v1/catalog/dev" || r.URL.Query().Get("all") != "true" {
			t.Fatalf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"capset_id": "dev",
			"name":      "Dev",
			"grpc": []map[string]any{{
				"service_id":                 "svc",
				"instance_id":                "inst",
				"method_full_name":           "pkg.Service/Call",
				"method_path":                "/pkg.Service/Call",
				"request_message_full_name":  "pkg.Request",
				"response_message_full_name": "pkg.Response",
			}},
		})
	}))
	defer server.Close()

	service := &Service{cap: newTestCapabilityProvider(server.URL, "")}
	resp, err := service.GetCapabilityCatalog(context.Background(), connect.NewRequest(&agentcomposev1.GetCapabilityCatalogRequest{CapsetId: "dev"}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetCapsetId() != "dev" || len(resp.Msg.GetMethods()) != 1 {
		t.Fatalf("unexpected response %+v", resp.Msg)
	}
	method := resp.Msg.GetMethods()[0]
	if method.GetServiceId() != "svc" || method.GetInstanceId() != "inst" || len(method.GetEndpoints()) != 1 {
		t.Fatalf("unexpected method %+v", method)
	}
}
