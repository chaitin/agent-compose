package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-compose/pkg/capabilities"
)

func TestProjectAwareCapabilityProviderUsesBareCatalogPathAndQualifiedGuideDeclaration(t *testing.T) {
	var gotPath, gotAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotAuthorization = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("# Upstream guide\n\nx-octobus-capset: dev"))
	}))
	defer server.Close()

	store := &mutableCapabilityAgentStore{definition: projectCapabilityDefinition(server.URL, "project-token")}
	provider := NewCapabilityProvider(staticGatewaySource{}, NewProjectOctoBusTargetResolver(store), "proxy:1")
	guide, err := capabilities.CapabilityGuideForScope(context.Background(), provider, capabilities.GuideScope{
		ManagedProjectID: "project-1",
		ManagedAgentID:   "agent-1",
	}, "internal/dev")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/admin/v1/catalog/dev" || gotAuthorization != "Bearer project-token" {
		t.Fatalf("catalog request path=%q authorization=%q", gotPath, gotAuthorization)
	}
	if content := string(guide); !strings.Contains(content, "x-octobus-capset: internal/dev") || !strings.Contains(content, "# Upstream guide") {
		t.Fatalf("guide = %s", content)
	}
}
