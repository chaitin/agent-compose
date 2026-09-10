package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sources"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

type projectRunWorkspaceResolver struct {
	controller *Controller
}

// WorkspaceRequest is the pair of candidate workspace specs a run can resolve
// from: the project-level default and an agent-level override.
type WorkspaceRequest struct {
	Project *compose.WorkspaceSpec
	Agent   *compose.WorkspaceSpec
}

func (r projectRunWorkspaceResolver) ResolveProjectRunWorkspace(ctx context.Context, run domain.ProjectRunRecord, project domain.ProjectRecord, req WorkspaceRequest) (*domain.WorkspaceConfig, *domain.SandboxWorkspace, error) {
	workspace, err := r.controller.prepareProjectRunWorkspace(ctx, run, project, req)
	if err != nil || workspace == nil {
		return workspace, nil, err
	}
	return workspace, toSandboxWorkspaceSnapshot(*workspace), nil
}

func (c *Controller) prepareProjectRunWorkspace(ctx context.Context, run domain.ProjectRunRecord, project domain.ProjectRecord, req WorkspaceRequest) (*domain.WorkspaceConfig, error) {
	_ = ctx
	workspace := req.Project
	if req.Agent != nil {
		workspace = req.Agent
	}
	if workspace == nil {
		return nil, nil
	}
	provider := strings.ToLower(strings.TrimSpace(workspace.Provider))
	mode, err := domain.NormalizeWorkspaceDelivery(provider, workspace.Mode, workspace.ReadOnly)
	if err != nil {
		return nil, err
	}
	if mode == domain.WorkspaceModeMount {
		if c == nil || c.config == nil {
			return nil, fmt.Errorf("config is required")
		}
		driver, err := driverpkg.ResolveSandboxRuntimeDriver(run.Driver, c.config.RuntimeDriver)
		if err != nil {
			return nil, err
		}
		if driver != driverpkg.RuntimeDriverDocker {
			return nil, fmt.Errorf("%w: workspace mount mode requires the docker runtime driver, got %q", domain.ErrInvalidArgument, driver)
		}
	}
	normalizedWorkspace := *workspace
	normalizedWorkspace.Mode = mode
	workspace = &normalizedWorkspace
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
	workspaceID := WorkspaceID(run, "local")
	if workspace.Mode == domain.WorkspaceModeMount {
		configJSON, err := workspaces.NewFileWorkspaceMountConfig(project, workspace.Path, workspace.Target, workspace.ReadOnly)
		if err != nil {
			return domain.WorkspaceConfig{}, err
		}
		return domain.WorkspaceConfig{
			ID: workspaceID, Name: WorkspaceName(run, "local"), Type: "file", ConfigJSON: configJSON,
			Comment: fmt.Sprintf("project run %s local workspace mount", run.RunID),
		}, nil
	}
	sourceDir, err := ResolveLocalProjectWorkspacePath(project, workspace.Path)
	if err != nil {
		return domain.WorkspaceConfig{}, err
	}
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
