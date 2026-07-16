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
	"agent-compose/pkg/storage/sessionstore"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type PreparationStore interface {
	GetProject(ctx context.Context, projectID string) (domain.ProjectRecord, error)
	GetProjectRevision(ctx context.Context, projectID string, revision int64) (domain.ProjectRevisionRecord, error)
	GetAgentDefinition(ctx context.Context, id string) (domain.AgentDefinition, error)
	ListGlobalEnv(ctx context.Context) ([]domain.SandboxEnvVar, error)
	ListProjectVolumes(ctx context.Context, projectID string) (map[string]domain.VolumeRecord, error)
}

type WorkspaceResolver interface {
	ResolveProjectRunWorkspace(ctx context.Context, run domain.ProjectRunRecord, project domain.ProjectRecord, projectWorkspace, agentWorkspace *compose.WorkspaceSpec) (*domain.WorkspaceConfig, *domain.SandboxWorkspace, error)
}

type Preparation struct {
	EnvItems         []domain.SandboxEnvVar
	ProviderEnvItems []domain.SandboxEnvVar
	CapsetIDs        []string
	WorkspaceConfig  *domain.WorkspaceConfig
	Workspace        *domain.SandboxWorkspace
	Volumes          []domain.VolumeMountSpec
	ProjectRoot      string
	ProjectVolumes   map[string]domain.VolumeRecord
	Jupyter          sessionstore.CreateSandboxOptions
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
		Volumes:          agent.Volumes,
		ProjectRoot:      ProjectRoot(project),
		Jupyter:          jupyterOptionsFromAgentSpec(agentSpec),
	}
	projectVolumes, err := store.ListProjectVolumes(ctx, project.ID)
	if err != nil {
		return Preparation{}, fmt.Errorf("list project volumes %s: %w", project.ID, err)
	}
	prepared.ProjectVolumes = projectVolumes
	if resolver == nil {
		return prepared, nil
	}
	projectWorkspace, agentWorkspace, err := ProjectRunWorkspaceSpecsFromV2(spec.GetWorkspaces(), agentSpec.GetWorkspace())
	if err != nil {
		return Preparation{}, err
	}
	workspaceConfig, workspaceSnapshot, err := resolver.ResolveProjectRunWorkspace(ctx, run, project, projectWorkspace, agentWorkspace)
	if err != nil {
		return Preparation{}, err
	}
	if workspaceConfig != nil {
		prepared.WorkspaceConfig = workspaceConfig
		prepared.Workspace = workspaceSnapshot
	}
	return prepared, nil
}

func ProjectRoot(project domain.ProjectRecord) string {
	sourcePath := strings.TrimSpace(project.SourcePath)
	if sourcePath == "" {
		return ""
	}
	info, err := os.Stat(sourcePath)
	if err == nil && info.IsDir() {
		return sourcePath
	}
	return filepath.Dir(sourcePath)
}

func jupyterOptionsFromAgentSpec(agent *agentcomposev2.AgentSpec) sessionstore.CreateSandboxOptions {
	if agent == nil || agent.GetJupyter() == nil {
		return sessionstore.CreateSandboxOptions{}
	}
	jupyter := agent.GetJupyter()
	return sessionstore.CreateSandboxOptions{
		JupyterEnabled:   jupyter.GetEnabled(),
		JupyterGuestPort: int(jupyter.GetGuestPort()),
	}
}

func DecodeRevisionSpec(raw string) (*agentcomposev2.ProjectSpec, error) {
	var spec agentcomposev2.ProjectSpec
	data := []byte(strings.TrimSpace(raw))
	protoData, err := normalizeCanonicalAgentStatuses(data)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(protoData, &spec); err != nil {
		return nil, fmt.Errorf("decode project revision spec: %w", err)
	}
	if err := restoreCanonicalProjectWorkspaces(data, &spec); err != nil {
		return nil, err
	}
	return &spec, nil
}

func normalizeCanonicalAgentStatuses(data []byte) ([]byte, error) {
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode project revision spec: %w", err)
	}
	agents, _ := document["agents"].([]any)
	for _, value := range agents {
		agent, _ := value.(map[string]any)
		status, ok := agent["status"].(string)
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(status)) {
		case "", "enabled":
			agent["status"] = int(agentcomposev2.AgentStatus_AGENT_STATUS_ENABLED)
		case "disabled":
			agent["status"] = int(agentcomposev2.AgentStatus_AGENT_STATUS_DISABLED)
		default:
			return nil, fmt.Errorf("decode project revision spec: unknown agent status %q", status)
		}
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode project revision compatibility shape: %w", err)
	}
	return encoded, nil
}

type canonicalRevisionSpec struct {
	Workspaces []json.RawMessage `json:"workspaces"`
}

type canonicalRevisionWorkspace struct {
	Key       string                        `json:"key"`
	Name      string                        `json:"name"`
	Provider  string                        `json:"provider"`
	URL       string                        `json:"url"`
	Branch    string                        `json:"branch"`
	Path      string                        `json:"path"`
	Workspace *agentcomposev2.WorkspaceSpec `json:"workspace"`
}

func restoreCanonicalProjectWorkspaces(data []byte, spec *agentcomposev2.ProjectSpec) error {
	if spec == nil || len(spec.GetWorkspaces()) == 0 {
		return nil
	}
	var stored canonicalRevisionSpec
	if err := json.Unmarshal(data, &stored); err != nil {
		return fmt.Errorf("decode project revision workspace compatibility shape: %w", err)
	}
	for i, raw := range stored.Workspaces {
		if i >= len(spec.Workspaces) || spec.Workspaces[i].GetWorkspace() != nil {
			continue
		}
		var workspace canonicalRevisionWorkspace
		if err := json.Unmarshal(raw, &workspace); err != nil {
			return fmt.Errorf("decode project revision workspace %d: %w", i, err)
		}
		if workspace.Workspace != nil {
			spec.Workspaces[i].Workspace = workspace.Workspace
			continue
		}
		if strings.TrimSpace(workspace.Provider) == "" &&
			strings.TrimSpace(workspace.URL) == "" &&
			strings.TrimSpace(workspace.Branch) == "" &&
			strings.TrimSpace(workspace.Path) == "" {
			continue
		}
		if key := strings.TrimSpace(workspace.Key); key != "" {
			spec.Workspaces[i].Name = key
		} else if strings.TrimSpace(spec.Workspaces[i].GetName()) == "" {
			spec.Workspaces[i].Name = strings.TrimSpace(workspace.Name)
		}
		spec.Workspaces[i].Workspace = &agentcomposev2.WorkspaceSpec{
			Provider: workspace.Provider,
			Url:      workspace.URL,
			Branch:   workspace.Branch,
			Path:     workspace.Path,
		}
	}
	return nil
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

func EnvItemsFromV2(items []*agentcomposev2.EnvVarSpec) []domain.SandboxEnvVar {
	env := make([]domain.SandboxEnvVar, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		env = append(env, domain.SandboxEnvVar{
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
		Name:     workspace.GetName(),
		Provider: workspace.GetProvider(),
		URL:      workspace.GetUrl(),
		Branch:   workspace.GetBranch(),
		Path:     workspace.GetPath(),
	}
}

func ProjectRunWorkspaceSpecsFromV2(projectWorkspaces []*agentcomposev2.NamedWorkspaceSpec, agentWorkspace *agentcomposev2.WorkspaceSpec) (*compose.WorkspaceSpec, *compose.WorkspaceSpec, error) {
	globals := make(map[string]compose.WorkspaceSpec, len(projectWorkspaces))
	for i, item := range projectWorkspaces {
		name := strings.TrimSpace(item.GetName())
		if name == "" {
			return nil, nil, fmt.Errorf("project workspace %d name is required", i)
		}
		if _, exists := globals[name]; exists {
			return nil, nil, fmt.Errorf("duplicate project workspace %q", name)
		}
		workspace := ComposeWorkspaceSpecFromV2(item.GetWorkspace())
		if workspace == nil {
			return nil, nil, fmt.Errorf("project workspace %q spec is required", name)
		}
		workspace.Name = ""
		globals[name] = *workspace
	}

	agent := ComposeWorkspaceSpecFromV2(agentWorkspace)
	if agent != nil {
		hasName := strings.TrimSpace(agent.Name) != ""
		hasInline := strings.TrimSpace(agent.Provider) != "" || strings.TrimSpace(agent.URL) != "" || strings.TrimSpace(agent.Branch) != "" || strings.TrimSpace(agent.Path) != ""
		switch {
		case hasInline:
			return nil, agent, nil
		case hasName:
			workspace, ok := globals[strings.TrimSpace(agent.Name)]
			if !ok {
				return nil, nil, fmt.Errorf("agent workspace %q is not defined", agent.Name)
			}
			return nil, &workspace, nil
		}
	}

	return nil, nil, nil
}

func MergeEnvItems(groups ...[]domain.SandboxEnvVar) []domain.SandboxEnvVar {
	var merged []domain.SandboxEnvVar
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
