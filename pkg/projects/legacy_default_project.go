package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"agent-compose/pkg/compose"
	"agent-compose/pkg/identity"
	domain "agent-compose/pkg/model"
)

const LegacyDefaultProjectName = "legacy-v1-default"

type legacyAgentDefinitionStore interface {
	ListAgentDefinitions(context.Context, domain.AgentDefinitionListOptions) (domain.AgentDefinitionListResult, error)
}

type legacyLoaderStore interface {
	ListLoaders(context.Context) ([]domain.Loader, error)
}

// SyncLegacyDefaultProject projects active standalone v1 agents and loaders
// into one deterministic v2 project. Source AgentDefinitions remain unchanged;
// legacy loaders are adopted in place so their history and trigger state stay
// attached to the task shown by the project APIs.
func (c *Controller) SyncLegacyDefaultProject(ctx context.Context) (ApplyResult, error) {
	agents, err := c.listStandaloneAgents(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	legacyLoaders, err := c.listLegacyDefaultLoaders(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	if len(agents) == 0 && len(legacyLoaders) == 0 {
		return ApplyResult{}, nil
	}
	workspaceProjection, err := c.loadLegacyWorkspaceProjection(ctx, agents, legacyLoaders)
	if err != nil {
		return ApplyResult{}, err
	}
	normalized, err := legacyDefaultNormalizedProjectWithWorkspaces(agents, legacyLoaders, workspaceProjection)
	if err != nil {
		return ApplyResult{}, err
	}
	// Uploaded v1 file presets are stored snapshots, not project source trees.
	// Keep the synthetic source empty so project identity and relative bind
	// resolution retain their v1 behavior; project artifacts carry preset IDs.
	return c.ApplyProject(ctx, ApplyRequest{Normalized: normalized})
}

func (c *Controller) listStandaloneAgents(ctx context.Context) ([]domain.AgentDefinition, error) {
	if c == nil || c.store == nil {
		return nil, fmt.Errorf("sync legacy default project: config store is required")
	}
	store, ok := c.store.(legacyAgentDefinitionStore)
	if !ok {
		return nil, fmt.Errorf("sync legacy default project: agent definition store is required")
	}
	var standalone []domain.AgentDefinition
	for offset := 0; ; {
		page, err := store.ListAgentDefinitions(ctx, domain.AgentDefinitionListOptions{
			IncludeDisabled: true,
			Offset:          offset,
			Limit:           200,
		})
		if err != nil {
			return nil, fmt.Errorf("list legacy agent definitions: %w", err)
		}
		for _, agent := range page.Agents {
			if agent.DeletedAt.IsZero() && strings.TrimSpace(agent.ManagedProjectID) == "" {
				standalone = append(standalone, agent)
			}
		}
		if !page.HasMore {
			break
		}
		offset = page.NextOffset
	}
	return standalone, nil
}

func (c *Controller) listLegacyDefaultLoaders(ctx context.Context) ([]domain.Loader, error) {
	if c == nil || c.store == nil {
		return nil, fmt.Errorf("sync legacy default project: config store is required")
	}
	store, ok := c.store.(legacyLoaderStore)
	if !ok {
		return nil, fmt.Errorf("sync legacy default project: loader store is required")
	}
	projectID, err := domain.StableProjectID(LegacyDefaultProjectName, "")
	if err != nil {
		return nil, fmt.Errorf("resolve legacy default project id: %w", err)
	}
	items, err := store.ListLoaders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list legacy loaders: %w", err)
	}
	legacy := make([]domain.Loader, 0, len(items))
	for _, loader := range items {
		managedProjectID := strings.TrimSpace(loader.Summary.ManagedProjectID)
		if managedProjectID == "" || managedProjectID == projectID {
			legacy = append(legacy, loader)
		}
	}
	return legacy, nil
}

func legacyDefaultNormalizedProject(agents []domain.AgentDefinition, legacyLoaders []domain.Loader) (NormalizedProject, error) {
	return legacyDefaultNormalizedProjectWithWorkspaces(agents, legacyLoaders, legacyWorkspaceProjection{})
}

func legacyDefaultNormalizedProjectWithWorkspaces(agents []domain.AgentDefinition, legacyLoaders []domain.Loader, workspaceProjection legacyWorkspaceProjection) (NormalizedProject, error) {
	spec := &compose.NormalizedProjectSpec{
		Name:       LegacyDefaultProjectName,
		Workspaces: workspaceProjection.workspaces,
	}
	agents = append([]domain.AgentDefinition(nil), agents...)
	sort.Slice(agents, func(i, j int) bool {
		if agents[i].Name == agents[j].Name {
			return agents[i].ID < agents[j].ID
		}
		return agents[i].Name < agents[j].Name
	})
	names := legacyProjectAgentNames(agents)
	agentByLegacyID := make(map[string]int, len(agents))
	agentByName := make(map[string]int, len(agents)+len(legacyLoaders))
	usedNames := make(map[string]struct{}, len(agents)+len(legacyLoaders))
	for index, definition := range agents {
		agent, err := normalizedAgentFromLegacy(definition)
		if err != nil {
			return NormalizedProject{}, err
		}
		agent.Name = names[index]
		agent.DisplayName = distinctLegacyDisplayName(agent.DisplayName, agent.Name)
		agent.Workspace, err = workspaceProjection.reference(definition.WorkspaceID)
		if err != nil {
			return NormalizedProject{}, fmt.Errorf("map legacy agent %s workspace: %w", definition.ID, err)
		}
		if legacyID := strings.TrimSpace(definition.ID); legacyID != "" {
			agentByLegacyID[legacyID] = len(spec.Agents)
		}
		agentByName[agent.Name] = len(spec.Agents)
		usedNames[agent.Name] = struct{}{}
		spec.Agents = append(spec.Agents, agent)
	}

	overrides, err := projectLegacyLoaders(spec, legacyLoaders, agentByLegacyID, agentByName, usedNames, workspaceProjection)
	if err != nil {
		return NormalizedProject{}, err
	}
	removeImplicitLegacyWorkspaceDefault(spec)
	sort.Slice(spec.Agents, func(i, j int) bool { return spec.Agents[i].Name < spec.Agents[j].Name })
	hash, err := spec.Hash()
	if err != nil {
		return NormalizedProject{}, fmt.Errorf("hash legacy default project: %w", err)
	}
	return NormalizedProject{Spec: spec, SpecHash: hash, managedLoaderOverrides: overrides}, nil
}

func legacyCanonicalAgentName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if !domain.IsProjectStableIdentifier(name) {
		return ""
	}
	return name
}

func legacyProjectAgentNames(agents []domain.AgentDefinition) []string {
	bases := make([]string, len(agents))
	counts := make(map[string]int, len(agents))
	for index, agent := range agents {
		bases[index] = legacyCanonicalAgentName(agent.Name)
		if bases[index] != "" {
			counts[bases[index]]++
		}
	}

	names := make([]string, len(agents))
	used := make(map[string]struct{}, len(agents))
	// Reserve unique, already-valid names first. A generated compatibility name
	// must never force a valid legacy name to change.
	for index, base := range bases {
		if base != "" && counts[base] == 1 {
			names[index] = base
			used[base] = struct{}{}
		}
	}
	for index, agent := range agents {
		if names[index] != "" {
			continue
		}
		prefix := bases[index]
		if prefix == "" {
			prefix = "legacy-agent"
		}
		names[index] = reserveLegacyStableName(prefix, identity.ResourceAgent, agent.ID, agent.Name, used)
	}
	return names
}

func projectLegacyLoaders(spec *compose.NormalizedProjectSpec, legacyLoaders []domain.Loader, agentByLegacyID, agentByName map[string]int, usedNames map[string]struct{}, workspaceProjection legacyWorkspaceProjection) (map[string]domain.Loader, error) {
	legacyLoaders = append([]domain.Loader(nil), legacyLoaders...)
	sort.Slice(legacyLoaders, func(i, j int) bool {
		iManaged := strings.TrimSpace(legacyLoaders[i].Summary.ManagedAgentName) != ""
		jManaged := strings.TrimSpace(legacyLoaders[j].Summary.ManagedAgentName) != ""
		if iManaged != jManaged {
			return iManaged
		}
		if legacyLoaders[i].Summary.ID != legacyLoaders[j].Summary.ID {
			return legacyLoaders[i].Summary.ID < legacyLoaders[j].Summary.ID
		}
		return legacyLoaders[i].Summary.Name < legacyLoaders[j].Summary.Name
	})

	scheduledAgents := make(map[string]struct{}, len(legacyLoaders))
	overrides := make(map[string]domain.Loader, len(legacyLoaders))
	for _, loader := range legacyLoaders {
		sourceIndex, sourceFound := agentByLegacyID[strings.TrimSpace(loader.Summary.AgentID)]
		managedName := legacyCanonicalAgentName(loader.Summary.ManagedAgentName)
		trustedManagedBinding := managedName != ""
		targetIndex := -1
		targetCreated := false

		if managedName != "" {
			if index, exists := agentByName[managedName]; exists {
				if _, scheduled := scheduledAgents[managedName]; !scheduled {
					targetIndex = index
				}
			} else {
				targetIndex = appendLegacyLoaderAgent(spec, loader, managedName, sourceIndex, sourceFound, true)
				targetCreated = true
				agentByName[managedName] = targetIndex
				usedNames[managedName] = struct{}{}
			}
		}
		if targetIndex < 0 && sourceFound {
			sourceName := spec.Agents[sourceIndex].Name
			if _, scheduled := scheduledAgents[sourceName]; !scheduled {
				targetIndex = sourceIndex
			}
		}
		associated := sourceFound || trustedManagedBinding
		if targetIndex < 0 {
			prefix := "legacy-loader"
			if sourceFound {
				prefix = spec.Agents[sourceIndex].Name + "-loader"
			}
			targetName := reserveLegacyStableName(prefix, identity.ResourceLoader, loader.Summary.ID, loader.Summary.Name, usedNames)
			targetIndex = appendLegacyLoaderAgent(spec, loader, targetName, sourceIndex, sourceFound, associated)
			targetCreated = true
			agentByName[targetName] = targetIndex
		}
		var err error
		targetIndex, err = projectLegacyLoaderWorkspace(spec, loader, targetIndex, targetCreated, agentByName, usedNames, workspaceProjection)
		if err != nil {
			return nil, err
		}

		targetName := spec.Agents[targetIndex].Name
		projected := loader
		projected.Triggers = append([]domain.LoaderTrigger(nil), loader.Triggers...)
		projected.EnvItems = append([]domain.SandboxEnvVar(nil), loader.EnvItems...)
		projected.Volumes = append([]domain.VolumeMountSpec(nil), loader.Volumes...)
		projected.Summary.CapsetIDs = append([]string(nil), loader.Summary.CapsetIDs...)
		projected.Script = strings.ReplaceAll(loader.Script, "\r\n", "\n")
		if !associated {
			// An unbound global loader has no lossless project-agent target. Keep it
			// visible, but do not let migration unexpectedly execute it.
			projected.Summary.Enabled = false
		}
		spec.Agents[targetIndex].Scheduler = &compose.NormalizedSchedulerSpec{
			Enabled:       projected.Summary.Enabled,
			SandboxPolicy: domain.NormalizeLoaderSandboxPolicy(projected.Summary.SandboxPolicy),
			DisplayName:   strings.TrimSpace(projected.Summary.Name),
			Description:   strings.TrimSpace(projected.Summary.Description),
			Script:        projected.Script,
		}
		scheduledAgents[targetName] = struct{}{}
		overrides[targetName] = projected
	}
	if len(overrides) == 0 {
		return nil, nil
	}
	return overrides, nil
}

func projectLegacyLoaderWorkspace(spec *compose.NormalizedProjectSpec, loader domain.Loader, targetIndex int, targetCreated bool, agentByName map[string]int, usedNames map[string]struct{}, workspaceProjection legacyWorkspaceProjection) (int, error) {
	workspace, err := workspaceProjection.reference(loader.Summary.WorkspaceID)
	if err != nil {
		return -1, fmt.Errorf("map legacy loader %s workspace: %w", loader.Summary.ID, err)
	}
	existing := spec.Agents[targetIndex].Workspace
	if workspace == nil {
		return targetIndex, nil
	}
	if targetCreated || existing != nil && existing.Name == workspace.Name {
		spec.Agents[targetIndex].Workspace = workspace
		return targetIndex, nil
	}

	// A loader-specific workspace cannot be assigned to a workspace-less source
	// agent without changing that agent's manual-run behavior. Preserve both v1
	// meanings by creating a dedicated compatibility scheduler agent.
	source := spec.Agents[targetIndex]
	name := reserveLegacyStableName(source.Name+"-loader", identity.ResourceLoader, loader.Summary.ID, loader.Summary.Name, usedNames)
	targetIndex = appendLegacyLoaderAgent(spec, loader, name, targetIndex, true, true)
	spec.Agents[targetIndex].Workspace = workspace
	agentByName[name] = targetIndex
	return targetIndex, nil
}

func appendLegacyLoaderAgent(spec *compose.NormalizedProjectSpec, loader domain.Loader, name string, sourceIndex int, sourceFound, associated bool) int {
	var agent compose.NormalizedAgentSpec
	if sourceFound {
		agent = spec.Agents[sourceIndex]
		agent.Scheduler = nil
	} else {
		agent = compose.NormalizedAgentSpec{
			Enabled:   associated && loader.Summary.Enabled,
			Provider:  legacyLoaderProvider(loader.Summary.DefaultAgent),
			Image:     strings.TrimSpace(loader.Summary.GuestImage),
			Driver:    legacyDriver(loader.Summary.Driver),
			Env:       legacyEnv(loader.EnvItems),
			CapsetIDs: append([]string(nil), loader.Summary.CapsetIDs...),
			Volumes:   legacyVolumes(loader.Volumes),
		}
	}
	agent.Name = name
	displayName := strings.TrimSpace(loader.Summary.Name)
	if displayName == "" && sourceFound {
		displayName = projectAgentDisplayName(spec.Agents[sourceIndex])
	}
	agent.DisplayName = distinctLegacyDisplayName(displayName, name)
	if description := strings.TrimSpace(loader.Summary.Description); description != "" {
		agent.Description = description
	}
	spec.Agents = append(spec.Agents, agent)
	return len(spec.Agents) - 1
}

func legacyLoaderProvider(provider string) string {
	provider = domain.NormalizeAgentKind(provider)
	switch provider {
	case "codex", "claude", "gemini", "opencode":
		return provider
	default:
		return domain.DefaultAgentProvider
	}
}

func reserveLegacyStableName(prefix string, kind identity.ResourceKind, id, fallback string, used map[string]struct{}) string {
	seed := strings.TrimSpace(id)
	if seed == "" {
		seed = strings.TrimSpace(fallback)
	}
	digest := identity.NewID(kind, LegacyDefaultProjectName, seed)
	for length := 12; length <= len(digest); length += 4 {
		if length > len(digest) {
			length = len(digest)
		}
		candidate := prefix + "-" + digest[:length]
		if _, exists := used[candidate]; !exists {
			used[candidate] = struct{}{}
			return candidate
		}
	}
	for sequence := 2; ; sequence++ {
		candidate := fmt.Sprintf("%s-%s-%d", prefix, digest, sequence)
		if _, exists := used[candidate]; !exists {
			used[candidate] = struct{}{}
			return candidate
		}
	}
}

func normalizedAgentFromLegacy(definition domain.AgentDefinition) (compose.NormalizedAgentSpec, error) {
	config, err := decodeLegacyAgentConfig(definition.ConfigJSON)
	if err != nil {
		return compose.NormalizedAgentSpec{}, fmt.Errorf("decode legacy agent %s config: %w", definition.ID, err)
	}
	agent := compose.NormalizedAgentSpec{
		Name:         strings.TrimSpace(definition.Name),
		DisplayName:  strings.TrimSpace(definition.Name),
		Description:  strings.TrimSpace(definition.Description),
		Enabled:      definition.Enabled,
		Provider:     strings.TrimSpace(definition.Provider),
		Model:        strings.TrimSpace(definition.Model),
		SystemPrompt: definition.SystemPrompt,
		Image:        strings.TrimSpace(definition.GuestImage),
		Driver:       legacyDriver(definition.Driver),
		Env:          legacyEnv(definition.EnvItems),
		CapsetIDs:    append([]string(nil), definition.CapsetIDs...),
		Skills:       legacySkills(definition.Skills),
		Volumes:      legacyVolumes(definition.Volumes),
		Jupyter:      config.Jupyter,
		MCPServers:   config.MCPServers,
	}
	return agent, nil
}

func distinctLegacyDisplayName(displayName, stableName string) string {
	displayName = strings.TrimSpace(displayName)
	if displayName == strings.TrimSpace(stableName) {
		return ""
	}
	return displayName
}

type legacyAgentConfig struct {
	Jupyter    *compose.JupyterSpec                       `json:"jupyter,omitempty"`
	MCPServers map[string]compose.NormalizedMCPServerSpec `json:"mcp_servers,omitempty"`
}

func decodeLegacyAgentConfig(raw string) (legacyAgentConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return legacyAgentConfig{}, nil
	}
	var config legacyAgentConfig
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return legacyAgentConfig{}, err
	}
	return config, nil
}

func legacyDriver(name string) *compose.NormalizedDriverSpec {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	driver := &compose.NormalizedDriverSpec{Name: name}
	switch name {
	case compose.DriverDocker:
		driver.Docker = &compose.DockerDriverSpec{}
	case compose.DriverBoxlite:
		driver.Boxlite = &compose.BoxliteDriverSpec{}
	case compose.DriverMicrosandbox:
		driver.Microsandbox = &compose.MicrosandboxDriverSpec{}
	}
	return driver
}

func legacyEnv(items []domain.SandboxEnvVar) map[string]compose.EnvVarSpec {
	if len(items) == 0 {
		return nil
	}
	result := make(map[string]compose.EnvVarSpec, len(items))
	for _, item := range items {
		result[item.Name] = compose.EnvVarSpec{Value: item.Value, Secret: item.Secret}
	}
	return result
}

func legacyVolumes(items []domain.VolumeMountSpec) []compose.NormalizedVolumeMountSpec {
	result := make([]compose.NormalizedVolumeMountSpec, 0, len(items))
	for _, item := range items {
		result = append(result, compose.NormalizedVolumeMountSpec{Type: item.Type, Source: item.Source, Target: item.Target, ReadOnly: item.ReadOnly})
	}
	return result
}

func legacySkills(items []domain.AgentSkill) []compose.NormalizedSkillSpec {
	result := make([]compose.NormalizedSkillSpec, 0, len(items))
	for _, item := range items {
		result = append(result, compose.NormalizedSkillSpec{Name: item.Name, Provider: item.Provider, URL: item.URL, Path: item.Path, Ref: item.Ref, Format: item.Format, Username: item.Username, Password: item.Password, Token: item.Token})
	}
	return result
}
