package projects

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/workspaces"
)

// bindLegacyFileWorkspaces restores the v1 preset binding carried by the
// synthetic project's canonical local-workspace path. Ordinary v2 projects
// continue to materialize local workspaces from their project source.
func (c *Controller) bindLegacyFileWorkspaces(ctx context.Context, project domain.ProjectRecord, spec *compose.NormalizedProjectSpec, definitions []domain.AgentDefinition) error {
	if !IsLegacyDefaultProject(project) || spec == nil || len(definitions) == 0 {
		return nil
	}
	agents := make(map[string]compose.NormalizedAgentSpec, len(spec.Agents))
	for _, agent := range spec.Agents {
		agents[agent.Name] = agent
	}
	type binding struct {
		definitionIndex int
		agentName       string
		workspaceID     string
	}
	bindings := make([]binding, 0, len(definitions))
	for index := range definitions {
		agent, ok := agents[definitions[index].ManagedAgentName]
		if !ok {
			continue
		}
		workspaceID, ok := legacyFileWorkspaceID(spec, agent)
		if !ok {
			continue
		}
		bindings = append(bindings, binding{definitionIndex: index, agentName: agent.Name, workspaceID: workspaceID})
	}
	if len(bindings) == 0 {
		return nil
	}
	if c == nil || c.config == nil {
		return fmt.Errorf("bind legacy file workspaces: config is required")
	}
	store, ok := c.store.(legacyWorkspaceConfigStore)
	if !ok {
		return fmt.Errorf("bind legacy file workspaces: workspace config store is required")
	}
	loaded := make(map[string]domain.WorkspaceConfig, len(bindings))
	for _, binding := range bindings {
		workspaceID := binding.workspaceID
		workspace, found := loaded[workspaceID]
		if !found {
			var err error
			workspace, err = store.GetWorkspaceConfig(ctx, workspaceID)
			if err != nil {
				return fmt.Errorf("bind legacy agent %s workspace %s: %w", binding.agentName, workspaceID, err)
			}
			if strings.ToLower(strings.TrimSpace(workspace.Type)) != "file" {
				return fmt.Errorf("bind legacy agent %s workspace %s: workspace type is %q, want file", binding.agentName, workspaceID, workspace.Type)
			}
			if _, err := workspaces.FileWorkspaceContentRoot(c.config, workspace); err != nil {
				return fmt.Errorf("bind legacy agent %s workspace %s: %w", binding.agentName, workspaceID, err)
			}
			loaded[workspaceID] = workspace
		}
		definitions[binding.definitionIndex].WorkspaceID = workspace.ID
	}
	return nil
}

// IsLegacyDefaultProject reports whether project is the canonical synthetic
// project created by the v1 compatibility migration.
func IsLegacyDefaultProject(project domain.ProjectRecord) bool {
	if strings.TrimSpace(project.Name) != LegacyDefaultProjectName || strings.TrimSpace(project.SourcePath) != "" {
		return false
	}
	projectID, err := domain.StableProjectID(LegacyDefaultProjectName, "")
	return err == nil && strings.TrimSpace(project.ID) == projectID
}

func legacyFileWorkspaceID(spec *compose.NormalizedProjectSpec, agent compose.NormalizedAgentSpec) (string, bool) {
	workspace := resolvedLegacyWorkspace(spec, agent.Workspace)
	return LegacyFileWorkspacePresetID(workspace)
}

// LegacyFileWorkspacePresetID returns the original v1 file-workspace preset ID
// encoded by the canonical migration-only local workspace path.
func LegacyFileWorkspacePresetID(workspace *compose.WorkspaceSpec) (string, bool) {
	if workspace == nil || strings.ToLower(strings.TrimSpace(workspace.Provider)) != "local" {
		return "", false
	}
	path := filepath.ToSlash(filepath.Clean(strings.TrimSpace(workspace.Path)))
	prefix := "workspaces/"
	suffix := "/" + workspaces.FileWorkspaceContentDirName
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	workspaceID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	expected, err := workspaces.FileWorkspaceContentRelRoot(workspaceID)
	if err != nil || filepath.ToSlash(filepath.Clean(expected)) != path {
		return "", false
	}
	return workspaceID, true
}

func resolvedLegacyWorkspace(spec *compose.NormalizedProjectSpec, reference *compose.WorkspaceSpec) *compose.WorkspaceSpec {
	if reference == nil {
		if len(spec.Workspaces) != 1 {
			return nil
		}
		for _, workspace := range spec.Workspaces {
			resolved := workspace
			return &resolved
		}
	}
	if strings.TrimSpace(reference.Provider) != "" || strings.TrimSpace(reference.URL) != "" || strings.TrimSpace(reference.Branch) != "" || strings.TrimSpace(reference.Commit) != "" || strings.TrimSpace(reference.Path) != "" {
		return reference
	}
	workspace, ok := spec.Workspaces[strings.TrimSpace(reference.Name)]
	if !ok {
		return nil
	}
	return &workspace
}
