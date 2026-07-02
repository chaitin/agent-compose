package capability

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

// fakeOctobus is an in-test OctoBus admin server covering the endpoints
// agent-compose depends on (status, capsets, catalog json + markdown). It records the last
// Authorization header so tests can assert token injection.
type fakeOctobus struct {
	server   *httptest.Server
	mu       sync.Mutex
	lastAuth string
}

func startFakeOctobus(t *testing.T) *fakeOctobus {
	t.Helper()
	f := &fakeOctobus{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.lastAuth = r.Header.Get("Authorization")
		f.mu.Unlock()
		switch {
		case r.URL.Path == "/admin/v1/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "services": 2})
		case r.URL.Path == "/admin/v1/capsets":
			_ = json.NewEncoder(w).Encode(map[string]any{"capsets": []map[string]any{
				{"id": "dev", "name": "Dev", "description": "dev capset", "enabled": true},
				{"id": "disabled", "name": "Disabled", "description": "disabled capset", "enabled": false},
			}})
		case r.URL.Path == "/admin/v1/catalog/dev" && r.URL.Query().Get("format") == "md":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte("# Catalog: dev\n\n## gRPC\n\n| Method | Metadata |\n| --- | --- |\n| `/pkg.Service/Call` | `x-octobus-capset=dev, x-octobus-instance=inst` |\n"))
		case r.URL.Path == "/admin/v1/catalog/dev":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"capset_id": "dev",
				"name":      "Dev",
				"grpc": []map[string]any{{
					"service_id":       "svc",
					"instance_id":      "inst",
					"method_full_name": "pkg.Service/Call",
					"method_path":      "/pkg.Service/Call",
					"metadata": map[string]string{
						"x-octobus-capset":   "dev",
						"x-octobus-instance": "inst",
					},
				}},
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeOctobus) auth() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastAuth
}

// TestCapabilityGatewayControlPlaneE2E drives the real ConfigService and
// CapabilityService handlers, backed by a real ConfigStore and dynamic
// provider, against a fake OctoBus — the exact path the frontend exercises.
func TestCapabilityGatewayControlPlaneE2E(t *testing.T) {
	ctx := context.Background()
	configDB := newTestConfigStore(t)
	octo := startFakeOctobus(t)
	service := &Service{
		configDB: configDB,
		cap:      &capabilityProvider{source: configDB, proxyTarget: "agent-compose:9100"},
	}

	// 1. Page configures the OctoBus connection (addr + token).
	cfg, err := service.UpdateCapabilityGatewayConfig(ctx, connect.NewRequest(&agentcomposev1.UpdateCapabilityGatewayConfigRequest{Addr: octo.server.URL, Token: "secret"}))
	if err != nil {
		t.Fatalf("update gateway config: %v", err)
	}
	if cfg.Msg.GetAddr() != octo.server.URL || !cfg.Msg.GetTokenSet() {
		t.Fatalf("unexpected gateway config: %+v", cfg.Msg)
	}

	// 2. Read-back reports token_set but never returns the token.
	got, err := service.GetCapabilityGatewayConfig(ctx, connect.NewRequest(&emptypb.Empty{}))
	if err != nil {
		t.Fatalf("get gateway config: %v", err)
	}
	if got.Msg.GetAddr() != octo.server.URL || !got.Msg.GetTokenSet() {
		t.Fatalf("unexpected read-back config: %+v", got.Msg)
	}

	// 3. Live status reaches OctoBus with the bearer token injected server-side.
	status, err := service.GetCapabilityStatus(ctx, connect.NewRequest(&agentcomposev1.GetCapabilityStatusRequest{}))
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	if !status.Msg.GetConfigured() || !status.Msg.GetOk() || status.Msg.GetServiceCount() != 2 {
		t.Fatalf("unexpected status: %+v", status.Msg)
	}
	if octo.auth() != "Bearer secret" {
		t.Fatalf("OctoBus saw auth %q, want %q", octo.auth(), "Bearer secret")
	}

	// 4. Enabled capsets and catalog flow through and are normalized. Disabled
	// capsets are hidden so callers cannot select capabilities OctoBus will
	// reject at runtime.
	capsets, err := service.ListCapabilitySets(ctx, connect.NewRequest(&agentcomposev1.ListCapabilitySetsRequest{}))
	if err != nil {
		t.Fatalf("list capsets: %v", err)
	}
	if len(capsets.Msg.GetCapsets()) != 1 || capsets.Msg.GetCapsets()[0].GetId() != "dev" {
		t.Fatalf("unexpected capsets: %+v", capsets.Msg)
	}
	catalog, err := service.GetCapabilityCatalog(ctx, connect.NewRequest(&agentcomposev1.GetCapabilityCatalogRequest{CapsetId: "dev"}))
	if err != nil {
		t.Fatalf("get catalog: %v", err)
	}
	methods := catalog.Msg.GetMethods()
	if len(methods) != 1 || methods[0].GetServiceId() != "svc" || methods[0].GetInstanceId() != "inst" {
		t.Fatalf("unexpected catalog: %+v", catalog.Msg)
	}
}

// TestCapabilityGatewayUpdateClearsToken verifies that an empty token follows
// the API contract and clears any previously saved OctoBus token.
func TestCapabilityGatewayUpdateClearsToken(t *testing.T) {
	ctx := context.Background()
	configDB := newTestConfigStore(t)
	service := &Service{configDB: configDB, cap: &capabilityProvider{source: configDB}}

	if _, err := service.UpdateCapabilityGatewayConfig(ctx, connect.NewRequest(&agentcomposev1.UpdateCapabilityGatewayConfigRequest{Addr: "http://octo:9000", Token: "keep-me"})); err != nil {
		t.Fatalf("initial update: %v", err)
	}
	cleared, err := service.UpdateCapabilityGatewayConfig(ctx, connect.NewRequest(&agentcomposev1.UpdateCapabilityGatewayConfigRequest{Addr: "http://octo:9000", Token: ""}))
	if err != nil {
		t.Fatalf("clear token update: %v", err)
	}
	if cleared.Msg.GetTokenSet() {
		t.Fatalf("token_set = true after clear, want false")
	}

	settings, err := configDB.GetCapabilityGateway(ctx)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if settings.Addr != "http://octo:9000" || settings.Token != "" {
		t.Fatalf("token not cleared: %+v", settings)
	}
}

// TestCapabilityGatewayDisabledWhenUnconfigured verifies the not-configured
// state when no addr is stored.
func TestCapabilityGatewayDisabledWhenUnconfigured(t *testing.T) {
	ctx := context.Background()
	configDB := newTestConfigStore(t)
	service := &Service{configDB: configDB, cap: &capabilityProvider{source: configDB}}

	status, err := service.GetCapabilityStatus(ctx, connect.NewRequest(&agentcomposev1.GetCapabilityStatusRequest{}))
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	if status.Msg.GetConfigured() || status.Msg.GetOk() {
		t.Fatalf("expected not-configured status, got %+v", status.Msg)
	}
	capsets, err := service.ListCapabilitySets(ctx, connect.NewRequest(&agentcomposev1.ListCapabilitySetsRequest{}))
	if err != nil {
		t.Fatalf("list capsets: %v", err)
	}
	if len(capsets.Msg.GetCapsets()) != 0 {
		t.Fatalf("expected no capsets when unconfigured, got %+v", capsets.Msg)
	}
}
