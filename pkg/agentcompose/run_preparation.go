package agentcompose

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type ProjectRunPreparation = model.ProjectRunPreparation

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
	var spec agentcomposev2.ProjectSpec
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &spec); err != nil {
		return nil, fmt.Errorf("decode project revision spec: %w", err)
	}
	return &spec, nil
}

func normalizedProjectAgentByName(spec *agentcomposev2.ProjectSpec, name string) (*agentcomposev2.AgentSpec, bool) {
	if spec == nil {
		return nil, false
	}
	name = strings.TrimSpace(name)
	for _, agent := range spec.GetAgents() {
		if agent.GetName() == name {
			return agent, true
		}
	}
	return nil, false
}

func sessionEnvItemsFromV2(items []*agentcomposev2.EnvVarSpec) []SessionEnvVar {
	env := make([]SessionEnvVar, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		env = append(env, SessionEnvVar{
			Name:   item.GetName(),
			Value:  item.GetValue(),
			Secret: item.GetSecret(),
		})
	}
	return normalizeEnvItems(env)
}

func composeWorkspaceSpecFromV2(workspace *agentcomposev2.WorkspaceSpec) *compose.WorkspaceSpec {
	if workspace == nil {
		return nil
	}
	return &compose.WorkspaceSpec{
		Provider: workspace.GetProvider(),
		URL:      workspace.GetUrl(),
		Branch:   workspace.GetBranch(),
		Path:     workspace.GetPath(),
	}
}

func mergeRunEnvItems(groups ...[]SessionEnvVar) []SessionEnvVar {
	var merged []SessionEnvVar
	for _, group := range groups {
		merged = mergeEnvItems(merged, group)
	}
	return merged
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
	config := WorkspaceConfig{
		ID:         workspaceID,
		Name:       projectRunWorkspaceName(run, "local"),
		Type:       "file",
		ConfigJSON: configJSON,
		Comment:    fmt.Sprintf("project run %s local workspace snapshot", run.RunID),
	}
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
	cleanPath, err := cleanLocalWorkspacePath(rawPath)
	if err != nil {
		return "", err
	}
	sourcePath := strings.TrimSpace(project.SourcePath)
	if sourcePath == "" {
		return "", fmt.Errorf("local workspace requires project source path")
	}
	sourceAbs, err := filepath.Abs(sourcePath)
	if err != nil {
		return "", fmt.Errorf("resolve project source path %q: %w", sourcePath, err)
	}
	sourceDir := sourceAbs
	if info, err := os.Stat(sourceAbs); err == nil && !info.IsDir() {
		sourceDir = filepath.Dir(sourceAbs)
	} else if err != nil {
		sourceDir = filepath.Dir(sourceAbs)
	}
	target := sourceDir
	if cleanPath != "." {
		target = filepath.Join(sourceDir, cleanPath)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("resolve local workspace path %q: %w", rawPath, err)
	}
	info, err := os.Lstat(targetAbs)
	if err != nil {
		return "", fmt.Errorf("local workspace source %s: %w", targetAbs, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("local workspace source %s is a symlink", targetAbs)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("local workspace source %s is not a directory", targetAbs)
	}
	return targetAbs, nil
}

func cleanLocalWorkspacePath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("local workspace path is required")
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("local workspace path %q must be relative", trimmed)
	}
	clean := filepath.Clean(trimmed)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("local workspace path %q escapes project source root", trimmed)
	}
	return clean, nil
}

func projectRunGitWorkspaceConfig(run ProjectRunRecord, workspace *compose.WorkspaceSpec) (WorkspaceConfig, error) {
	workspaceID := projectRunWorkspaceID(run, "git")
	if strings.TrimSpace(workspace.URL) == "" {
		return WorkspaceConfig{}, fmt.Errorf("git workspace url is required")
	}
	if _, err := normalizeGitCloneTarget(workspaceID, workspace.Path); err != nil {
		return WorkspaceConfig{}, err
	}
	payload, err := json.Marshal(gitWorkspaceConfig{
		URL:         strings.TrimSpace(workspace.URL),
		Branch:      strings.TrimSpace(workspace.Branch),
		CloneTarget: strings.TrimSpace(workspace.Path),
	})
	if err != nil {
		return WorkspaceConfig{}, fmt.Errorf("encode git workspace config: %w", err)
	}
	return WorkspaceConfig{
		ID:         workspaceID,
		Name:       projectRunWorkspaceName(run, "git"),
		Type:       "git",
		ConfigJSON: string(payload),
		Comment:    fmt.Sprintf("project run %s git workspace snapshot", run.RunID),
	}, nil
}

func projectRunWorkspaceID(run ProjectRunRecord, provider string) string {
	return stableReadableID("workspace", run.AgentName+"-"+provider, run.RunID+"|workspace|"+provider)
}

func projectRunWorkspaceName(run ProjectRunRecord, provider string) string {
	name := strings.TrimSpace(run.ProjectName)
	if name == "" {
		name = strings.TrimSpace(run.ProjectID)
	}
	agent := strings.TrimSpace(run.AgentName)
	if agent == "" {
		agent = "agent"
	}
	return strings.TrimSpace(fmt.Sprintf("%s %s %s run workspace", name, agent, provider))
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
