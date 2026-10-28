package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	"github.com/chaitin/agent-compose/pkg/capability"
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
		ProjectID: "project-1",
		AgentID:   "agent-1",
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

type failIfResolved struct{ t *testing.T }

func (f failIfResolved) ResolveOctoBusServer(context.Context, string, string, string) (ResolvedProjectOctoBusServer, error) {
	f.t.Fatal("project OctoBus resolver must not be used for an unqualified capset")
	return ResolvedProjectOctoBusServer{}, nil
}

func TestProjectAwareCapabilityProviderRoutesUnqualifiedGuideThroughGatewayWithScope(t *testing.T) {
	var gotPath, gotProject, gotUsername string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotProject = r.Header.Get(capability.ProjectScopeHeader)
		gotUsername = r.Header.Get(capability.ExtUsernameHeader)
		_, _ = w.Write([]byte("# Gateway guide"))
	}))
	defer server.Close()

	provider := NewCapabilityProvider(staticGatewaySource{addr: server.URL}, failIfResolved{t: t}, "proxy:1")
	guide, err := capabilities.CapabilityGuideForScope(context.Background(), provider, capabilities.GuideScope{
		ProjectID: "project-1",
		AgentID:   "agent-1",
		Username:  "alice",
	}, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/admin/v1/catalog/legacy" {
		t.Fatalf("catalog request path = %q", gotPath)
	}
	if gotProject != "project-1" || gotUsername != "alice" {
		t.Fatalf("scope headers = %q/%q", gotProject, gotUsername)
	}
	if string(guide) != "# Gateway guide" {
		t.Fatalf("guide = %q", guide)
	}
}
