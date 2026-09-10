package api

import (
	"fmt"

	"github.com/chaitin/agent-compose/pkg/compose"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func workspaceModeToProto(raw string) (agentcomposev2.WorkspaceMode, error) {
	mode, err := domain.NormalizeWorkspaceMode(raw)
	if err != nil {
		return agentcomposev2.WorkspaceMode_WORKSPACE_MODE_UNSPECIFIED, err
	}
	if mode == domain.WorkspaceModeMount {
		return agentcomposev2.WorkspaceMode_WORKSPACE_MODE_MOUNT, nil
	}
	return agentcomposev2.WorkspaceMode_WORKSPACE_MODE_UNSPECIFIED, nil
}

func workspaceModeFromProto(mode agentcomposev2.WorkspaceMode) (string, error) {
	switch mode {
	case agentcomposev2.WorkspaceMode_WORKSPACE_MODE_UNSPECIFIED:
		return "", nil
	case agentcomposev2.WorkspaceMode_WORKSPACE_MODE_COPY:
		return domain.WorkspaceModeCopy, nil
	case agentcomposev2.WorkspaceMode_WORKSPACE_MODE_MOUNT:
		return domain.WorkspaceModeMount, nil
	default:
		return "", fmt.Errorf("unsupported workspace mode %d", mode)
	}
}

func validateProjectWorkspaceModes(spec *compose.NormalizedProjectSpec) error {
	for name, workspace := range spec.Workspaces {
		if _, err := workspaceModeToProto(workspace.Mode); err != nil {
			return fmt.Errorf("workspaces.%s.mode: %w", name, err)
		}
	}
	for _, agent := range spec.Agents {
		if agent.Workspace == nil {
			continue
		}
		if _, err := workspaceModeToProto(agent.Workspace.Mode); err != nil {
			return fmt.Errorf("agents.%s.workspace.mode: %w", agent.Name, err)
		}
	}
	return nil
}
