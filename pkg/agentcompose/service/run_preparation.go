package agentcompose

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"agent-compose/pkg/agentcompose/workspaces"
	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/runs"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func (s *Service) prepareProjectRun(ctx context.Context, run ProjectRunRecord, requestEnv []*agentcomposev2.EnvVarSpec) (runs.Preparation, error) {
	if s == nil {
		return runs.Preparation{}, fmt.Errorf("service is required")
	}
	return runs.PrepareProjectRun(ctx, s.configDB, projectRunWorkspaceResolver{service: s}, run, requestEnv)
}

type projectRunWorkspaceResolver struct {
	service *Service
}

func (r projectRunWorkspaceResolver) ResolveProjectRunWorkspace(ctx context.Context, run ProjectRunRecord, project ProjectRecord, projectWorkspace, agentWorkspace *compose.WorkspaceSpec) (*WorkspaceConfig, *SessionWorkspace, error) {
	workspace, err := r.service.prepareProjectRunWorkspace(ctx, run, project, projectWorkspace, agentWorkspace)
	if err != nil || workspace == nil {
		return workspace, nil, err
	}
	return workspace, toSessionWorkspaceSnapshot(*workspace), nil
}

func (s *Service) prepareProjectRunWorkspace(ctx context.Context, run ProjectRunRecord, project ProjectRecord, projectWorkspace, agentWorkspace *compose.WorkspaceSpec) (*WorkspaceConfig, error) {
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
	case "local":
		config, err := s.materializeLocalProjectRunWorkspace(run, project, workspace)
		if err != nil {
			return nil, err
		}
		return &config, nil
	case "git":
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

func (s *Service) materializeLocalProjectRunWorkspace(run ProjectRunRecord, project ProjectRecord, workspace *compose.WorkspaceSpec) (WorkspaceConfig, error) {
	if s == nil || s.config == nil {
		return WorkspaceConfig{}, fmt.Errorf("config is required")
	}
	sourceDir, err := runs.ResolveLocalProjectWorkspacePath(project, workspace.Path)
	if err != nil {
		return WorkspaceConfig{}, err
	}
	workspaceID := runs.WorkspaceID(run, "local")
	configJSON := workspaces.DefaultFileConfigJSON(s.config, workspaceID)
	if _, err := workspaces.ValidateFileWorkspaceConfig(s.config, workspaceID, configJSON); err != nil {
		return WorkspaceConfig{}, err
	}
	if err := resetFileWorkspaceSnapshotContent(s.config, workspaceID); err != nil {
		return WorkspaceConfig{}, err
	}
	config := WorkspaceConfig{
		ID:         workspaceID,
		Name:       runs.WorkspaceName(run, "local"),
		Type:       "file",
		ConfigJSON: configJSON,
		Comment:    fmt.Sprintf("project run %s local workspace snapshot", run.RunID),
	}
	content, err := workspaces.OpenFileWorkspaceContent(s.config, config)
	if err != nil {
		return WorkspaceConfig{}, err
	}
	defer func() { _ = content.Root.Close() }()
	sourceRoot, err := os.OpenRoot(sourceDir)
	if err != nil {
		return WorkspaceConfig{}, fmt.Errorf("open local workspace source %s: %w", sourceDir, err)
	}
	defer func() { _ = sourceRoot.Close() }()
	if err := workspaces.CopyRootDirectoryContents(sourceRoot, content.AbsRoot); err != nil {
		return WorkspaceConfig{}, fmt.Errorf("materialize local workspace snapshot: %w", err)
	}
	return config, nil
}

func projectRunGitWorkspaceConfig(run ProjectRunRecord, workspace *compose.WorkspaceSpec) (WorkspaceConfig, error) {
	workspaceID := runs.WorkspaceID(run, "git")
	if strings.TrimSpace(workspace.URL) == "" {
		return WorkspaceConfig{}, fmt.Errorf("git workspace url is required")
	}
	if _, err := workspaces.NormalizeGitCloneTarget(workspaceID, workspace.Path); err != nil {
		return WorkspaceConfig{}, err
	}
	payload, err := json.Marshal(workspaces.GitWorkspaceConfig{
		URL:         strings.TrimSpace(workspace.URL),
		Branch:      strings.TrimSpace(workspace.Branch),
		CloneTarget: strings.TrimSpace(workspace.Path),
	})
	if err != nil {
		return WorkspaceConfig{}, fmt.Errorf("encode git workspace config: %w", err)
	}
	return WorkspaceConfig{
		ID:         workspaceID,
		Name:       runs.WorkspaceName(run, "git"),
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
