package runs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type Preparation struct {
	EnvItems         []domain.SessionEnvVar
	ProviderEnvItems []domain.SessionEnvVar
	CapsetIDs        []string
	WorkspaceConfig  *domain.WorkspaceConfig
	Workspace        *domain.SessionWorkspace
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
