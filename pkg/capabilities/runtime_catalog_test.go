package capabilities

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/capability"
)

type runtimeCatalogProvider struct {
	Provider
	scopes []GuideScope
}

func (p *runtimeCatalogProvider) CatalogForScope(_ context.Context, scope GuideScope, id string) (capability.Catalog, error) {
	p.scopes = append(p.scopes, scope)
	if id == "offline" {
		return capability.Catalog{}, errors.New("private-upstream-token")
	}
	method := capability.Method{
		InstanceID: "instance-a", MethodFullName: "sample.Service/Echo",
		RequestMessageFullName: "sample.Request", ResponseMessageFullName: "sample.Response",
		Endpoints: []capability.Endpoint{{Protocol: capability.ProtocolGRPC, Metadata: map[string]string{"authorization": "private-upstream-token"}}},
	}
	other := method
	other.InstanceID = "instance-b"
	return capability.Catalog{Methods: []capability.Method{method, method, other}}, nil
}

func TestRuntimeCatalogPreservesRoutingScopeAndSeparatesIdenticalMethods(t *testing.T) {
	provider := &runtimeCatalogProvider{}
	scope := GuideScope{ProjectID: "project", AgentID: "agent", Username: "alice"}
	result := BuildRuntimeCatalog(context.Background(), provider, scope, "sandbox", []string{"one/dev", "two/dev", "one/dev", "offline"})
	if len(result.Tools) != 4 || len(result.Unavailable) != 1 || result.Unavailable[0] != "offline" {
		t.Fatalf("catalog = %#v", result)
	}
	for _, actual := range provider.scopes {
		if actual != scope {
			t.Fatalf("scope = %#v", actual)
		}
	}
	ids := map[string]bool{}
	for _, tool := range result.Tools {
		if ids[tool.ID] || len(tool.ID) != 64 || !strings.Contains(tool.Capset, "/dev") {
			t.Fatalf("tool identity = %#v", tool)
		}
		ids[tool.ID] = true
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-upstream-token") {
		t.Fatal("credential leaked to runtime catalog")
	}
}

func TestRuntimeCatalogDoesNotFallBackToUnscopedDirectory(t *testing.T) {
	result := BuildRuntimeCatalog(context.Background(), nil, GuideScope{Username: "alice"}, "sandbox", []string{"dev"})
	if len(result.Tools) != 0 || len(result.Unavailable) != 1 {
		t.Fatalf("catalog = %#v", result)
	}
}

func TestRuntimeCatalogExcludesEndpointsWithoutGRPCOrMessageTypes(t *testing.T) {
	method := capability.Method{InstanceID: "instance", MethodFullName: "sample.Service/Call", RequestMessageFullName: "Request", ResponseMessageFullName: "Response"}
	if _, ok := runtimeTool("dev", method); ok {
		t.Fatal("non-gRPC method included")
	}
	method.Endpoints = []capability.Endpoint{{Protocol: capability.ProtocolGRPC}}
	method.RequestMessageFullName = ""
	if _, ok := runtimeTool("dev", method); ok {
		t.Fatal("method without request type included")
	}
}
