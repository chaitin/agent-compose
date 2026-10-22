package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	"github.com/chaitin/agent-compose/pkg/capability"
	"github.com/chaitin/agent-compose/pkg/capproxy"
	"github.com/chaitin/agent-compose/pkg/compose"
	domain "github.com/chaitin/agent-compose/pkg/model"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ProjectOctoBusTargetResolver struct {
	agents AgentDefinitionStore
}

// ProjectOctoBusTokenSource supplies a project server's daemon-managed bearer
// token. Implementations return an empty string when no token is set.
type ProjectOctoBusTokenSource interface {
	GetProjectOctoBusServerToken(context.Context, string, string) (string, error)
}

type ResolvedProjectOctoBusServer struct {
	Server   compose.NormalizedOctoBusServerSpec
	CapsetID string
}

type capabilityResolutionError struct {
	status *status.Status
	cause  error
}

func (e *capabilityResolutionError) Error() string {
	return e.status.Message() + ": " + e.cause.Error()
}

func (e *capabilityResolutionError) Unwrap() error {
	return e.cause
}

func (e *capabilityResolutionError) GRPCStatus() *status.Status {
	return e.status
}

func NewProjectOctoBusTargetResolver(agents AgentDefinitionStore) *ProjectOctoBusTargetResolver {
	return &ProjectOctoBusTargetResolver{agents: agents}
}

// ResolveOctoBusServer reads the current project agent definition on every
// call. Project re-apply therefore updates running sandboxes consistently with
// the existing project agent configuration behavior.
func (r *ProjectOctoBusTargetResolver) ResolveOctoBusServer(ctx context.Context, projectID, projectAgentID, declaration string) (ResolvedProjectOctoBusServer, error) {
	projectID = strings.TrimSpace(projectID)
	projectAgentID = strings.TrimSpace(projectAgentID)
	if projectID == "" || projectAgentID == "" {
		return ResolvedProjectOctoBusServer{}, status.Error(codes.FailedPrecondition, "project capability scope is unavailable")
	}
	parsed, err := capability.ParseCapsetDeclaration(declaration)
	if err != nil || !parsed.Qualified() {
		return ResolvedProjectOctoBusServer{}, status.Error(codes.InvalidArgument, "qualified capset declaration is invalid")
	}
	if r == nil || r.agents == nil {
		return ResolvedProjectOctoBusServer{}, status.Error(codes.Unavailable, "project capability configuration is unavailable")
	}
	definition, err := r.agents.GetAgentDefinition(ctx, projectAgentID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return ResolvedProjectOctoBusServer{}, status.Error(codes.NotFound, "managed agent capability configuration was not found")
		}
		return ResolvedProjectOctoBusServer{}, &capabilityResolutionError{
			status: status.New(codes.Unavailable, "load managed agent capability configuration"),
			cause:  err,
		}
	}
	if strings.TrimSpace(definition.ProjectID) != projectID || strings.TrimSpace(definition.ID) != projectAgentID {
		return ResolvedProjectOctoBusServer{}, status.Error(codes.PermissionDenied, "managed agent capability scope does not match sandbox")
	}
	servers, err := capabilities.AgentOctoBusServers(definition)
	if err != nil {
		return ResolvedProjectOctoBusServer{}, status.Error(codes.FailedPrecondition, "managed agent capability configuration is invalid")
	}
	server, ok := servers[parsed.ServerName]
	if !ok {
		return ResolvedProjectOctoBusServer{}, status.Error(codes.FailedPrecondition, fmt.Sprintf("octobus server %q is not configured for managed agent", parsed.ServerName))
	}
	if strings.TrimSpace(server.URL) == "" {
		return ResolvedProjectOctoBusServer{}, status.Error(codes.FailedPrecondition, fmt.Sprintf("octobus server %q has no URL", parsed.ServerName))
	}
	if server.TokenSource == compose.OctoBusTokenSourceDaemon {
		tokenStore, ok := r.agents.(ProjectOctoBusTokenSource)
		if !ok {
			return ResolvedProjectOctoBusServer{}, status.Error(codes.Unavailable, "project octobus token store is not configured")
		}
		token, err := tokenStore.GetProjectOctoBusServerToken(ctx, projectID, parsed.ServerName)
		if err != nil {
			return ResolvedProjectOctoBusServer{}, &capabilityResolutionError{
				status: status.New(codes.Unavailable, "load project octobus server token"),
				cause:  err,
			}
		}
		server.Token = token
	}
	return ResolvedProjectOctoBusServer{Server: server, CapsetID: parsed.CapsetID}, nil
}

func (r *ProjectOctoBusTargetResolver) ResolveCapabilityTarget(ctx context.Context, binding capproxy.SandboxBinding, declaration string) (capproxy.Target, error) {
	resolved, err := r.ResolveOctoBusServer(ctx, binding.ProjectID, binding.AgentID, declaration)
	if err != nil {
		return capproxy.Target{}, err
	}
	return capproxy.Target{Addr: resolved.Server.URL, Token: resolved.Server.Token, CapsetID: resolved.CapsetID}, nil
}
