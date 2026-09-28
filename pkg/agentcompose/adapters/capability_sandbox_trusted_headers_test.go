package adapters

import (
	"context"
	"reflect"
	"testing"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type trustedHeaderSandboxStore struct {
	sandbox *domain.Sandbox
}

func (s trustedHeaderSandboxStore) ListSandboxes(context.Context, domain.SandboxListOptions) (domain.SandboxListResult, error) {
	return domain.SandboxListResult{Sandboxes: []*domain.Sandbox{s.sandbox}}, nil
}

func TestCapabilitySandboxTrustedHeadersAreTransient(t *testing.T) {
	sandbox := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:       "sandbox-1",
			VMStatus: domain.VMStatusRunning,
			Tags: []domain.SandboxTag{
				{Name: capabilities.CapsetTagName, Value: "dev"},
			},
		},
		EnvItems: []domain.SandboxEnvVar{
			{Name: capabilities.SandboxTokenEnvName, Value: "sandbox-token", Secret: true},
		},
	}
	resolver := NewCapabilitySandboxResolver(trustedHeaderSandboxStore{sandbox: sandbox})
	if err := resolver.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}

	trusted := []domain.TrustedHeader{
		{Name: "x-mpi-user-id", Value: "user-1"},
		{Name: "x-mpi-role", Value: "admin"},
		{Name: "x-other", Value: "ignored"},
	}
	resolver.IndexSandbox(sandbox, trusted)
	binding, err := resolver.ResolveCapabilitySandbox(context.Background(), "sandbox-token")
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.TrustedHeader{
		{Name: "x-octobus-ext-user-id", Value: "user-1"},
		{Name: "x-octobus-ext-role", Value: "admin"},
	}
	if !reflect.DeepEqual(binding.TrustedHeaders, want) {
		t.Fatalf("trusted headers = %#v, want %#v", binding.TrustedHeaders, want)
	}

	resolver.IndexSandbox(sandbox, nil)
	binding, err = resolver.ResolveCapabilitySandbox(context.Background(), "sandbox-token")
	if err != nil {
		t.Fatal(err)
	}
	if len(binding.TrustedHeaders) != 0 {
		t.Fatalf("ordinary re-index retained trusted headers: %#v", binding.TrustedHeaders)
	}

	resolver.IndexSandbox(sandbox, trusted)
	if err := resolver.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	binding, err = resolver.ResolveCapabilitySandbox(context.Background(), "sandbox-token")
	if err != nil {
		t.Fatal(err)
	}
	if len(binding.TrustedHeaders) != 0 {
		t.Fatalf("resolver rebuild retained trusted headers: %#v", binding.TrustedHeaders)
	}
}

func TestCapabilitySandboxTrustedHeadersConflict(t *testing.T) {
	sandbox := &domain.Sandbox{
		Summary: domain.SandboxSummary{
			ID:       "sandbox-1",
			VMStatus: domain.VMStatusRunning,
			Tags:     []domain.SandboxTag{{Name: capabilities.CapsetTagName, Value: "dev"}},
		},
		EnvItems: []domain.SandboxEnvVar{{Name: capabilities.SandboxTokenEnvName, Value: "sandbox-token", Secret: true}},
	}
	resolver := NewCapabilitySandboxResolver(trustedHeaderSandboxStore{sandbox: sandbox})
	alice := []domain.TrustedHeader{{Name: "x-mpi-user-id", Value: "alice"}}
	bob := []domain.TrustedHeader{{Name: "x-mpi-user-id", Value: "bob"}}

	if resolver.TrustedHeadersConflict("sandbox-1", bob) {
		t.Fatal("unbound sandbox reported a conflict")
	}
	resolver.IndexSandbox(sandbox, alice)
	if resolver.TrustedHeadersConflict("sandbox-1", alice) {
		t.Fatal("same identity reported a conflict")
	}
	// Headers without the trusted prefix are never bound, so they do not
	// distinguish identities.
	if resolver.TrustedHeadersConflict("sandbox-1", append(alice, domain.TrustedHeader{Name: "x-other", Value: "ignored"})) {
		t.Fatal("untrusted header reported a conflict")
	}
	if !resolver.TrustedHeadersConflict("sandbox-1", bob) {
		t.Fatal("different identity did not report a conflict")
	}
	if !resolver.TrustedHeadersConflict("sandbox-1", nil) {
		t.Fatal("anonymous run did not conflict with a bound identity")
	}
	resolver.RevokeSandbox("sandbox-1")
	if resolver.TrustedHeadersConflict("sandbox-1", bob) {
		t.Fatal("revoked sandbox reported a conflict")
	}
}
