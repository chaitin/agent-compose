package capabilities

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chaitin/agent-compose/pkg/capability"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestDynamicProviderCapabilityGuideForScopeCarriesScope(t *testing.T) {
	var gotProject, gotUsername, gotAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProject = r.Header.Get(capability.ProjectScopeHeader)
		gotUsername = r.Header.Get(capability.ExtUsernameHeader)
		gotAuthorization = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("# guide"))
	}))
	defer server.Close()

	provider := NewDynamicProvider(fakeGatewaySource{settings: domain.CapabilityGatewaySettings{Addr: server.URL, Token: "deploy-token"}}, "proxy:1")
	if _, err := provider.CapabilityGuideForScope(context.Background(), GuideScope{
		ProjectID: "project-1",
		AgentID:   "agent-1",
		Username:  "alice",
	}, "dev"); err != nil {
		t.Fatal(err)
	}
	if gotProject != "project-1" || gotUsername != "alice" {
		t.Fatalf("scope headers = %q/%q", gotProject, gotUsername)
	}
	if gotAuthorization != "Bearer deploy-token" {
		t.Fatalf("authorization = %q", gotAuthorization)
	}
}

func TestDynamicProviderCapabilityGuideForScopeOmitsEmptyScope(t *testing.T) {
	var gotProject, gotUsername string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProject = r.Header.Get(capability.ProjectScopeHeader)
		gotUsername = r.Header.Get(capability.ExtUsernameHeader)
		_, _ = w.Write([]byte("# guide"))
	}))
	defer server.Close()

	provider := NewDynamicProvider(fakeGatewaySource{settings: domain.CapabilityGatewaySettings{Addr: server.URL}}, "proxy:1")
	if _, err := provider.CapabilityGuideForScope(context.Background(), GuideScope{AgentID: "agent-1"}, "dev"); err != nil {
		t.Fatal(err)
	}
	if gotProject != "" || gotUsername != "" {
		t.Fatalf("scope headers = %q/%q, want none", gotProject, gotUsername)
	}
}

func TestDynamicProviderCapabilityGuideForScopeRejectsQualifiedDeclaration(t *testing.T) {
	provider := NewDynamicProvider(fakeGatewaySource{settings: domain.CapabilityGatewaySettings{Addr: "http://127.0.0.1:1"}}, "")
	_, err := provider.CapabilityGuideForScope(context.Background(), GuideScope{ProjectID: "project-1"}, "internal/dev")
	if err == nil || !errors.Is(err, errProjectOctoBusNotConfigured) {
		t.Fatalf("error = %v", err)
	}
}
