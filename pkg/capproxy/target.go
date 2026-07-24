package capproxy

import (
	"context"
	"strings"

	"agent-compose/pkg/capability"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Target describes one resolved OctoBus upstream. CapsetID is the real
// OctoBus capset ID, without agent-compose's project server qualifier.
type Target struct {
	Addr     string
	Token    string
	CapsetID string
}

// TargetResolver resolves a qualified capset declaration for an authenticated
// sandbox. Implementations may use the binding's managed project and agent
// identity to select project-scoped configuration. The declaration has already
// been checked against binding.CapsetIDs before this method is called.
type TargetResolver interface {
	ResolveCapabilityTarget(ctx context.Context, binding SandboxBinding, declaration string) (Target, error)
}

func (s *Server) resolveTarget(ctx context.Context, binding SandboxBinding, declaration string) (Target, error) {
	parsed, err := capability.ParseCapsetDeclaration(declaration)
	if err != nil {
		return Target{}, status.Error(codes.InvalidArgument, err.Error())
	}
	if !parsed.Qualified() {
		addr, token, ok := s.octobus(ctx)
		if !ok {
			return Target{}, status.Error(codes.Unavailable, "capability gateway is not configured")
		}
		return Target{Addr: addr, Token: token, CapsetID: declaration}, nil
	}
	if s.targets == nil {
		return Target{}, status.Error(codes.Unavailable, "project capability gateway is not configured")
	}
	target, err := s.targets.ResolveCapabilityTarget(ctx, binding, declaration)
	if err != nil {
		return Target{}, err
	}
	if strings.TrimSpace(target.Addr) == "" || strings.TrimSpace(target.CapsetID) == "" {
		return Target{}, status.Error(codes.Unavailable, "project capability gateway target is not configured")
	}
	return target, nil
}
