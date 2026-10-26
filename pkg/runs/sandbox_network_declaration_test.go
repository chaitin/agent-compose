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
	specJSON := `{"name":"worker","policy":{"stopped_runtime_policy":"remove","network":{"default":"deny-all","allow":[{"host":"api.github.com","port":443},{"host":"*.example.com","port":8443}]}}}`
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
	if len(declaration.Allow) != 2 || declaration.Allow[0].Host != "api.github.com" {
		t.Fatalf("declaration allow = %#v, want the declared allowances", declaration.Allow)
	}
}

func TestResolveNetworkDeclarationAcceptsDeprecatedSandboxKey(t *testing.T) {
	// Managed agents persisted before the policy block was renamed to `policy`
	// still store their declaration under the deprecated `sandbox` key.
	// Decoding such a spec as undeclared would report an unrestricted network
	// policy for an agent that declared a restricted one.
	const legacySpecJSON = `{"name":"worker","sandbox":{"stopped_runtime_policy":"retain","network":{"default":"deny-all","allow":[{"host":"api.github.com","port":443}]}}}`
	resolver, err := NewSandboxRunTargetResolver(&networkDeclarationStoreStub{agents: map[string]domain.ProjectAgentRecord{
		"agent-1": {ID: "agent-1", ProjectID: "project-1", AgentName: "worker", SpecJSON: legacySpecJSON},
	}})
	if err != nil {
		t.Fatalf("NewSandboxRunTargetResolver returned error: %v", err)
	}
	sandbox := sandboxWithTags("sandbox-1", domain.SandboxTag{Name: domain.AgentSandboxTagID, Value: "agent-1"})

	declaration, err := resolver.ResolveNetworkDeclaration(context.Background(), sandbox)
	if err != nil {
		t.Fatalf("ResolveNetworkDeclaration returned error: %v", err)
	}
	if declaration == nil || declaration.Default != egress.Deny {
		t.Fatalf("declaration = %#v, want the deny policy stored under the deprecated key", declaration)
	}
	if len(declaration.Allow) != 1 || declaration.Allow[0].Host != "api.github.com" {
		t.Fatalf("declaration allow = %#v, want the allowance stored under the deprecated key", declaration.Allow)
	}
}

func TestResolveNetworkDeclarationPermissiveDefaultFromPersistedSpec(t *testing.T) {
	// The canonical spec stores the schema's "allow-all" default; the resolver
	// must translate it into the decision model's allow rather than hand the
	// schema value to a compiler that only knows allow/deny.
	for name, specJSON := range map[string]string{
		"omitted default":  `{"name":"worker","policy":{"network":{"allow":[{"host":"api.github.com","port":443}]}}}`,
		"explicit default": `{"name":"worker","policy":{"network":{"default":"allow-all"}}}`,
		"empty block":      `{"name":"worker","policy":{"network":{}}}`,
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
			agents:  map[string]domain.ProjectAgentRecord{"agent-1": {ID: "agent-1", SpecJSON: `{"policy":{"network":{"default":"deny-all"}}}`}},
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

func TestResolveNetworkDeclarationRejectsRemovedProtocolInPersistedSpec(t *testing.T) {
	// Canonical specs written before protocol was removed still contain it.
	// Decoding such a spec as any protocol would silently widen a named
	// protocol allowance, so resolution must fail closed and tell the operator
	// which stored entry is stale.
	tests := map[string]string{
		"policy allow":    `{"name":"worker","policy":{"network":{"default":"deny-all","allow":[{"host":"api.example.com","port":80,"protocol":"https"}]}}}`,
		"deprecated deny": `{"name":"worker","sandbox":{"network":{"deny":[{"host":"metadata.example.com","port":80,"protocol":"tcp"}]}}}`,
	}
	for name, specJSON := range tests {
		t.Run(name, func(t *testing.T) {
			resolver, err := NewSandboxRunTargetResolver(&networkDeclarationStoreStub{agents: map[string]domain.ProjectAgentRecord{
				"agent-1": {ID: "agent-1", ProjectID: "project-1", AgentName: "worker", SpecJSON: specJSON},
			}})
			if err != nil {
				t.Fatalf("NewSandboxRunTargetResolver returned error: %v", err)
			}
			sandbox := sandboxWithTags("sandbox-1", domain.SandboxTag{Name: domain.AgentSandboxTagID, Value: "agent-1"})

			_, err = resolver.ResolveNetworkDeclaration(context.Background(), sandbox)
			if err == nil {
				t.Fatal("ResolveNetworkDeclaration accepted a persisted spec with the removed protocol field")
			}
			if !strings.Contains(err.Error(), "protocol") || !strings.Contains(err.Error(), "removed network entry protocol") {
				t.Fatalf("error = %q, want the protocol migration message", err)
			}
		})
	}
}

func TestResolveNetworkDeclarationAcceptsAnyProtocolInPersistedSpec(t *testing.T) {
	// Every canonical spec written before the removal carries protocol, and the
	// field default "any" leaves the entry's meaning unchanged. Rejecting it
	// would break every upgrade instead of only entries that narrowed the
	// proxy scheme.
	tests := map[string]string{
		"any":       `{"name":"worker","policy":{"network":{"default":"deny-all","allow":[{"host":"api.example.com","port":80,"protocol":"any"}]}}}`,
		"uppercase": `{"name":"worker","policy":{"network":{"default":"deny-all","allow":[{"host":"api.example.com","port":80,"protocol":"ANY"}]}}}`,
	}
	for name, specJSON := range tests {
		t.Run(name, func(t *testing.T) {
			resolver, err := NewSandboxRunTargetResolver(&networkDeclarationStoreStub{agents: map[string]domain.ProjectAgentRecord{
				"agent-1": {ID: "agent-1", ProjectID: "project-1", AgentName: "worker", SpecJSON: specJSON},
			}})
			if err != nil {
				t.Fatalf("NewSandboxRunTargetResolver returned error: %v", err)
			}
			sandbox := sandboxWithTags("sandbox-1", domain.SandboxTag{Name: domain.AgentSandboxTagID, Value: "agent-1"})

			declaration, err := resolver.ResolveNetworkDeclaration(context.Background(), sandbox)
			if err != nil {
				t.Fatalf("ResolveNetworkDeclaration returned error: %v", err)
			}
			if declaration == nil || len(declaration.Allow) != 1 || declaration.Allow[0].Protocol != egress.ProtocolAny {
				t.Fatalf("declaration = %#v, want the entry with any protocol", declaration)
			}
		})
	}
}

func TestResolveNetworkDeclarationScansPastAnyProtocolInPersistedSpec(t *testing.T) {
	// Old canonical specs carry protocol on every entry. The first entry's
	// default "any" must not stop the scan before a later named value, or that
	// later entry would silently widen to any protocol.
	const specJSON = `{"name":"worker","policy":{"network":{"default":"deny-all","allow":[{"host":"api.example.com","port":80,"protocol":"any"},{"host":"api.example.com","port":443,"protocol":"https"}]}}}`
	resolver, err := NewSandboxRunTargetResolver(&networkDeclarationStoreStub{agents: map[string]domain.ProjectAgentRecord{
		"agent-1": {ID: "agent-1", ProjectID: "project-1", AgentName: "worker", SpecJSON: specJSON},
	}})
	if err != nil {
		t.Fatalf("NewSandboxRunTargetResolver returned error: %v", err)
	}
	sandbox := sandboxWithTags("sandbox-1", domain.SandboxTag{Name: domain.AgentSandboxTagID, Value: "agent-1"})

	_, err = resolver.ResolveNetworkDeclaration(context.Background(), sandbox)
	if err == nil {
		t.Fatal("ResolveNetworkDeclaration accepted a later named protocol after an any entry")
	}
	if !strings.Contains(err.Error(), "policy.network.allow[1].protocol") {
		t.Fatalf("error = %q, want the later named entry's path", err)
	}
}
