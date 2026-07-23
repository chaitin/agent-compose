package projects

import (
	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/compose"
	"agent-compose/pkg/identity"
	domain "agent-compose/pkg/model"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func NewRecordFromSpec(spec *compose.NormalizedProjectSpec, sourcePath string) (domain.ProjectRecord, error) {
	if spec == nil {
		return domain.ProjectRecord{}, fmt.Errorf("project spec is required")
	}
	sourcePath = domain.NormalizeProjectSourcePath(sourcePath)
	projectID, err := domain.StableProjectID(spec.Name, sourcePath)
	if err != nil {
		return domain.ProjectRecord{}, err
	}
	specHash, err := spec.Hash()
	if err != nil {
		return domain.ProjectRecord{}, fmt.Errorf("hash project spec: %w", err)
	}
	sourceJSON, err := EncodeSourceJSON(sourcePath)
	if err != nil {
		return domain.ProjectRecord{}, err
	}
	return domain.ProjectRecord{
		ID:         projectID,
		Name:       strings.TrimSpace(spec.Name),
		ShortID:    identity.ShortID(projectID),
		SourcePath: sourcePath,
		SourceJSON: sourceJSON,
		SpecHash:   specHash,
	}, nil
}

func NewAgentRecordFromSpec(projectID string, revision int64, agent compose.NormalizedAgentSpec) (domain.ProjectAgentRecord, error) {
	managedAgentID, err := domain.StableManagedAgentID(projectID, agent.Name)
	if err != nil {
		return domain.ProjectAgentRecord{}, err
	}
	specJSON, err := MarshalCanonicalJSON(agent)
	if err != nil {
		return domain.ProjectAgentRecord{}, fmt.Errorf("marshal project agent %s spec: %w", agent.Name, err)
	}
	driver := ""
	if agent.Driver != nil {
		driver = agent.Driver.Name
	}
	return domain.ProjectAgentRecord{
		ID:               managedAgentID,
		Name:             strings.TrimSpace(agent.Name),
		ShortID:          identity.ShortID(managedAgentID),
		ProjectID:        strings.TrimSpace(projectID),
		AgentName:        strings.TrimSpace(agent.Name),
		ManagedAgentID:   managedAgentID,
		Revision:         revision,
		Provider:         strings.TrimSpace(agent.Provider),
		Model:            strings.TrimSpace(agent.Model),
		Image:            strings.TrimSpace(agent.Image),
		Driver:           strings.TrimSpace(driver),
		SchedulerEnabled: agent.Enabled && agent.Scheduler != nil && agent.Scheduler.Enabled,
		SpecJSON:         string(specJSON),
	}, nil
}

func NewAgentRecordsFromSpec(projectID string, revision int64, spec *compose.NormalizedProjectSpec) ([]domain.ProjectAgentRecord, error) {
	agents := make([]domain.ProjectAgentRecord, 0, len(spec.Agents))
	for _, agent := range spec.Agents {
		record, err := NewAgentRecordFromSpec(projectID, revision, agent)
		if err != nil {
			return nil, err
		}
		agents = append(agents, record)
	}
	return agents, nil
}

func NewAgentDefinitionsFromSpec(project domain.ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]domain.AgentDefinition, error) {
	agents := make([]domain.AgentDefinition, 0, len(spec.Agents))
	for _, agent := range spec.Agents {
		record, err := NewAgentDefinitionFromSpec(project, revision, agent, spec.MCPServers)
		if err != nil {
			return nil, err
		}
		agents = append(agents, record)
	}
	return agents, nil
}

func NewAgentDefinitionFromSpec(project domain.ProjectRecord, revision int64, agent compose.NormalizedAgentSpec, projectMCPServers map[string]compose.NormalizedMCPServerSpec) (domain.AgentDefinition, error) {
	managedAgentID, err := domain.StableManagedAgentID(project.ID, agent.Name)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	configJSON, err := agentDefinitionConfigJSON(agent, projectMCPServers)
	if err != nil {
		return domain.AgentDefinition{}, fmt.Errorf("marshal managed agent %s config: %w", agent.Name, err)
	}
	driver := ""
	if agent.Driver != nil {
		driver = agent.Driver.Name
	}
	return domain.AgentDefinition{
		ID:                     managedAgentID,
		Name:                   agent.Name,
		Description:            agent.Description,
		Enabled:                agent.Enabled,
		Provider:               agent.Provider,
		Model:                  agent.Model,
		SystemPrompt:           agent.SystemPrompt,
		Driver:                 driver,
		GuestImage:             agent.Image,
		EnvItems:               SandboxEnvItemsFromCompose(agent.Env),
		Volumes:                VolumeMountSpecsFromCompose(agent.Volumes),
		ConfigJSON:             configJSON,
		CapsetIDs:              capabilities.NormalizeCapsetIDs(agent.CapsetIDs),
		Skills:                 AgentSkillsFromCompose(agent.Skills, project.SourcePath),
		ManagedProjectID:       project.ID,
		ManagedProjectRevision: revision,
		ManagedAgentName:       agent.Name,
	}, nil
}

func projectAgentDisplayName(agent compose.NormalizedAgentSpec) string {
	if displayName := strings.TrimSpace(agent.DisplayName); displayName != "" {
		return displayName
	}
	return strings.TrimSpace(agent.Name)
}

type agentDefinitionConfigPayload struct {
	Jupyter    *compose.JupyterSpec                       `json:"jupyter,omitempty"`
	MCPServers map[string]compose.NormalizedMCPServerSpec `json:"mcp_servers,omitempty"`
}

func agentDefinitionConfigJSON(agent compose.NormalizedAgentSpec, projectMCPServers map[string]compose.NormalizedMCPServerSpec) (string, error) {
	payload := agentDefinitionConfigPayload{
		Jupyter:    agent.Jupyter,
		MCPServers: selectedAgentMCPServers(agent, projectMCPServers),
	}
	if payload.Jupyter == nil && len(payload.MCPServers) == 0 {
		return "{}", nil
	}
	data, err := MarshalCanonicalJSON(struct {
		Jupyter    *compose.JupyterSpec                       `json:"jupyter,omitempty"`
		MCPServers map[string]compose.NormalizedMCPServerSpec `json:"mcp_servers,omitempty"`
	}{
		Jupyter:    payload.Jupyter,
		MCPServers: payload.MCPServers,
	})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func selectedAgentMCPServers(agent compose.NormalizedAgentSpec, projectMCPServers map[string]compose.NormalizedMCPServerSpec) map[string]compose.NormalizedMCPServerSpec {
	if len(agent.MCPServers) > 0 {
		result := make(map[string]compose.NormalizedMCPServerSpec, len(agent.MCPServers))
		for name, server := range agent.MCPServers {
			result[name] = server
		}
		return result
	}
	return nil
}

func NewManagedLoaderFromScheduler(project domain.ProjectRecord, scheduler domain.ProjectSchedulerRecord, agent compose.NormalizedAgentSpec) (domain.Loader, error) {
	managedAgentID, err := domain.StableManagedAgentID(project.ID, agent.Name)
	if err != nil {
		return domain.Loader{}, err
	}
	driver := ""
	if agent.Driver != nil {
		driver = agent.Driver.Name
	}
	var triggers []domain.LoaderTrigger
	script := agent.Scheduler.Script
	if strings.TrimSpace(script) == "" {
		var err error
		triggers, script, err = ManagedLoaderTriggersAndScript(project.ID, agent.Name, "", agent.Scheduler)
		if err != nil {
			return domain.Loader{}, err
		}
	}
	return domain.Loader{
		Summary: domain.LoaderSummary{
			ID:                 scheduler.ManagedLoaderID,
			Name:               managedSchedulerDisplayName(project.Name, agent),
			Description:        strings.TrimSpace(agent.Scheduler.Description),
			Enabled:            scheduler.Enabled,
			Runtime:            domain.LoaderRuntimeScheduler,
			AgentID:            managedAgentID,
			Driver:             driver,
			GuestImage:         agent.Image,
			DefaultAgent:       agent.Provider,
			SandboxPolicy:      agent.Scheduler.SandboxPolicy,
			ConcurrencyPolicy:  domain.NormalizeLoaderConcurrencyPolicy(agent.Scheduler.ConcurrencyPolicy),
			CapsetIDs:          capabilities.NormalizeCapsetIDs(agent.CapsetIDs),
			ManagedProjectID:   project.ID,
			ManagedRevision:    scheduler.Revision,
			ManagedAgentName:   agent.Name,
			ManagedSchedulerID: scheduler.SchedulerID,
		},
		Script:   script,
		Triggers: triggers,
		EnvItems: SandboxEnvItemsFromCompose(agent.Env),
		Volumes:  VolumeMountSpecsFromCompose(agent.Volumes),
	}, nil
}

func managedSchedulerDisplayName(projectName string, agent compose.NormalizedAgentSpec) string {
	if displayName := strings.TrimSpace(agent.Scheduler.DisplayName); displayName != "" {
		return displayName
	}
	return fmt.Sprintf("%s/%s scheduler", projectName, agent.Name)
}

func VolumeMountSpecsFromCompose(values []compose.NormalizedVolumeMountSpec) []domain.VolumeMountSpec {
	if len(values) == 0 {
		return nil
	}
	out := make([]domain.VolumeMountSpec, 0, len(values))
	for _, value := range values {
		out = append(out, domain.VolumeMountSpec{
			Type:     value.Type,
			Source:   value.Source,
			Target:   value.Target,
			ReadOnly: value.ReadOnly,
		})
	}
	return out
}

func AgentSkillsFromCompose(values []compose.NormalizedSkillSpec, sourcePath string) []domain.AgentSkill {
	if len(values) == 0 {
		return nil
	}
	sourceRoot := composeSourceRoot(sourcePath)
	out := make([]domain.AgentSkill, 0, len(values))
	for _, value := range values {
		out = append(out, domain.AgentSkill{
			Name:       value.Name,
			Provider:   value.Provider,
			URL:        value.URL,
			Ref:        value.Ref,
			Path:       value.Path,
			Format:     value.Format,
			Username:   value.Username,
			Password:   value.Password,
			Token:      value.Token,
			SourceRoot: sourceRoot,
		})
	}
	return domain.NormalizeAgentSkills(out)
}

func composeSourceRoot(sourcePath string) string {
	sourcePath = domain.NormalizeProjectSourcePath(sourcePath)
	if sourcePath == "" {
		return ""
	}
	info, err := filepath.Abs(sourcePath)
	if err != nil {
		info = sourcePath
	}
	if stat, err := os.Stat(info); err == nil && stat.IsDir() {
		return filepath.Clean(info)
	}
	return filepath.Dir(filepath.Clean(info))
}

type SchedulerBuild struct {
	Scheduler          domain.ProjectSchedulerRecord
	Loader             domain.Loader
	ValidationTriggers []domain.LoaderTrigger
}

func SchedulerRecords(builds []SchedulerBuild) []domain.ProjectSchedulerRecord {
	schedulers := make([]domain.ProjectSchedulerRecord, 0, len(builds))
	for _, build := range builds {
		schedulers = append(schedulers, build.Scheduler)
	}
	return schedulers
}

func SchedulerLoaders(builds []SchedulerBuild) []domain.Loader {
	loaders := make([]domain.Loader, 0, len(builds))
	for _, build := range builds {
		loaders = append(loaders, build.Loader)
	}
	return loaders
}

func NewSchedulerBuildsFromSpec(project domain.ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]SchedulerBuild, error) {
	builds := make([]SchedulerBuild, 0)
	for _, agent := range spec.Agents {
		record, ok, err := NewSchedulerRecordFromSpec(project.ID, revision, agent)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		loader, err := NewManagedLoaderFromScheduler(project, record, agent)
		if err != nil {
			return nil, err
		}
		builds = append(builds, SchedulerBuild{
			Scheduler:          record,
			Loader:             loader,
			ValidationTriggers: loader.Triggers,
		})
	}
	return builds, nil
}

func NewSchedulerRecordFromSpec(projectID string, revision int64, agent compose.NormalizedAgentSpec) (domain.ProjectSchedulerRecord, bool, error) {
	if agent.Scheduler == nil {
		return domain.ProjectSchedulerRecord{}, false, nil
	}
	schedulerID, err := domain.StableProjectSchedulerID(projectID, agent.Name, "")
	if err != nil {
		return domain.ProjectSchedulerRecord{}, false, err
	}
	loaderID, err := domain.StableManagedLoaderID(projectID, agent.Name, "")
	if err != nil {
		return domain.ProjectSchedulerRecord{}, false, err
	}
	specJSON, err := MarshalCanonicalJSON(agent.Scheduler)
	if err != nil {
		return domain.ProjectSchedulerRecord{}, false, fmt.Errorf("marshal project scheduler %s spec: %w", agent.Name, err)
	}
	return domain.ProjectSchedulerRecord{
		ID:              schedulerID,
		ShortID:         identity.ShortID(schedulerID),
		ProjectID:       strings.TrimSpace(projectID),
		SchedulerID:     schedulerID,
		AgentName:       strings.TrimSpace(agent.Name),
		ManagedLoaderID: loaderID,
		Revision:        revision,
		Enabled:         agent.Enabled && agent.Scheduler.Enabled,
		TriggerCount:    len(agent.Scheduler.Triggers),
		SpecJSON:        string(specJSON),
	}, true, nil
}

func EncodeSourceJSON(sourcePath string) (string, error) {
	data, err := json.Marshal(struct {
		ComposePath string `json:"compose_path,omitempty"`
	}{ComposePath: domain.NormalizeProjectSourcePath(sourcePath)})
	if err != nil {
		return "", fmt.Errorf("marshal project source: %w", err)
	}
	return string(data), nil
}

func MarshalCanonicalJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return data, nil
}
