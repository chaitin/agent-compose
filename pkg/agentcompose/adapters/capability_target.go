package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/capproxy"
	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ProjectOctoBusTargetResolver struct {
	agents AgentDefinitionStore
}

type ResolvedProjectOctoBusServer struct {
	Server   compose.NormalizedOctoBusServerSpec
	CapsetID string
}

func NewProjectOctoBusTargetResolver(agents AgentDefinitionStore) *ProjectOctoBusTargetResolver {
	return &ProjectOctoBusTargetResolver{agents: agents}
}

// ResolveOctoBusServer reads the current managed agent definition on every
// call. Project re-apply therefore updates running sandboxes consistently with
// the existing managed agent configuration behavior.
func (r *ProjectOctoBusTargetResolver) ResolveOctoBusServer(ctx context.Context, managedProjectID, managedAgentID, declaration string) (ResolvedProjectOctoBusServer, error) {
	managedProjectID = strings.TrimSpace(managedProjectID)
	managedAgentID = strings.TrimSpace(managedAgentID)
	if managedProjectID == "" || managedAgentID == "" {
		return ResolvedProjectOctoBusServer{}, status.Error(codes.FailedPrecondition, "project capability scope is unavailable")
	}
	serverName, capsetID, qualified := strings.Cut(strings.TrimSpace(declaration), "/")
	if !qualified || serverName == "" || capsetID == "" {
		return ResolvedProjectOctoBusServer{}, status.Error(codes.InvalidArgument, "qualified capset declaration is invalid")
	}
	if r == nil || r.agents == nil {
		return ResolvedProjectOctoBusServer{}, status.Error(codes.Unavailable, "project capability configuration is unavailable")
	}
	definition, err := r.agents.GetAgentDefinition(ctx, managedAgentID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return ResolvedProjectOctoBusServer{}, status.Error(codes.NotFound, "managed agent capability configuration was not found")
		}
		return ResolvedProjectOctoBusServer{}, status.Error(codes.Unavailable, "load managed agent capability configuration")
	}
	if strings.TrimSpace(definition.ManagedProjectID) != managedProjectID || strings.TrimSpace(definition.ID) != managedAgentID {
		return ResolvedProjectOctoBusServer{}, status.Error(codes.PermissionDenied, "managed agent capability scope does not match sandbox")
	}
	servers, err := capabilities.AgentOctoBusServers(definition)
	if err != nil {
		return ResolvedProjectOctoBusServer{}, status.Error(codes.FailedPrecondition, "managed agent capability configuration is invalid")
	}
	server, ok := servers[serverName]
	if !ok {
		return ResolvedProjectOctoBusServer{}, status.Error(codes.FailedPrecondition, fmt.Sprintf("octobus server %q is not configured for managed agent", serverName))
	}
	if strings.TrimSpace(server.URL) == "" {
		return ResolvedProjectOctoBusServer{}, status.Error(codes.FailedPrecondition, fmt.Sprintf("octobus server %q has no URL", serverName))
	}
	return ResolvedProjectOctoBusServer{Server: server, CapsetID: capsetID}, nil
}

func (r *ProjectOctoBusTargetResolver) ResolveCapabilityTarget(ctx context.Context, binding capproxy.SandboxBinding, declaration string) (capproxy.Target, error) {
	resolved, err := r.ResolveOctoBusServer(ctx, binding.ManagedProjectID, binding.ManagedAgentID, declaration)
	if err != nil {
		return capproxy.Target{}, err
	}
	return capproxy.Target{Addr: resolved.Server.URL, Token: resolved.Server.Token, CapsetID: resolved.CapsetID}, nil
}
