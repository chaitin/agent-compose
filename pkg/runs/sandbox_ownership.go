package runs

import (
	"fmt"
	"sort"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

type sandboxRunOwnership struct {
	ProjectID string
	AgentName string
	AgentID   string
}

func validateProjectRunSandboxOwnership(sandbox *domain.Sandbox, run domain.ProjectRunRecord) error {
	if sandbox == nil {
		return nil
	}
	ownership, err := projectRunSandboxOwnership(sandbox)
	if err != nil {
		return fmt.Errorf("%w: sandbox %s has conflicting ownership metadata: %w", ErrInvalidRequest, sandbox.Summary.ID, err)
	}
	if ownership.ProjectID != "" && ownership.ProjectID != strings.TrimSpace(run.ProjectID) {
		return projectRunSandboxOwnershipMismatch(sandbox.Summary.ID, ownership, run)
	}
	if ownership.AgentName != "" && ownership.AgentName != strings.TrimSpace(run.AgentName) {
		return projectRunSandboxOwnershipMismatch(sandbox.Summary.ID, ownership, run)
	}
	if ownership.AgentID != "" && strings.TrimSpace(run.AgentID) != "" && ownership.AgentID != strings.TrimSpace(run.AgentID) {
		return projectRunSandboxOwnershipMismatch(sandbox.Summary.ID, ownership, run)
	}
	return nil
}

func projectRunSandboxOwnership(sandbox *domain.Sandbox) (sandboxRunOwnership, error) {
	if sandbox == nil {
		return sandboxRunOwnership{}, nil
	}
	projectID, err := uniqueSandboxTagValue(sandbox.Summary.Tags, "project", "project_id")
	if err != nil {
		return sandboxRunOwnership{}, fmt.Errorf("project: %w", err)
	}
	agentName, err := uniqueSandboxTagValue(sandbox.Summary.Tags, "agent")
	if err != nil {
		return sandboxRunOwnership{}, fmt.Errorf("agent: %w", err)
	}
	agentID, err := uniqueSandboxTagValue(sandbox.Summary.Tags, domain.AgentSandboxTagID)
	if err != nil {
		return sandboxRunOwnership{}, fmt.Errorf("agent id: %w", err)
	}
	return sandboxRunOwnership{ProjectID: projectID, AgentName: agentName, AgentID: agentID}, nil
}

func uniqueSandboxTagValue(tags []domain.SandboxTag, names ...string) (string, error) {
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		wanted[name] = struct{}{}
	}
	values := make(map[string]struct{})
	for _, tag := range tags {
		name := strings.TrimSpace(tag.Name)
		if _, ok := wanted[name]; !ok {
			continue
		}
		value := strings.TrimSpace(tag.Value)
		if value != "" {
			values[value] = struct{}{}
		}
	}
	if len(values) == 0 {
		return "", nil
	}
	if len(values) == 1 {
		for value := range values {
			return value, nil
		}
	}
	conflicts := make([]string, 0, len(values))
	for value := range values {
		conflicts = append(conflicts, value)
	}
	sort.Strings(conflicts)
	return "", fmt.Errorf("multiple values %q", conflicts)
}

func projectRunSandboxOwnershipMismatch(sandboxID string, ownership sandboxRunOwnership, run domain.ProjectRunRecord) error {
	owner := sandboxRunOwnershipDescription(ownership)
	requested := sandboxRunOwnershipDescription(sandboxRunOwnership{
		ProjectID: strings.TrimSpace(run.ProjectID),
		AgentName: strings.TrimSpace(run.AgentName),
		AgentID:   strings.TrimSpace(run.AgentID),
	})
	return fmt.Errorf("%w: sandbox %s belongs to %s; cannot reuse it for %s", ErrInvalidRequest, sandboxID, owner, requested)
}

func sandboxRunOwnershipDescription(ownership sandboxRunOwnership) string {
	parts := make([]string, 0, 3)
	if ownership.ProjectID != "" {
		parts = append(parts, fmt.Sprintf("project %q", ownership.ProjectID))
	}
	if ownership.AgentName != "" {
		parts = append(parts, fmt.Sprintf("agent %q", ownership.AgentName))
	}
	if ownership.AgentID != "" {
		parts = append(parts, fmt.Sprintf("agent ID %q", ownership.AgentID))
	}
	if len(parts) == 0 {
		return "an unowned sandbox"
	}
	return strings.Join(parts, " ")
}
