package runs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/egress"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestResolveNetworkDeclarationFromManagedAgentSpec(t *testing.T) {
	specJSON := `{"name":"worker","sandbox":{"stopped_runtime_policy":"remove","network":{"default":"deny","allow":[{"host":"api.github.com","port":443,"protocol":"https"},{"host":"*.example.com","port":8443,"protocol":"tcp"}]}}}`
	store := &networkDeclarationStoreStub{agents: map[string]domain.ProjectAgentRecord{
		"agent-1": {ID: "agent-1", ProjectID: "project-1", AgentName: "worker", SpecJSON: specJSON},
	}}
	resolver, err := NewSandboxRunTargetResolver(store)
	if err != nil {
		t.Fatalf("NewSandboxRunTargetResolver returned error: %v", err)
	}
	sandbox := sandboxWithTags("sandbox-1", domain.SandboxTag{Name: domain.AgentSandboxTagID, Value: "agent-1"})

	declaration, err := resolver.ResolveNetworkDeclaration(context.Background(), sandbox)
	if err != nil {
		t.Fatalf("ResolveNetworkDeclaration returned error: %v", err)
	}
	if declaration == nil || declaration.Default != egress.Deny {
		t.Fatalf("declaration = %#v, want a declared deny policy", declaration)
	}
	if len(declaration.Allow) != 2 || declaration.Allow[0].Host != "api.github.com" || declaration.Allow[0].Protocol != egress.ProtocolHTTPS {
		t.Fatalf("declaration allow = %#v, want the declared allowances", declaration.Allow)
	}
}

func TestResolveNetworkDeclarationPermissiveDefaultFromPersistedSpec(t *testing.T) {
	// The canonical spec stores the schema's "allow-all" default; the resolver
	// must translate it into the decision model's allow rather than hand the
	// schema value to a compiler that only knows allow/deny.
	for name, specJSON := range map[string]string{
		"omitted default":  `{"name":"worker","sandbox":{"network":{"allow":[{"host":"api.github.com","port":443,"protocol":"https"}]}}}`,
		"explicit default": `{"name":"worker","sandbox":{"network":{"default":"allow-all"}}}`,
		"empty block":      `{"name":"worker","sandbox":{"network":{}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			store := &networkDeclarationStoreStub{agents: map[string]domain.ProjectAgentRecord{
				"agent-1": {ID: "agent-1", ProjectID: "project-1", AgentName: "worker", SpecJSON: specJSON},
			}}
			resolver, err := NewSandboxRunTargetResolver(store)
			if err != nil {
				t.Fatalf("NewSandboxRunTargetResolver returned error: %v", err)
			}
			sandbox := sandboxWithTags("sandbox-1", domain.SandboxTag{Name: domain.AgentSandboxTagID, Value: "agent-1"})

			declaration, err := resolver.ResolveNetworkDeclaration(context.Background(), sandbox)
			if err != nil {
				t.Fatalf("ResolveNetworkDeclaration returned error: %v", err)
			}
			if declaration == nil || declaration.Default != egress.Allow {
				t.Fatalf("declaration = %#v, want a declared allow policy", declaration)
			}
			if err := declaration.Validate(); err != nil {
				t.Fatalf("declaration did not validate: %v", err)
			}
		})
	}
}

func TestResolveNetworkDeclarationUndeclaredCases(t *testing.T) {
	tests := []struct {
		name    string
		sandbox *domain.Sandbox
		agents  map[string]domain.ProjectAgentRecord
	}{
		{
			name:    "no managed agent tag",
			sandbox: sandboxWithTags("sandbox-1", domain.SandboxTag{Name: "project", Value: "project-1"}),
			agents:  map[string]domain.ProjectAgentRecord{"agent-1": {ID: "agent-1", SpecJSON: `{"sandbox":{"network":{"default":"deny"}}}`}},
		},
		{
			name:    "agent record missing",
			sandbox: sandboxWithTags("sandbox-1", domain.SandboxTag{Name: domain.AgentSandboxTagID, Value: "agent-1"}),
			agents:  map[string]domain.ProjectAgentRecord{},
		},
		{
			name:    "agent declares no sandbox block",
			sandbox: sandboxWithTags("sandbox-1", domain.SandboxTag{Name: domain.AgentSandboxTagID, Value: "agent-1"}),
			agents:  map[string]domain.ProjectAgentRecord{"agent-1": {ID: "agent-1", SpecJSON: `{"name":"worker"}`}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver, err := NewSandboxRunTargetResolver(&networkDeclarationStoreStub{agents: tt.agents})
			if err != nil {
				t.Fatalf("NewSandboxRunTargetResolver returned error: %v", err)
			}
			declaration, err := resolver.ResolveNetworkDeclaration(context.Background(), tt.sandbox)
			if err != nil {
				t.Fatalf("ResolveNetworkDeclaration returned error: %v", err)
			}
			if declaration != nil {
				t.Fatalf("declaration = %#v, want nil for an undeclared policy", declaration)
			}
		})
	}
}

func TestResolveNetworkDeclarationStoreError(t *testing.T) {
	resolver, err := NewSandboxRunTargetResolver(&networkDeclarationStoreStub{err: errors.New("store unavailable")})
	if err != nil {
		t.Fatalf("NewSandboxRunTargetResolver returned error: %v", err)
	}
	_, err = resolver.ResolveNetworkDeclaration(context.Background(), sandboxWithTags("sandbox-1", domain.SandboxTag{Name: domain.AgentSandboxTagID, Value: "agent-1"}))
	if err == nil || !strings.Contains(err.Error(), "store unavailable") {
		t.Fatalf("ResolveNetworkDeclaration error = %v, want the store failure", err)
	}
}

type networkDeclarationStoreStub struct {
	agents map[string]domain.ProjectAgentRecord
	err    error
}

func (s *networkDeclarationStoreStub) ListLatestProjectRunsForSandboxes(context.Context, []string) (map[string]domain.ProjectRunRecord, error) {
	return nil, nil
}

func (s *networkDeclarationStoreStub) ListProjectAgentsByIDs(context.Context, []string) (map[string]domain.ProjectAgentRecord, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.agents, nil
}
