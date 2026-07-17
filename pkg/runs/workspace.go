package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sources"
	"agent-compose/pkg/workspaces"
)

type projectRunWorkspaceResolver struct {
	controller *Controller
}

func (r projectRunWorkspaceResolver) ResolveProjectRunWorkspace(ctx context.Context, run domain.ProjectRunRecord, project domain.ProjectRecord, projectWorkspace, agentWorkspace *compose.WorkspaceSpec) (*domain.WorkspaceConfig, *domain.SandboxWorkspace, error) {
	workspace, err := r.controller.prepareProjectRunWorkspace(ctx, run, project, projectWorkspace, agentWorkspace)
	if err != nil || workspace == nil {
		return workspace, nil, err
	}
	return workspace, toSandboxWorkspaceSnapshot(*workspace), nil
}

func (c *Controller) prepareProjectRunWorkspace(ctx context.Context, run domain.ProjectRunRecord, project domain.ProjectRecord, projectWorkspace, agentWorkspace *compose.WorkspaceSpec) (*domain.WorkspaceConfig, error) {
	_ = ctx
	workspace := projectWorkspace
	if agentWorkspace != nil {
		workspace = agentWorkspace
	}
	if workspace == nil {
		return nil, nil
	}
	provider := strings.ToLower(strings.TrimSpace(workspace.Provider))
	switch provider {
	case sources.ProviderFile:
		config, err := c.materializeLocalProjectRunWorkspace(run, project, workspace)
		if err != nil {
			return nil, err
		}
		return &config, nil
	case sources.ProviderGit:
		config, err := projectRunGitWorkspaceConfig(run, workspace)
		if err != nil {
			return nil, err
		}
		return &config, nil
	default:
		if provider == "" {
			return nil, fmt.Errorf("workspace provider is required")
		}
		return nil, fmt.Errorf("unsupported workspace provider %q", workspace.Provider)
	}
}

func (c *Controller) materializeLocalProjectRunWorkspace(run domain.ProjectRunRecord, project domain.ProjectRecord, workspace *compose.WorkspaceSpec) (domain.WorkspaceConfig, error) {
	if c == nil || c.config == nil {
		return domain.WorkspaceConfig{}, fmt.Errorf("config is required")
	}
	sourceDir, err := ResolveLocalProjectWorkspacePath(project, workspace.Path)
	if err != nil {
		return domain.WorkspaceConfig{}, err
	}
	workspaceID := WorkspaceID(run, "local")
	configJSON := workspaces.DefaultFileConfigJSON(c.config, workspaceID)
	if _, err := workspaces.ValidateFileWorkspaceConfig(c.config, workspaceID, configJSON); err != nil {
		return domain.WorkspaceConfig{}, err
	}
	if err := resetFileWorkspaceSnapshotContent(c.config, workspaceID); err != nil {
		return domain.WorkspaceConfig{}, err
	}
	config := domain.WorkspaceConfig{
		ID:         workspaceID,
		Name:       WorkspaceName(run, "local"),
		Type:       "file",
		ConfigJSON: configJSON,
		Comment:    fmt.Sprintf("project run %s local workspace snapshot", run.RunID),
	}
	content, err := workspaces.OpenFileWorkspaceContent(c.config, config)
	if err != nil {
		return domain.WorkspaceConfig{}, err
	}
	defer func() { _ = content.Root.Close() }()
	sourceRoot, err := os.OpenRoot(sourceDir)
	if err != nil {
		return domain.WorkspaceConfig{}, fmt.Errorf("open local workspace source %s: %w", sourceDir, err)
	}
	defer func() { _ = sourceRoot.Close() }()
	target, err := workspaces.NormalizeWorkspaceTarget(workspaceID, workspace.Target)
	if err != nil {
		return domain.WorkspaceConfig{}, err
	}
	destination := content.AbsRoot
	if target != "." {
		destination = filepath.Join(content.AbsRoot, target)
		if err := os.MkdirAll(destination, 0o755); err != nil {
			return domain.WorkspaceConfig{}, fmt.Errorf("create local workspace target %s: %w", target, err)
		}
	}
	if err := workspaces.CopyRootDirectoryContents(sourceRoot, destination); err != nil {
		return domain.WorkspaceConfig{}, fmt.Errorf("materialize local workspace snapshot: %w", err)
	}
	return config, nil
}

func projectRunGitWorkspaceConfig(run domain.ProjectRunRecord, workspace *compose.WorkspaceSpec) (domain.WorkspaceConfig, error) {
	workspaceID := WorkspaceID(run, "git")
	if strings.TrimSpace(workspace.URL) == "" {
		return domain.WorkspaceConfig{}, fmt.Errorf("git workspace url is required")
	}
	if _, err := workspaces.NormalizeWorkspaceTarget(workspaceID, workspace.Target); err != nil {
		return domain.WorkspaceConfig{}, err
	}
	payload, err := json.Marshal(workspaces.GitWorkspaceConfig{
		Source: workspace.ContentSource(),
		Target: strings.TrimSpace(workspace.Target),
	})
	if err != nil {
		return domain.WorkspaceConfig{}, fmt.Errorf("encode git workspace config: %w", err)
	}
	return domain.WorkspaceConfig{
		ID:         workspaceID,
		Name:       WorkspaceName(run, "git"),
		Type:       "git",
		ConfigJSON: string(payload),
		Comment:    fmt.Sprintf("project run %s git workspace snapshot", run.RunID),
	}, nil
}

func resetFileWorkspaceSnapshotContent(config *appconfig.Config, workspaceID string) error {
	dataRoot, err := workspaces.OpenFileWorkspaceDataRoot(config)
	if err != nil {
		return err
	}
	defer func() { _ = dataRoot.Close() }()
	relRoot, err := workspaces.FileWorkspaceContentRelRoot(workspaceID)
	if err != nil {
		return err
	}
	if err := dataRoot.RemoveAll(relRoot); err != nil {
		return fmt.Errorf("reset local workspace snapshot %s: %w", workspaceID, err)
	}
	return nil
}

func toSandboxWorkspaceSnapshot(item domain.WorkspaceConfig) *domain.SandboxWorkspace {
	return &domain.SandboxWorkspace{
		ID:         item.ID,
		Name:       item.Name,
		Type:       item.Type,
		ConfigJSON: item.ConfigJSON,
	}
}
