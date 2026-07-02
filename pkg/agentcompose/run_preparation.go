package agentcompose

import (
	"context"
	"fmt"
	"os"
	"strings"

	rundomain "agent-compose/internal/agentcompose/run"
	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type ProjectRunPreparation struct {
	EnvItems         []SessionEnvVar
	ProviderEnvItems []SessionEnvVar
	CapsetIDs        []string
	WorkspaceConfig  *WorkspaceConfig
	Workspace        *SessionWorkspace
}

func (s *Service) prepareProjectRun(ctx context.Context, run ProjectRunRecord, requestEnv []*agentcomposev2.EnvVarSpec) (ProjectRunPreparation, error) {
	if s == nil || s.configDB == nil {
		return ProjectRunPreparation{}, fmt.Errorf("config store is required")
	}
	project, err := s.configDB.GetProject(ctx, run.ProjectID)
	if err != nil {
		return ProjectRunPreparation{}, fmt.Errorf("resolve project %s: %w", run.ProjectID, err)
	}
	revision, err := s.configDB.GetProjectRevision(ctx, run.ProjectID, run.ProjectRevision)
	if err != nil {
		return ProjectRunPreparation{}, fmt.Errorf("resolve project revision %s/%d: %w", run.ProjectID, run.ProjectRevision, err)
	}
	spec, err := decodeProjectRevisionSpec(revision.SpecJSON)
	if err != nil {
		return ProjectRunPreparation{}, err
	}
	agentSpec, ok := normalizedProjectAgentByName(spec, run.AgentName)
	if !ok {
		return ProjectRunPreparation{}, fmt.Errorf("project revision %s/%d missing agent %s", run.ProjectID, run.ProjectRevision, run.AgentName)
	}
	agent, err := s.configDB.GetAgentDefinition(ctx, run.ManagedAgentID)
	if err != nil {
		return ProjectRunPreparation{}, fmt.Errorf("resolve managed agent definition %s: %w", run.ManagedAgentID, err)
	}
	globalEnv, err := s.configDB.ListGlobalEnv(ctx)
	if err != nil {
		return ProjectRunPreparation{}, fmt.Errorf("list global env: %w", err)
	}
	envItems := mergeRunEnvItems(
		globalEnv,
		sessionEnvItemsFromV2(spec.GetVariables()),
		agent.EnvItems,
		sessionEnvItemsFromV2(requestEnv),
	)
	providerEnvItems := envItems
	envItems = filterPersistedRuntimeEnv(envItems)
	workspace, err := s.prepareProjectRunWorkspace(ctx, run, project, composeWorkspaceSpecFromV2(spec.GetWorkspace()), composeWorkspaceSpecFromV2(agentSpec.GetWorkspace()))
	if err != nil {
		return ProjectRunPreparation{}, err
	}
	prepared := ProjectRunPreparation{EnvItems: envItems, ProviderEnvItems: providerEnvItems, CapsetIDs: normalizeCapsetIDs(agent.CapsetIDs)}
	if workspace != nil {
		prepared.WorkspaceConfig = workspace
		prepared.Workspace = toSessionWorkspaceSnapshot(*workspace)
	}
	return prepared, nil
}

func decodeProjectRevisionSpec(raw string) (*agentcomposev2.ProjectSpec, error) {
	return rundomain.DecodeProjectRevisionSpec(raw)
}

func normalizedProjectAgentByName(spec *agentcomposev2.ProjectSpec, name string) (*agentcomposev2.AgentSpec, bool) {
	return rundomain.ProjectAgentByName(spec, name)
}

func sessionEnvItemsFromV2(items []*agentcomposev2.EnvVarSpec) []SessionEnvVar {
	return rundomain.EnvItemsFromV2(items)
}

func composeWorkspaceSpecFromV2(workspace *agentcomposev2.WorkspaceSpec) *compose.WorkspaceSpec {
	return rundomain.WorkspaceSpecFromV2(workspace)
}

func mergeRunEnvItems(groups ...[]SessionEnvVar) []SessionEnvVar {
	return rundomain.MergeEnvItems(groups...)
}

func (s *Service) prepareProjectRunWorkspace(ctx context.Context, run ProjectRunRecord, project ProjectRecord, projectWorkspace, agentWorkspace *compose.WorkspaceSpec) (*WorkspaceConfig, error) {
	_ = ctx
	workspace := rundomain.SelectWorkspace(projectWorkspace, agentWorkspace)
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
	sourceDir, err := resolveLocalProjectWorkspacePath(project, workspace.Path)
	if err != nil {
		return WorkspaceConfig{}, err
	}
	workspaceID := projectRunWorkspaceID(run, "local")
	configJSON := defaultFileWorkspaceConfigJSON(s.config, workspaceID)
	if _, err := validateFileWorkspaceConfig(s.config, workspaceID, configJSON); err != nil {
		return WorkspaceConfig{}, err
	}
	if err := resetFileWorkspaceSnapshotContent(s.config, workspaceID); err != nil {
		return WorkspaceConfig{}, err
	}
	config := rundomain.LocalWorkspaceConfig(run, configJSON)
	content, err := openFileWorkspaceContent(s.config, config)
	if err != nil {
		return WorkspaceConfig{}, err
	}
	defer func() { _ = content.Root.Close() }()
	sourceRoot, err := os.OpenRoot(sourceDir)
	if err != nil {
		return WorkspaceConfig{}, fmt.Errorf("open local workspace source %s: %w", sourceDir, err)
	}
	defer func() { _ = sourceRoot.Close() }()
	if err := copyRootDirectoryContents(sourceRoot, content.AbsRoot); err != nil {
		return WorkspaceConfig{}, fmt.Errorf("materialize local workspace snapshot: %w", err)
	}
	return config, nil
}

func resolveLocalProjectWorkspacePath(project ProjectRecord, rawPath string) (string, error) {
	return rundomain.ResolveLocalWorkspacePath(project, rawPath)
}

func cleanLocalWorkspacePath(raw string) (string, error) {
	return rundomain.CleanLocalWorkspacePath(raw)
}

func projectRunGitWorkspaceConfig(run ProjectRunRecord, workspace *compose.WorkspaceSpec) (WorkspaceConfig, error) {
	return rundomain.GitWorkspaceConfig(run, workspace)
}

func projectRunWorkspaceID(run ProjectRunRecord, provider string) string {
	return rundomain.WorkspaceID(run, provider)
}

func projectRunWorkspaceName(run ProjectRunRecord, provider string) string {
	return rundomain.WorkspaceName(run, provider)
}

func resetFileWorkspaceSnapshotContent(config *appconfig.Config, workspaceID string) error {
	dataRoot, err := openFileWorkspaceDataRoot(config)
	if err != nil {
		return err
	}
	defer func() { _ = dataRoot.Close() }()
	relRoot, err := fileWorkspaceContentRelRoot(workspaceID)
	if err != nil {
		return err
	}
	if err := dataRoot.RemoveAll(relRoot); err != nil {
		return fmt.Errorf("reset local workspace snapshot %s: %w", workspaceID, err)
	}
	return nil
}
