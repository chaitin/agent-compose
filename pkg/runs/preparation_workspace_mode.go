package runs

import (
	"encoding/json"
	"fmt"

	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func workspaceModeText(mode agentcomposev2.WorkspaceMode) (string, error) {
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

// storedWorkspaceMode accepts canonical authoring strings and the enum JSON
// representation used by API-shaped historical revision snapshots.
func storedWorkspaceMode(raw json.RawMessage) (agentcomposev2.WorkspaceMode, error) {
	if len(raw) == 0 {
		return agentcomposev2.WorkspaceMode_WORKSPACE_MODE_UNSPECIFIED, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return agentcomposev2.WorkspaceMode_WORKSPACE_MODE_UNSPECIFIED, err
	}
	return revisionWorkspaceMode(value)
}

func revisionWorkspaceMode(value any) (agentcomposev2.WorkspaceMode, error) {
	switch mode := value.(type) {
	case nil:
		return agentcomposev2.WorkspaceMode_WORKSPACE_MODE_UNSPECIFIED, nil
	case string:
		switch mode {
		case "", "WORKSPACE_MODE_UNSPECIFIED":
			return agentcomposev2.WorkspaceMode_WORKSPACE_MODE_UNSPECIFIED, nil
		case "copy", "WORKSPACE_MODE_COPY":
			return agentcomposev2.WorkspaceMode_WORKSPACE_MODE_COPY, nil
		case "mount", "WORKSPACE_MODE_MOUNT":
			return agentcomposev2.WorkspaceMode_WORKSPACE_MODE_MOUNT, nil
		}
	case float64:
		for _, candidate := range []agentcomposev2.WorkspaceMode{
			agentcomposev2.WorkspaceMode_WORKSPACE_MODE_UNSPECIFIED,
			agentcomposev2.WorkspaceMode_WORKSPACE_MODE_COPY,
			agentcomposev2.WorkspaceMode_WORKSPACE_MODE_MOUNT,
		} {
			if mode == float64(candidate) {
				return candidate, nil
			}
		}
	}
	return agentcomposev2.WorkspaceMode_WORKSPACE_MODE_UNSPECIFIED, fmt.Errorf("unknown workspace mode %v", value)
}

func normalizeRevisionWorkspaceModes(root map[string]any) error {
	workspaces, _ := root["workspaces"].([]any)
	for index, value := range workspaces {
		workspace, _ := value.(map[string]any)
		if nested, ok := workspace["workspace"].(map[string]any); ok {
			workspace = nested
		}
		if err := normalizeRevisionWorkspaceMode(workspace); err != nil {
			return fmt.Errorf("workspaces[%d].mode: %w", index, err)
		}
	}
	agents, _ := root["agents"].([]any)
	for index, value := range agents {
		agent, _ := value.(map[string]any)
		workspace, _ := agent["workspace"].(map[string]any)
		if err := normalizeRevisionWorkspaceMode(workspace); err != nil {
			return fmt.Errorf("agents[%d].workspace.mode: %w", index, err)
		}
	}
	return nil
}

func normalizeRevisionWorkspaceMode(workspace map[string]any) error {
	value, present := workspace["mode"]
	if !present {
		return nil
	}
	mode, err := revisionWorkspaceMode(value)
	if err != nil {
		return err
	}
	workspace["mode"] = int32(mode)
	return nil
}
