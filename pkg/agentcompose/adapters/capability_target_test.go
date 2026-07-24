package adapters

import (
	"context"
	"errors"
	"strings"
	"testing"

	"agent-compose/pkg/capproxy"
	domain "agent-compose/pkg/model"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type mutableCapabilityAgentStore struct {
	definition domain.AgentDefinition
	err        error
}

func (s *mutableCapabilityAgentStore) GetAgentDefinition(context.Context, string) (domain.AgentDefinition, error) {
	return s.definition, s.err
}

func TestProjectOctoBusTargetResolverUsesLatestDefinitionAndIsolatesToken(t *testing.T) {
	store := &mutableCapabilityAgentStore{definition: projectCapabilityDefinition("https://first.example", "first-token")}
	resolver := NewProjectOctoBusTargetResolver(store)
	binding := capproxy.SandboxBinding{ManagedProjectID: "project-1", ManagedAgentID: "agent-1"}

	first, err := resolver.ResolveCapabilityTarget(context.Background(), binding, "internal/dev")
	if err != nil {
		t.Fatalf("first resolve returned error: %v", err)
	}
	if first.Addr != "https://first.example" || first.Token != "first-token" || first.CapsetID != "dev" {
		t.Fatalf("first target = %#v", first)
	}

	store.definition = projectCapabilityDefinition("https://second.example", "second-token")
	second, err := resolver.ResolveCapabilityTarget(context.Background(), binding, "internal/dev")
	if err != nil {
		t.Fatalf("second resolve returned error: %v", err)
	}
	if second.Addr != "https://second.example" || second.Token != "second-token" {
		t.Fatalf("second target = %#v", second)
	}
}

func TestProjectOctoBusTargetResolverRejectsMissingOrMismatchedScope(t *testing.T) {
	store := &mutableCapabilityAgentStore{definition: projectCapabilityDefinition("https://internal.example", "token")}
	resolver := NewProjectOctoBusTargetResolver(store)
	for _, test := range []struct {
		name    string
		binding capproxy.SandboxBinding
		code    codes.Code
	}{
		{name: "missing scope", binding: capproxy.SandboxBinding{}, code: codes.FailedPrecondition},
		{name: "wrong project", binding: capproxy.SandboxBinding{ManagedProjectID: "project-2", ManagedAgentID: "agent-1"}, code: codes.PermissionDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolver.ResolveCapabilityTarget(context.Background(), test.binding, "internal/dev")
			if status.Code(err) != test.code {
				t.Fatalf("status code = %v, want %v (err %v)", status.Code(err), test.code, err)
			}
		})
	}
}

func TestProjectOctoBusTargetResolverRejectsMissingServerAndStoreFailure(t *testing.T) {
	store := &mutableCapabilityAgentStore{definition: projectCapabilityDefinition("https://internal.example", "token")}
	resolver := NewProjectOctoBusTargetResolver(store)
	binding := capproxy.SandboxBinding{ManagedProjectID: "project-1", ManagedAgentID: "agent-1"}

	_, err := resolver.ResolveCapabilityTarget(context.Background(), binding, "public/dev")
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("missing server status = %v, want %v", status.Code(err), codes.FailedPrecondition)
	}
	store.err = errors.New("database unavailable")
	_, err = resolver.ResolveCapabilityTarget(context.Background(), binding, "internal/dev")
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("store error status = %v, want %v", status.Code(err), codes.Unavailable)
	}
	if status.Convert(err).Message() != "load managed agent capability configuration" {
		t.Fatalf("store error exposed through gRPC = %q", status.Convert(err).Message())
	}
	if !errors.Is(err, store.err) || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("store error cause was not preserved: %v", err)
	}
}

func projectCapabilityDefinition(url, token string) domain.AgentDefinition {
	return domain.AgentDefinition{
		ID:               "agent-1",
		ManagedProjectID: "project-1",
		ConfigJSON:       `{"octobus_servers":{"internal":{"url":"` + url + `","token":"` + token + `"}}}`,
	}
}
