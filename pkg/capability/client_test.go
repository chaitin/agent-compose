package capability

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientStatusNotConfigured(t *testing.T) {
	client := NewClient(Config{})
	status := client.Status(context.Background())
	if status.Configured {
		t.Fatal("expected status to be unconfigured")
	}
	if status.Status != "not_configured" {
		t.Fatalf("unexpected status %q", status.Status)
	}
}

func TestClientInjectsToken(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "services": 2})
	}))
	defer server.Close()

	client := NewClient(Config{Addr: server.URL, Token: "secret-token"})
	status := client.Status(context.Background())
	if !status.OK {
		t.Fatalf("expected ok status, got %+v", status)
	}
	if authorization != "Bearer secret-token" {
		t.Fatalf("unexpected authorization header %q", authorization)
	}
}

func TestListCapsets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/v1/capsets" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"capsets": []map[string]any{{
				"id":          "dev",
				"name":        "Dev",
				"description": "tools",
				"enabled":     true,
			}},
		})
	}))
	defer server.Close()

	client := NewClient(Config{Addr: server.URL})
	capsets, err := client.ListCapsets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(capsets) != 1 || capsets[0].ID != "dev" || !capsets[0].Enabled {
		t.Fatalf("unexpected capsets %+v", capsets)
	}
}

func TestClientCatalogUsesAllQueryAndEscapesCapset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/admin/v1/catalog/dev%2Ftools" {
			t.Fatalf("unexpected path %s", r.URL.EscapedPath())
		}
		if r.URL.Query().Get("all") != "true" {
			t.Fatalf("expected all=true, got %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(octobusCatalogResponse{CapsetID: "dev/tools"})
	}))
	defer server.Close()

	client := NewClient(Config{Addr: server.URL})
	catalog, err := client.Catalog(context.Background(), "dev/tools")
	if err != nil {
		t.Fatal(err)
	}
	if catalog.CapsetID != "dev/tools" {
		t.Fatalf("unexpected catalog %+v", catalog)
	}
}

func TestClientPreservesOctoBusErrorPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code":    "NOT_FOUND",
				"message": "capset dev was not found",
				"details": map[string]any{"capset_id": "dev"},
			},
		})
	}))
	defer server.Close()

	client := NewClient(Config{Addr: server.URL})
	_, err := client.Catalog(context.Background(), "dev")
	if err == nil {
		t.Fatal("Catalog returned nil error")
	}
	var upstream *OctoBusError
	if !errors.As(err, &upstream) {
		t.Fatalf("Catalog error type = %T, want *OctoBusError: %v", err, err)
	}
	if upstream.HTTPStatus != http.StatusNotFound || upstream.Code != "NOT_FOUND" || upstream.Message != "capset dev was not found" {
		t.Fatalf("unexpected OctoBus error: %+v", upstream)
	}
	if !strings.Contains(err.Error(), "NOT_FOUND: capset dev was not found") {
		t.Fatalf("error string did not preserve upstream code/message: %v", err)
	}
}

func TestClientCatalogMarkdownPreservesPlainTextOctoBusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/admin/v1/catalog/dev" {
			t.Fatalf("unexpected path %s", r.URL.EscapedPath())
		}
		if r.URL.Query().Get("format") != "md" || r.URL.Query().Get("grpc") != "true" {
			t.Fatalf("unexpected query %s", r.URL.RawQuery)
		}
		if r.Header.Get("Accept") != "text/markdown" {
			t.Fatalf("unexpected accept header %q", r.Header.Get("Accept"))
		}
		http.Error(w, "temporary upstream outage", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := NewClient(Config{Addr: server.URL})
	_, err := client.CatalogMarkdown(context.Background(), "dev")
	if err == nil {
		t.Fatal("CatalogMarkdown returned nil error")
	}
	var upstream *OctoBusError
	if !errors.As(err, &upstream) {
		t.Fatalf("CatalogMarkdown error type = %T, want *OctoBusError: %v", err, err)
	}
	if upstream.HTTPStatus != http.StatusServiceUnavailable || upstream.Code != "HTTP_503" || upstream.Message != http.StatusText(http.StatusServiceUnavailable) {
		t.Fatalf("unexpected OctoBus error fallback: %+v", upstream)
	}
}

func TestClientInvokeConnectPostsJSONAndPreservesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.EscapedPath() != "/capsets/threat-intel/connect/cloud/ThreatBook.Cloud/IpReputation" {
			t.Fatalf("unexpected path %s", r.URL.EscapedPath())
		}
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Fatalf("unexpected authorization header %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Connect-Protocol-Version") != "1" {
			t.Fatalf("unexpected connect protocol header %q", r.Header.Get("Connect-Protocol-Version"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"resource":"8.8.8.8","lang":"zh"}` {
			t.Fatalf("unexpected body %s", body)
		}
		_, _ = w.Write([]byte(`{"response_code":0,"data":{"is_malicious":false}}`))
	}))
	defer server.Close()

	client := NewClient(Config{Addr: server.URL, Token: "secret-token"})
	result, err := client.InvokeConnect(context.Background(), InvokeRequest{
		CapsetID:   "threat-intel",
		InstanceID: "cloud",
		ServiceID:  "ThreatBook.Cloud",
		Method:     "IpReputation",
		Payload:    json.RawMessage(`{"resource":"8.8.8.8","lang":"zh"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != `{"response_code":0,"data":{"is_malicious":false}}` {
		t.Fatalf("unexpected result %s", result)
	}
}

func TestClientSeparatesAdminAndInvokeTokens(t *testing.T) {
	var adminAuthorization, invokeAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/v1/capsets":
			adminAuthorization = r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]any{"capsets": []any{}})
		case "/capsets/threat-intel/connect/cloud/ThreatBook.Cloud/IpReputation":
			invokeAuthorization = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"response_code":0}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		Addr:       server.URL,
		Token:      "capset-token",
		AdminToken: "admin-token",
	})
	if _, err := client.ListCapsets(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.InvokeConnect(context.Background(), InvokeRequest{
		CapsetID:   "threat-intel",
		InstanceID: "cloud",
		ServiceID:  "ThreatBook.Cloud",
		Method:     "IpReputation",
		Payload:    json.RawMessage(`{"resource":"8.8.8.8"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if adminAuthorization != "Bearer admin-token" {
		t.Fatalf("admin authorization = %q", adminAuthorization)
	}
	if invokeAuthorization != "Bearer capset-token" {
		t.Fatalf("invoke authorization = %q", invokeAuthorization)
	}
}

func TestClientInvokeConnectPreservesConnectError(t *testing.T) {
	const nestedMessage = `{"code":"FAILED_PRECONDITION","message":"threatbook upstream business failure","response_code":-4,"verbose_msg":"超出每月访问限制"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":    "failed_precondition",
			"message": nestedMessage,
		})
	}))
	defer server.Close()

	client := NewClient(Config{Addr: server.URL, Token: "capset-token"})
	_, err := client.InvokeConnect(context.Background(), InvokeRequest{
		CapsetID:   "threat-intel",
		InstanceID: "cloud",
		ServiceID:  "ThreatBook.Cloud",
		Method:     "IpReputation",
	})
	if err == nil {
		t.Fatal("InvokeConnect returned nil error")
	}
	var upstream *OctoBusError
	if !errors.As(err, &upstream) {
		t.Fatalf("InvokeConnect error type = %T, want *OctoBusError: %v", err, err)
	}
	if upstream.HTTPStatus != http.StatusPreconditionFailed || upstream.Code != "failed_precondition" || upstream.Message != nestedMessage {
		t.Fatalf("unexpected OctoBus error: %+v", upstream)
	}
	if !strings.Contains(err.Error(), "response_code") || !strings.Contains(err.Error(), "超出每月访问限制") {
		t.Fatalf("error string did not preserve upstream details: %v", err)
	}
}

func TestClientInvokeConnectRejectsInvalidRequest(t *testing.T) {
	client := NewClient(Config{Addr: "http://127.0.0.1:9000"})
	tests := []InvokeRequest{
		{InstanceID: "cloud", ServiceID: "svc", Method: "method"},
		{CapsetID: "dev", ServiceID: "svc", Method: "method"},
		{CapsetID: "dev", InstanceID: "cloud", Method: "method"},
		{CapsetID: "dev", InstanceID: "cloud", ServiceID: "svc"},
		{CapsetID: "dev", InstanceID: "cloud", ServiceID: "svc", Method: "method", Payload: json.RawMessage(`{`)},
	}
	for _, request := range tests {
		if _, err := client.InvokeConnect(context.Background(), request); !errors.Is(err, ErrInvalidInvoke) {
			t.Fatalf("InvokeConnect(%+v) error = %v, want ErrInvalidInvoke", request, err)
		}
	}
}

func TestNormalizeCatalogMergesEndpoints(t *testing.T) {
	catalog, err := NormalizeCatalog(octobusCatalogResponse{
		CapsetID: "dev",
		Name:     "Dev",
		GRPC: []octobusGRPCItem{{
			ServiceID:               "svc",
			InstanceID:              "inst",
			RuntimeMode:             "stdio",
			MethodFullName:          "pkg.Service/Call",
			MethodPath:              "/pkg.Service/Call",
			Metadata:                map[string]string{"k": "v"},
			RequestMessageFullName:  "pkg.Request",
			ResponseMessageFullName: "pkg.Response",
			BackendInstanceStatus:   "running",
		}},
		MCP: []octobusMCPItem{{
			ServiceID:      "svc",
			InstanceID:     "inst",
			MethodFullName: "pkg.Service/Call",
			Endpoint:       "/capsets/dev/mcp",
			ToolName:       "pkg_service_call",
		}},
		ConnectRPC: []octobusConnectRPCItem{{
			ServiceID:      "svc",
			InstanceID:     "inst",
			MethodFullName: "pkg.Service/Call",
			Procedure:      "/pkg.Service/Call",
			Endpoint:       "/capsets/dev/connect/inst/pkg.Service/Call",
			HTTPMethod:     http.MethodPost,
			ContentTypes:   []string{"application/json"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Methods) != 1 {
		t.Fatalf("expected one method, got %+v", catalog.Methods)
	}
	method := catalog.Methods[0]
	if method.ServiceID != "svc" || method.InstanceID != "inst" || method.MethodFullName != "pkg.Service/Call" {
		t.Fatalf("unexpected method %+v", method)
	}
	if len(method.Endpoints) != 3 {
		t.Fatalf("expected three endpoints, got %+v", method.Endpoints)
	}
}

func TestNormalizeCatalogAllowsDuplicateGRPCMethodBindings(t *testing.T) {
	catalog, err := NormalizeCatalog(octobusCatalogResponse{
		CapsetID: "dev",
		GRPC: []octobusGRPCItem{
			{ServiceID: "svc-a", InstanceID: "inst-a", MethodFullName: "pkg.Service/Call"},
			{ServiceID: "svc-b", InstanceID: "inst-b", MethodFullName: "pkg.Service/Call"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Methods) != 2 {
		t.Fatalf("expected duplicate method bindings to be preserved, got %+v", catalog.Methods)
	}
}
