package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/compose"
	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type PreparationStore interface {
	GetProject(ctx context.Context, projectID string) (domain.ProjectRecord, error)
	GetProjectRevision(ctx context.Context, projectID string, revision int64) (domain.ProjectRevisionRecord, error)
	GetAgentDefinition(ctx context.Context, id string) (domain.AgentDefinition, error)
	ListGlobalEnv(ctx context.Context) ([]domain.SessionEnvVar, error)
}

type WorkspaceResolver interface {
	ResolveProjectRunWorkspace(ctx context.Context, run domain.ProjectRunRecord, project domain.ProjectRecord, projectWorkspace, agentWorkspace *compose.WorkspaceSpec) (*domain.WorkspaceConfig, *domain.SessionWorkspace, error)
}

type Preparation struct {
	EnvItems         []domain.SessionEnvVar
	ProviderEnvItems []domain.SessionEnvVar
	CapsetIDs        []string
	WorkspaceConfig  *domain.WorkspaceConfig
	Workspace        *domain.SessionWorkspace
}

func PrepareProjectRun(ctx context.Context, store PreparationStore, resolver WorkspaceResolver, run domain.ProjectRunRecord, requestEnv []*agentcomposev2.EnvVarSpec) (Preparation, error) {
	if store == nil {
		return Preparation{}, fmt.Errorf("config store is required")
	}
	project, err := store.GetProject(ctx, run.ProjectID)
	if err != nil {
		return Preparation{}, fmt.Errorf("resolve project %s: %w", run.ProjectID, err)
	}
	revision, err := store.GetProjectRevision(ctx, run.ProjectID, run.ProjectRevision)
	if err != nil {
		return Preparation{}, fmt.Errorf("resolve project revision %s/%d: %w", run.ProjectID, run.ProjectRevision, err)
	}
	spec, err := DecodeRevisionSpec(revision.SpecJSON)
	if err != nil {
		return Preparation{}, err
	}
	agentSpec, ok := AgentSpecByName(spec, run.AgentName)
	if !ok {
		return Preparation{}, fmt.Errorf("project revision %s/%d missing agent %s", run.ProjectID, run.ProjectRevision, run.AgentName)
	}
	agent, err := store.GetAgentDefinition(ctx, run.ManagedAgentID)
	if err != nil {
		return Preparation{}, fmt.Errorf("resolve managed agent definition %s: %w", run.ManagedAgentID, err)
	}
	globalEnv, err := store.ListGlobalEnv(ctx)
	if err != nil {
		return Preparation{}, fmt.Errorf("list global env: %w", err)
	}
	envItems := MergeEnvItems(
		globalEnv,
		EnvItemsFromV2(spec.GetVariables()),
		agent.EnvItems,
		EnvItemsFromV2(requestEnv),
	)
	providerEnvItems := envItems
	envItems = llms.FilterPersistedRuntimeEnv(envItems)
	prepared := Preparation{
		EnvItems:         envItems,
		ProviderEnvItems: providerEnvItems,
		CapsetIDs:        capabilities.NormalizeCapsetIDs(agent.CapsetIDs),
	}
	if resolver == nil {
		return prepared, nil
	}
	workspaceConfig, workspaceSnapshot, err := resolver.ResolveProjectRunWorkspace(ctx, run, project, ComposeWorkspaceSpecFromV2(spec.GetWorkspace()), ComposeWorkspaceSpecFromV2(agentSpec.GetWorkspace()))
	if err != nil {
		return Preparation{}, err
	}
	if workspaceConfig != nil {
		prepared.WorkspaceConfig = workspaceConfig
		prepared.Workspace = workspaceSnapshot
	}
	return prepared, nil
}

func DecodeRevisionSpec(raw string) (*agentcomposev2.ProjectSpec, error) {
	var spec agentcomposev2.ProjectSpec
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &spec); err != nil {
		return nil, fmt.Errorf("decode project revision spec: %w", err)
	}
	return &spec, nil
}

func AgentSpecByName(spec *agentcomposev2.ProjectSpec, name string) (*agentcomposev2.AgentSpec, bool) {
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

func EnvItemsFromV2(items []*agentcomposev2.EnvVarSpec) []domain.SessionEnvVar {
	env := make([]domain.SessionEnvVar, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		env = append(env, domain.SessionEnvVar{
			Name:   item.GetName(),
			Value:  item.GetValue(),
			Secret: item.GetSecret(),
		})
	}
	return domain.NormalizeEnvItems(env)
}

func ComposeWorkspaceSpecFromV2(workspace *agentcomposev2.WorkspaceSpec) *compose.WorkspaceSpec {
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

func MergeEnvItems(groups ...[]domain.SessionEnvVar) []domain.SessionEnvVar {
	var merged []domain.SessionEnvVar
	for _, group := range groups {
		merged = domain.MergeEnvItems(merged, group)
	}
	return merged
}

func ResolveLocalProjectWorkspacePath(project domain.ProjectRecord, rawPath string) (string, error) {
	cleanPath, err := CleanLocalWorkspacePath(rawPath)
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

func CleanLocalWorkspacePath(raw string) (string, error) {
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
