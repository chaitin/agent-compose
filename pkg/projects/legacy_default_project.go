package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
)

const LegacyDefaultProjectName = "legacy-v1-default"

var legacyAgentNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

type legacyAgentDefinitionStore interface {
	ListAgentDefinitions(context.Context, domain.AgentDefinitionListOptions) (domain.AgentDefinitionListResult, error)
}

// SyncLegacyDefaultProject projects active standalone v1 agents into one
// deterministic v2 project without changing the source AgentDefinitions.
func (c *Controller) SyncLegacyDefaultProject(ctx context.Context) (ApplyResult, error) {
	agents, err := c.listStandaloneAgents(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	if len(agents) == 0 {
		return ApplyResult{}, nil
	}
	normalized, err := legacyDefaultNormalizedProject(agents)
	if err != nil {
		return ApplyResult{}, err
	}
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

func legacyDefaultNormalizedProject(agents []domain.AgentDefinition) (NormalizedProject, error) {
	spec := &compose.NormalizedProjectSpec{Name: LegacyDefaultProjectName}
	agents = append([]domain.AgentDefinition(nil), agents...)
	sort.Slice(agents, func(i, j int) bool {
		if agents[i].Name == agents[j].Name {
			return agents[i].ID < agents[j].ID
		}
		return agents[i].Name < agents[j].Name
	})
	seen := make(map[string]string, len(agents))
	for _, definition := range agents {
		name, err := legacyProjectAgentName(definition.Name)
		if err != nil {
			return NormalizedProject{}, fmt.Errorf("legacy agent %s: %w", definition.ID, err)
		}
		if name == "" {
			return NormalizedProject{}, fmt.Errorf("legacy agent %s has no name", definition.ID)
		}
		if existingID, exists := seen[name]; exists {
			return NormalizedProject{}, fmt.Errorf("legacy agents %s and %s share name %q", existingID, definition.ID, name)
		}
		seen[name] = definition.ID
		agent, err := normalizedAgentFromLegacy(definition)
		if err != nil {
			return NormalizedProject{}, err
		}
		agent.Name = name
		spec.Agents = append(spec.Agents, agent)
	}
	hash, err := spec.Hash()
	if err != nil {
		return NormalizedProject{}, fmt.Errorf("hash legacy default project: %w", err)
	}
	return NormalizedProject{Spec: spec, SpecHash: hash}, nil
}

func legacyProjectAgentName(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "", nil
	}
	if !legacyAgentNamePattern.MatchString(name) {
		return "", fmt.Errorf("legacy agent name %q cannot be converted to a stable v2 identifier", name)
	}
	return name, nil
}

func normalizedAgentFromLegacy(definition domain.AgentDefinition) (compose.NormalizedAgentSpec, error) {
	if strings.TrimSpace(definition.WorkspaceID) != "" {
		return compose.NormalizedAgentSpec{}, fmt.Errorf("legacy agent %s uses workspace preset %s, which cannot be projected losslessly", definition.ID, definition.WorkspaceID)
	}
	config, err := decodeLegacyAgentConfig(definition.ConfigJSON)
	if err != nil {
		return compose.NormalizedAgentSpec{}, fmt.Errorf("decode legacy agent %s config: %w", definition.ID, err)
	}
	agent := compose.NormalizedAgentSpec{
		Name:         strings.TrimSpace(definition.Name),
		Status:       map[bool]string{true: "enabled", false: "disabled"}[definition.Enabled],
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
		MCPs:         config.MCPs,
	}
	return agent, nil
}

type legacyAgentConfig struct {
	Jupyter *compose.JupyterSpec                       `json:"jupyter,omitempty"`
	MCPs    map[string]compose.NormalizedMCPServerSpec `json:"mcps,omitempty"`
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
	return &compose.NormalizedDriverSpec{Name: name}
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
		result = append(result, compose.NormalizedSkillSpec{Name: item.Name, Source: item.Source, URL: item.URL, Path: item.Path, Ref: item.Ref, Username: item.Username, Password: item.Password, Token: item.Token})
	}
	return result
}
