package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	loaderdomain "agent-compose/internal/agentcompose/loader"
	projectdomain "agent-compose/internal/agentcompose/project"
	workspace "agent-compose/internal/agentcompose/workspace"
	"agent-compose/pkg/agentcompose/domain"
	"agent-compose/pkg/compose"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type EnvVar = loaderdomain.EnvVar

func DecodeProjectRevisionSpec(raw string) (*agentcomposev2.ProjectSpec, error) {
	var spec agentcomposev2.ProjectSpec
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &spec); err != nil {
		return nil, fmt.Errorf("decode project revision spec: %w", err)
	}
	return &spec, nil
}

func ProjectAgentByName(spec *agentcomposev2.ProjectSpec, name string) (*agentcomposev2.AgentSpec, bool) {
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

func EnvItemsFromV2(items []*agentcomposev2.EnvVarSpec) []EnvVar {
	env := make([]EnvVar, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		env = append(env, EnvVar{
			Name:   item.GetName(),
			Value:  item.GetValue(),
			Secret: item.GetSecret(),
		})
	}
	return NormalizeEnvItems(env)
}

func WorkspaceSpecFromV2(item *agentcomposev2.WorkspaceSpec) *compose.WorkspaceSpec {
	if item == nil {
		return nil
	}
	return &compose.WorkspaceSpec{
		Provider: item.GetProvider(),
		URL:      item.GetUrl(),
		Branch:   item.GetBranch(),
		Path:     item.GetPath(),
	}
}

func SelectWorkspace(projectWorkspace, agentWorkspace *compose.WorkspaceSpec) *compose.WorkspaceSpec {
	if agentWorkspace != nil {
		return agentWorkspace
	}
	return projectWorkspace
}

func MergeEnvItems(groups ...[]EnvVar) []EnvVar {
	var merged []EnvVar
	for _, group := range groups {
		merged = mergeEnvItems(merged, group)
	}
	return merged
}

func NormalizeEnvItems(items []EnvVar) []EnvVar {
	if len(items) == 0 {
		return nil
	}
	merged := make(map[string]EnvVar, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		item.Name = name
		merged[name] = item
	}
	if len(merged) == 0 {
		return nil
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]EnvVar, 0, len(keys))
	for _, key := range keys {
		result = append(result, merged[key])
	}
	return result
}

func ResolveLocalWorkspacePath(project projectdomain.ProjectRecord, rawPath string) (string, error) {
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

func GitWorkspaceConfig(run projectdomain.ProjectRunRecord, spec *compose.WorkspaceSpec) (workspace.Config, error) {
	workspaceID := WorkspaceID(run, "git")
	if strings.TrimSpace(spec.URL) == "" {
		return workspace.Config{}, fmt.Errorf("git workspace url is required")
	}
	if _, err := workspace.NormalizeGitCloneTarget(workspaceID, spec.Path); err != nil {
		return workspace.Config{}, err
	}
	payload, err := json.Marshal(workspace.GitConfig{
		URL:         strings.TrimSpace(spec.URL),
		Branch:      strings.TrimSpace(spec.Branch),
		CloneTarget: strings.TrimSpace(spec.Path),
	})
	if err != nil {
		return workspace.Config{}, fmt.Errorf("encode git workspace config: %w", err)
	}
	return workspace.Config{
		ID:         workspaceID,
		Name:       WorkspaceName(run, "git"),
		Type:       "git",
		ConfigJSON: string(payload),
		Comment:    fmt.Sprintf("project run %s git workspace snapshot", run.RunID),
	}, nil
}

func LocalWorkspaceConfig(run projectdomain.ProjectRunRecord, configJSON string) workspace.Config {
	return workspace.Config{
		ID:         WorkspaceID(run, "local"),
		Name:       WorkspaceName(run, "local"),
		Type:       "file",
		ConfigJSON: configJSON,
		Comment:    fmt.Sprintf("project run %s local workspace snapshot", run.RunID),
	}
}

func WorkspaceID(run projectdomain.ProjectRunRecord, provider string) string {
	return domain.StableReadableID("workspace", run.AgentName+"-"+provider, run.RunID+"|workspace|"+provider)
}

func WorkspaceName(run projectdomain.ProjectRunRecord, provider string) string {
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

func mergeEnvItems(globalItems, sessionItems []EnvVar) []EnvVar {
	merged := make(map[string]EnvVar, len(globalItems)+len(sessionItems))
	for _, item := range NormalizeEnvItems(globalItems) {
		merged[item.Name] = item
	}
	for _, item := range NormalizeEnvItems(sessionItems) {
		merged[item.Name] = item
	}
	if len(merged) == 0 {
		return nil
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]EnvVar, 0, len(keys))
	for _, key := range keys {
		result = append(result, merged[key])
	}
	return result
}
