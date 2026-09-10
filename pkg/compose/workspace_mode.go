package compose

import domain "github.com/chaitin/agent-compose/pkg/model"

func normalizeWorkspaceDelivery(path string, workspace *WorkspaceSpec) error {
	mode, err := domain.NormalizeWorkspaceDelivery(workspace.Provider, workspace.Mode, workspace.ReadOnly)
	if err != nil {
		field := ".mode"
		if normalized, modeErr := domain.NormalizeWorkspaceMode(workspace.Mode); modeErr == nil && normalized != domain.WorkspaceModeMount && workspace.ReadOnly {
			field = ".read_only"
		}
		return &ValidationError{Path: path + field, Message: err.Error()}
	}
	workspace.Mode = mode
	return nil
}
