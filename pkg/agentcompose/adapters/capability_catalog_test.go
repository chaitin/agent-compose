package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	"github.com/chaitin/agent-compose/pkg/capability"
)

func TestRuntimeCatalogUsesQualifiedServerWithoutRewritingTheUpstreamID(t *testing.T) {
	var gotPath, gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotToken = r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"capset_id":"dev","grpc":[{"instance_id":"one","method_full_name":"sample.Service/Echo","request_message_full_name":"sample.Request","response_message_full_name":"sample.Response"}]}`))
	}))
	defer server.Close()
	store := &mutableCapabilityAgentStore{definition: projectCapabilityDefinition(server.URL, "project-secret")}
	provider := NewCapabilityProvider(staticGatewaySource{}, NewProjectOctoBusTargetResolver(store), "proxy:1")
	result := capabilities.BuildRuntimeCatalog(context.Background(), provider, capabilities.GuideScope{ProjectID: "project-1", AgentID: "agent-1"}, "sandbox", []string{"internal/dev"})
	if gotPath != "/admin/v1/catalog/dev" || gotToken != "Bearer project-secret" || len(result.Tools) != 1 || result.Tools[0].Capset != "internal/dev" {
		t.Fatalf("path=%q, catalog=%#v", gotPath, result)
	}
}

func TestRuntimeCatalogCarriesTrustedScopeToTheGlobalGateway(t *testing.T) {
	var gotProject, gotUsername string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProject, gotUsername = r.Header.Get(capability.ProjectScopeHeader), r.Header.Get(capability.ExtUsernameHeader)
		_, _ = w.Write([]byte(`{"capset_id":"dev"}`))
	}))
	defer server.Close()
	provider := NewCapabilityProvider(staticGatewaySource{addr: server.URL}, failIfResolved{t: t}, "proxy:1")
	result := capabilities.BuildRuntimeCatalog(context.Background(), provider, capabilities.GuideScope{ProjectID: "project-1", Username: "alice"}, "sandbox", []string{"dev"})
	if gotProject != "project-1" || gotUsername != "alice" || len(result.Unavailable) != 0 {
		t.Fatalf("scope=%q/%q, catalog=%#v", gotProject, gotUsername, result)
	}
}
