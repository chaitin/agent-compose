package runs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
)

type SandboxRunTarget struct {
	ProjectID string
	AgentName string
}

type SandboxRunTargetStore interface {
	ListLatestProjectRunsForSandboxes(context.Context, []string) (map[string]domain.ProjectRunRecord, error)
	ListProjectAgentsByManagedAgentIDs(context.Context, []string) (map[string]domain.ProjectAgentRecord, error)
	ListProjectAgents(context.Context, string) ([]domain.ProjectAgentRecord, error)
}

type SandboxRunTargetResolver struct {
	store                  SandboxRunTargetStore
	legacyDefaultProjectID string
}

func NewSandboxRunTargetResolver(store SandboxRunTargetStore) (*SandboxRunTargetResolver, error) {
	if store == nil {
		return nil, fmt.Errorf("sandbox run target store is required")
	}
	projectID, err := domain.StableProjectID(projects.LegacyDefaultProjectName, "")
	if err != nil {
		return nil, fmt.Errorf("resolve legacy default project id: %w", err)
	}
	return &SandboxRunTargetResolver{store: store, legacyDefaultProjectID: projectID}, nil
}

func (r *SandboxRunTargetResolver) Resolve(ctx context.Context, sandbox *domain.Sandbox) (SandboxRunTarget, error) {
	resolved, err := r.ResolveBatch(ctx, []*domain.Sandbox{sandbox})
	if err != nil || sandbox == nil {
		return SandboxRunTarget{}, err
	}
	return resolved[sandbox.Summary.ID], nil
}

func (r *SandboxRunTargetResolver) ResolveBatch(ctx context.Context, sandboxes []*domain.Sandbox) (map[string]SandboxRunTarget, error) {
	result := make(map[string]SandboxRunTarget, len(sandboxes))
	sandboxIDs := make([]string, 0, len(sandboxes))
	managedAgentIDs := make([]string, 0, len(sandboxes))
	for _, sandbox := range sandboxes {
		if sandbox == nil {
			continue
		}
		sandboxIDs = append(sandboxIDs, sandbox.Summary.ID)
		if id := sandboxTagValue(sandbox, domain.AgentSandboxTagID); id != "" {
			managedAgentIDs = append(managedAgentIDs, id)
		}
	}
	runsBySandbox, err := r.store.ListLatestProjectRunsForSandboxes(ctx, sandboxIDs)
	if err != nil {
		return nil, err
	}
	agentsByManagedID, err := r.store.ListProjectAgentsByManagedAgentIDs(ctx, managedAgentIDs)
	if err != nil {
		return nil, err
	}
	unresolved := make([]*domain.Sandbox, 0, len(sandboxes))
	for _, sandbox := range sandboxes {
		if sandbox == nil {
			continue
		}
		id := sandbox.Summary.ID
		if run, ok := runsBySandbox[id]; ok {
			result[id] = SandboxRunTarget{ProjectID: run.ProjectID, AgentName: run.AgentName}
			continue
		}
		projectID := sandboxTagValue(sandbox, "project")
		if projectID == "" {
			projectID = sandboxTagValue(sandbox, "project_id")
		}
		if agentName := sandboxTagValue(sandbox, "agent"); projectID != "" && agentName != "" {
			result[id] = SandboxRunTarget{ProjectID: projectID, AgentName: agentName}
			continue
		}
		if agent, ok := agentsByManagedID[sandboxTagValue(sandbox, domain.AgentSandboxTagID)]; ok {
			result[id] = SandboxRunTarget{ProjectID: agent.ProjectID, AgentName: agent.AgentName}
			continue
		}
		unresolved = append(unresolved, sandbox)
	}
	if len(unresolved) == 0 {
		return result, nil
	}
	legacyAgents, err := r.store.ListProjectAgents(ctx, r.legacyDefaultProjectID)
	if err != nil {
		slog.Warn("failed to list legacy default project agents while resolving sandbox targets", "error", err)
		return result, nil
	}
	legacyByName := make(map[string]domain.ProjectAgentRecord, len(legacyAgents))
	for _, agent := range legacyAgents {
		legacyByName[strings.ToLower(strings.TrimSpace(agent.AgentName))] = agent
	}
	for _, sandbox := range unresolved {
		id := sandbox.Summary.ID
		if agent, ok := legacyByName[strings.ToLower(sandboxTagValue(sandbox, domain.AgentSandboxTagName))]; ok {
			result[id] = SandboxRunTarget{ProjectID: agent.ProjectID, AgentName: agent.AgentName}
		}
	}
	return result, nil
}

func sandboxTagValue(sandbox *domain.Sandbox, name string) string {
	if sandbox == nil {
		return ""
	}
	for _, tag := range sandbox.Summary.Tags {
		if strings.TrimSpace(tag.Name) == name {
			return strings.TrimSpace(tag.Value)
		}
	}
	return ""
}
