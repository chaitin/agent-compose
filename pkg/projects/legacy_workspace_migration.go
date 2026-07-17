package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/identity"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/workspaces"
)

type legacyWorkspaceConfigStore interface {
	GetWorkspaceConfig(context.Context, string) (domain.WorkspaceConfig, error)
}

type legacyWorkspaceProjection struct {
	workspaces map[string]compose.WorkspaceSpec
	nameByID   map[string]string
}

func (c *Controller) loadLegacyWorkspaceProjection(ctx context.Context, agents []domain.AgentDefinition, loaders []domain.Loader) (legacyWorkspaceProjection, error) {
	workspaceIDs := legacyReferencedWorkspaceIDs(agents, loaders)
	if len(workspaceIDs) == 0 {
		return legacyWorkspaceProjection{}, nil
	}
	if c == nil || c.store == nil {
		return legacyWorkspaceProjection{}, fmt.Errorf("sync legacy default project: config store is required")
	}
	store, ok := c.store.(legacyWorkspaceConfigStore)
	if !ok {
		return legacyWorkspaceProjection{}, fmt.Errorf("sync legacy default project: workspace config store is required")
	}

	configs := make([]domain.WorkspaceConfig, 0, len(workspaceIDs))
	for _, workspaceID := range workspaceIDs {
		config, err := store.GetWorkspaceConfig(ctx, workspaceID)
		if err != nil {
			return legacyWorkspaceProjection{}, fmt.Errorf("load legacy workspace preset %s: %w", workspaceID, err)
		}
		configs = append(configs, config)
	}

	nameByID := legacyProjectWorkspaceNames(configs)
	projectWorkspaces := make(map[string]compose.WorkspaceSpec, len(configs))
	for _, config := range configs {
		workspace, err := mapLegacyWorkspaceConfig(c.config, config)
		if err != nil {
			return legacyWorkspaceProjection{}, err
		}
		projectWorkspaces[nameByID[config.ID]] = workspace
	}
	return legacyWorkspaceProjection{workspaces: projectWorkspaces, nameByID: nameByID}, nil
}

func legacyReferencedWorkspaceIDs(agents []domain.AgentDefinition, loaders []domain.Loader) []string {
	seen := make(map[string]struct{}, len(agents)+len(loaders))
	for _, agent := range agents {
		if workspaceID := strings.TrimSpace(agent.WorkspaceID); workspaceID != "" {
			seen[workspaceID] = struct{}{}
		}
	}
	for _, loader := range loaders {
		if workspaceID := strings.TrimSpace(loader.Summary.WorkspaceID); workspaceID != "" {
			seen[workspaceID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(seen))
	for workspaceID := range seen {
		ids = append(ids, workspaceID)
	}
	sort.Strings(ids)
	return ids
}

func legacyProjectWorkspaceNames(configs []domain.WorkspaceConfig) map[string]string {
	configs = append([]domain.WorkspaceConfig(nil), configs...)
	sort.Slice(configs, func(i, j int) bool {
		if configs[i].Name == configs[j].Name {
			return configs[i].ID < configs[j].ID
		}
		return configs[i].Name < configs[j].Name
	})

	bases := make(map[string]string, len(configs))
	counts := make(map[string]int, len(configs))
	for _, config := range configs {
		base := legacyCanonicalAgentName(config.Name)
		bases[config.ID] = base
		if base != "" {
			counts[base]++
		}
	}

	names := make(map[string]string, len(configs))
	used := make(map[string]struct{}, len(configs))
	for _, config := range configs {
		base := bases[config.ID]
		if base != "" && counts[base] == 1 {
			names[config.ID] = base
			used[base] = struct{}{}
		}
	}
	for _, config := range configs {
		if names[config.ID] != "" {
			continue
		}
		prefix := bases[config.ID]
		if prefix == "" {
			prefix = "legacy-workspace"
		}
		names[config.ID] = reserveLegacyStableName(prefix, identity.ResourceWorkspace, config.ID, config.Name, used)
	}
	return names
}

func mapLegacyWorkspaceConfig(config *appconfig.Config, workspace domain.WorkspaceConfig) (compose.WorkspaceSpec, error) {
	switch strings.ToLower(strings.TrimSpace(workspace.Type)) {
	case "git":
		var legacy workspaces.GitWorkspaceConfig
		if err := json.Unmarshal([]byte(workspace.ConfigJSON), &legacy); err != nil {
			return compose.WorkspaceSpec{}, fmt.Errorf("decode legacy git workspace preset %s: %w", workspace.ID, err)
		}
		target, err := workspaces.NormalizeWorkspaceTarget(workspace.ID, legacy.Target)
		if err != nil {
			return compose.WorkspaceSpec{}, err
		}
		if strings.TrimSpace(legacy.URL) == "" {
			return compose.WorkspaceSpec{}, fmt.Errorf("legacy git workspace preset %s has no url", workspace.ID)
		}
		return compose.WorkspaceSpec{
			Provider: "git",
			URL:      legacy.URL,
			Ref:      legacy.Ref,
			Username: legacy.Username,
			Password: legacy.Password,
			Token:    legacy.Token,
			Target:   target,
		}, nil
	case "file":
		// Keep a valid v2 shape in the returned spec. The synthetic-project
		// artifact boundary recognizes this canonical storage path and restores
		// the original preset ID instead of treating DATA_ROOT as project source.
		if config == nil || strings.TrimSpace(config.DataRoot) == "" {
			return compose.WorkspaceSpec{}, fmt.Errorf("map legacy file workspace preset %s: data root is required", workspace.ID)
		}
		root, err := workspaces.FileWorkspaceContentRoot(config, workspace)
		if err != nil {
			return compose.WorkspaceSpec{}, fmt.Errorf("map legacy file workspace preset %s: %w", workspace.ID, err)
		}
		dataRoot, err := filepath.Abs(config.DataRoot)
		if err != nil {
			return compose.WorkspaceSpec{}, fmt.Errorf("resolve data root for legacy file workspace preset %s: %w", workspace.ID, err)
		}
		relative, err := filepath.Rel(filepath.Clean(dataRoot), filepath.Clean(root))
		if err != nil {
			return compose.WorkspaceSpec{}, fmt.Errorf("map legacy file workspace preset %s path: %w", workspace.ID, err)
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return compose.WorkspaceSpec{}, fmt.Errorf("legacy file workspace preset %s content is outside the data root", workspace.ID)
		}
		return compose.WorkspaceSpec{Provider: "file", Path: filepath.ToSlash(relative), Target: "."}, nil
	default:
		return compose.WorkspaceSpec{}, fmt.Errorf("legacy workspace preset %s has unsupported type %q", workspace.ID, workspace.Type)
	}
}

func (p legacyWorkspaceProjection) reference(workspaceID string) (*compose.WorkspaceSpec, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, nil
	}
	name := strings.TrimSpace(p.nameByID[workspaceID])
	if name == "" {
		return nil, fmt.Errorf("legacy workspace preset %s was not loaded for project migration", workspaceID)
	}
	return &compose.WorkspaceSpec{Name: name}, nil
}

func removeImplicitLegacyWorkspaceDefault(spec *compose.NormalizedProjectSpec) {
	if spec == nil || len(spec.Workspaces) == 0 {
		return
	}
	hasWorkspaceLessAgent := false
	for _, agent := range spec.Agents {
		if agent.Workspace == nil {
			hasWorkspaceLessAgent = true
			break
		}
	}
	if !hasWorkspaceLessAgent {
		return
	}
	for index := range spec.Agents {
		workspace := spec.Agents[index].Workspace
		if workspace == nil {
			continue
		}
		mapped, ok := spec.Workspaces[strings.TrimSpace(workspace.Name)]
		if !ok {
			continue
		}
		mapped.Name = ""
		spec.Agents[index].Workspace = &mapped
	}
	spec.Workspaces = nil
}
