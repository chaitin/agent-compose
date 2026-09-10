package runs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/capabilities"
	"github.com/chaitin/agent-compose/pkg/compose"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"google.golang.org/protobuf/encoding/protojson"
)

type PreparationStore interface {
	GetProject(ctx context.Context, projectID string) (domain.ProjectRecord, error)
	GetProjectRevision(ctx context.Context, projectID string, revision int64) (domain.ProjectRevisionRecord, error)
	ListGlobalEnv(ctx context.Context) ([]domain.SandboxEnvVar, error)
	ListProjectVolumes(ctx context.Context, projectID string) (map[string]domain.VolumeRecord, error)
}

type WorkspaceResolver interface {
	ResolveProjectRunWorkspace(ctx context.Context, run domain.ProjectRunRecord, project domain.ProjectRecord, req WorkspaceRequest) (*domain.WorkspaceConfig, *domain.SandboxWorkspace, error)
}

type Preparation struct {
	EnvItems         []domain.SandboxEnvVar
	ProviderEnvItems []domain.SandboxEnvVar
	AgentDefinition  *domain.AgentDefinition
	CapsetIDs        []string
	WorkspaceConfig  *domain.WorkspaceConfig
	Workspace        *domain.SandboxWorkspace
	Volumes          []domain.VolumeMountSpec
	ProjectRoot      string
	ProjectVolumes   map[string]domain.VolumeRecord
	SandboxOptions   sandboxstore.CreateSandboxOptions
}

// PreparationDeps groups the store and workspace resolver PrepareProjectRun
// needs to prepare a run.
type PreparationDeps struct {
	Store    PreparationStore
	Resolver WorkspaceResolver
}

func PrepareProjectRun(ctx context.Context, deps PreparationDeps, run domain.ProjectRunRecord, requestEnv []*agentcomposev2.EnvVarSpec) (Preparation, error) {
	store := deps.Store
	resolver := deps.Resolver
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
	agent, err := projects.AgentDefinitionFromRevision(project, revision, run.AgentName)
	if err != nil {
		return Preparation{}, fmt.Errorf("resolve revision agent %s: %w", run.AgentID, err)
	}
	globalEnv, err := store.ListGlobalEnv(ctx)
	if err != nil {
		return Preparation{}, fmt.Errorf("list global env: %w", err)
	}
	providerEnvItems := MergeEnvItems(
		EnvItemsFromV2(spec.GetVariables()),
		agent.EnvItems,
		EnvItemsFromV2(requestEnv),
	)
	envItems := domain.MergeEnvItems(globalEnv, providerEnvItems)
	envItems = llms.FilterPersistedRuntimeEnv(envItems)
	prepared := Preparation{
		EnvItems:         envItems,
		ProviderEnvItems: providerEnvItems,
		AgentDefinition:  &agent,
		CapsetIDs:        capabilities.NormalizeCapsetIDs(agent.CapsetIDs),
		Volumes:          agent.Volumes,
		ProjectRoot:      ProjectRoot(project),
		SandboxOptions:   sandboxOptionsFromAgentSpec(agentSpec),
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
	workspaceConfig, workspaceSnapshot, err := resolver.ResolveProjectRunWorkspace(ctx, run, project, WorkspaceRequest{Project: projectWorkspace, Agent: agentWorkspace})
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

func sandboxOptionsFromAgentSpec(agent *agentcomposev2.AgentSpec) sandboxstore.CreateSandboxOptions {
	if agent == nil {
		return sandboxstore.CreateSandboxOptions{}
	}
	options := sandboxstore.CreateSandboxOptions{}
	if jupyter := agent.GetJupyter(); jupyter != nil {
		options.JupyterEnabled = jupyter.GetEnabled()
		options.JupyterGuestPort = int(jupyter.GetGuestPort())
	}
	if sandbox := agent.GetSandbox(); sandbox != nil {
		options.StoppedRuntimePolicy = sandbox.GetStoppedRuntimePolicy()
	}
	return options
}

func DecodeRevisionSpec(raw string) (*agentcomposev2.ProjectSpec, error) {
	var spec agentcomposev2.ProjectSpec
	data := []byte(strings.TrimSpace(raw))
	normalizedData, err := normalizeRevisionClosedSetsJSON(data)
	if err != nil {
		return nil, fmt.Errorf("decode project revision spec: %w", err)
	}
	opts := protojson.UnmarshalOptions{DiscardUnknown: true}
	if err := opts.Unmarshal(normalizedData, &spec); err != nil {
		return nil, fmt.Errorf("decode project revision spec: %w", err)
	}
	if err := restoreCanonicalRevisionWorkspaces(data, &spec); err != nil {
		return nil, err
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

func ComposeWorkspaceSpecFromV2(workspace *agentcomposev2.WorkspaceSpec) (*compose.WorkspaceSpec, error) {
	if workspace == nil {
		return nil, nil
	}
	mode, err := workspaceModeText(workspace.GetMode())
	if err != nil {
		return nil, err
	}
	return &compose.WorkspaceSpec{
		Mode:     mode,
		ReadOnly: workspace.GetReadOnly(),
		Name:     workspace.GetName(),
		Provider: workspace.GetProvider(),
		URL:      workspace.GetUrl(),
		Ref:      workspace.GetRef(),
		Path:     workspace.GetPath(),
		Format:   workspace.GetFormat(),
		Target:   workspace.GetTarget(),
		Username: workspace.GetUsername(),
		Password: workspace.GetPassword(),
		Token:    workspace.GetToken(),
	}, nil
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
		workspace, err := ComposeWorkspaceSpecFromV2(item.GetWorkspace())
		if err != nil {
			return nil, nil, fmt.Errorf("project workspace %q: %w", name, err)
		}
		if workspace == nil {
			return nil, nil, fmt.Errorf("project workspace %q spec is required", name)
		}
		workspace.Name = ""
		globals[name] = *workspace
	}

	agent, err := ComposeWorkspaceSpecFromV2(agentWorkspace)
	if err != nil {
		return nil, nil, fmt.Errorf("agent workspace: %w", err)
	}
	if agent != nil {
		hasName := strings.TrimSpace(agent.Name) != ""
		hasInline := agent.ContentSource().HasContent() || strings.TrimSpace(agent.Target) != ""
		if !hasInline && (agent.Mode != "" || agent.ReadOnly) {
			return nil, nil, fmt.Errorf("workspace mode and read_only require an inline source; set them on the named workspace definition instead")
		}
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
