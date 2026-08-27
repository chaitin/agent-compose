package projects

import (
	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/compose"
	"agent-compose/pkg/identity"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/schedulers"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-compose/pkg/capability"
)

func NewRecordFromSpec(spec *compose.NormalizedProjectSpec, sourcePath string) (domain.ProjectRecord, error) {
	if spec == nil {
		return domain.ProjectRecord{}, fmt.Errorf("project spec is required")
	}
	sourcePath = NormalizeProjectSourcePath(sourcePath)
	projectID, err := StableProjectID(spec.Name, sourcePath)
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
	projectAgentID, err := StableProjectAgentID(projectID, agent.Name)
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
		ID:               projectAgentID,
		Name:             strings.TrimSpace(agent.Name),
		ShortID:          identity.ShortID(projectAgentID),
		ProjectID:        strings.TrimSpace(projectID),
		AgentName:        strings.TrimSpace(agent.Name),
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
		record, err := NewAgentDefinitionFromSpec(project, revision, agent, AgentDefinitionProjectRefs{MCPServers: spec.MCPServers, OctoBusServers: spec.OctoBusServers})
		if err != nil {
			return nil, err
		}
		agents = append(agents, record)
	}
	return agents, nil
}

// AgentDefinitionProjectRefs holds the project-level MCP/OctoBus server
// definitions an agent's config may reference by name.
type AgentDefinitionProjectRefs struct {
	MCPServers     map[string]compose.NormalizedMCPServerSpec
	OctoBusServers map[string]compose.NormalizedOctoBusServerSpec
}

func NewAgentDefinitionFromSpec(project domain.ProjectRecord, revision int64, agent compose.NormalizedAgentSpec, projectRefs AgentDefinitionProjectRefs) (domain.AgentDefinition, error) {
	projectAgentID, err := StableProjectAgentID(project.ID, agent.Name)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	configJSON, err := agentDefinitionConfigJSON(agent, projectRefs.MCPServers, projectRefs.OctoBusServers)
	if err != nil {
		return domain.AgentDefinition{}, fmt.Errorf("marshal managed agent %s config: %w", agent.Name, err)
	}
	driver := ""
	if agent.Driver != nil {
		driver = agent.Driver.Name
	}
	workspaceID := ""
	if agent.Workspace != nil {
		workspaceID = strings.TrimSpace(agent.Workspace.Name)
	}
	return domain.AgentDefinition{
		ID:              projectAgentID,
		Name:            agent.Name,
		Description:     agent.Description,
		Enabled:         agent.Enabled,
		Provider:        agent.Provider,
		Model:           agent.Model,
		SystemPrompt:    agent.SystemPrompt,
		Driver:          driver,
		GuestImage:      agent.Image,
		WorkspaceID:     workspaceID,
		EnvItems:        SandboxEnvItemsFromCompose(agent.Env),
		Volumes:         VolumeMountSpecsFromCompose(agent.Volumes),
		ConfigJSON:      configJSON,
		CapsetIDs:       capabilities.NormalizeCapsetIDs(agent.CapsetIDs),
		Skills:          AgentSkillsFromCompose(agent.Skills, project.SourcePath),
		ProjectID:       project.ID,
		ProjectRevision: revision,
		AgentName:       agent.Name,
	}, nil
}

type agentDefinitionConfig struct {
	InputSchema    *compose.JSONSchema                            `json:"input_schema,omitempty"`
	OutputSchema   *compose.JSONSchema                            `json:"output_schema,omitempty"`
	Jupyter        *compose.JupyterSpec                           `json:"jupyter,omitempty"`
	Sandbox        *compose.NormalizedSandboxSpec                 `json:"sandbox,omitempty"`
	MCPServers     map[string]compose.NormalizedMCPServerSpec     `json:"mcp_servers,omitempty"`
	OctoBusServers map[string]compose.NormalizedOctoBusServerSpec `json:"octobus_servers,omitempty"`
	// Workspace carries the full yaml `workspace:` declaration (provider,
	// url/path, ref, target, credentials). AgentDefinition.WorkspaceID only
	// keeps the yaml `name` label, which is not a workspace_config preset id
	// (see issue #599), so runtime code must read the inline spec from here
	// to actually resolve the declared workspace instead of looking it up as
	// a preset.
	Workspace *compose.WorkspaceSpec `json:"workspace,omitempty"`
}

func agentDefinitionConfigJSON(agent compose.NormalizedAgentSpec, projectMCPServers map[string]compose.NormalizedMCPServerSpec, projectOctoBusServers map[string]compose.NormalizedOctoBusServerSpec) (string, error) {
	payload := agentDefinitionConfig{
		InputSchema:    agent.InputSchema,
		OutputSchema:   agent.OutputSchema,
		Jupyter:        agent.Jupyter,
		Sandbox:        agent.Sandbox,
		MCPServers:     selectedAgentMCPServers(agent, projectMCPServers),
		OctoBusServers: selectedAgentOctoBusServers(agent, projectOctoBusServers),
		Workspace:      agent.Workspace,
	}
	if payload.InputSchema == nil && payload.OutputSchema == nil && payload.Jupyter == nil && payload.Sandbox == nil && payload.Workspace == nil && len(payload.MCPServers) == 0 && len(payload.OctoBusServers) == 0 {
		return "{}", nil
	}
	data, err := MarshalCanonicalJSON(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func selectedAgentOctoBusServers(agent compose.NormalizedAgentSpec, projectServers map[string]compose.NormalizedOctoBusServerSpec) map[string]compose.NormalizedOctoBusServerSpec {
	var selected map[string]compose.NormalizedOctoBusServerSpec
	for _, declaration := range agent.CapsetIDs {
		parsed, err := capability.ParseCapsetDeclaration(declaration)
		if err != nil || !parsed.Qualified() {
			continue
		}
		server, ok := projectServers[parsed.ServerName]
		if !ok {
			continue
		}
		if selected == nil {
			selected = make(map[string]compose.NormalizedOctoBusServerSpec)
		}
		selected[parsed.ServerName] = server
	}
	return selected
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

func NewSchedulerDefinition(project domain.ProjectRecord, scheduler domain.ProjectSchedulerRecord, agent compose.NormalizedAgentSpec) (domain.Scheduler, error) {
	projectAgentID, err := StableProjectAgentID(project.ID, agent.Name)
	if err != nil {
		return domain.Scheduler{}, err
	}
	driver := ""
	if agent.Driver != nil {
		driver = agent.Driver.Name
	}
	workspaceID := ""
	if agent.Workspace != nil {
		workspaceID = strings.TrimSpace(agent.Workspace.Name)
	}
	var triggers []domain.SchedulerTrigger
	script := agent.Scheduler.Script
	if strings.TrimSpace(script) == "" {
		var err error
		triggers, script, err = ProjectSchedulerTriggersAndScript(project.ID, agent.Name, "", agent.Scheduler)
		if err != nil {
			return domain.Scheduler{}, err
		}
	}
	return domain.Scheduler{
		Summary: domain.SchedulerSummary{
			ID:                 scheduler.ID,
			Name:               projectSchedulerDisplayName(project.Name, agent),
			Description:        strings.TrimSpace(agent.Scheduler.Description),
			Enabled:            scheduler.Enabled,
			Runtime:            domain.SchedulerRuntimeScheduler,
			AgentID:            projectAgentID,
			Driver:             driver,
			GuestImage:         agent.Image,
			WorkspaceID:        workspaceID,
			DefaultAgent:       agent.Provider,
			SandboxPolicy:      agent.Scheduler.SandboxPolicy,
			ConcurrencyPolicy:  schedulers.NormalizeConcurrencyPolicy(agent.Scheduler.ConcurrencyPolicy),
			CapsetIDs:          capabilities.NormalizeCapsetIDs(agent.CapsetIDs),
			ProjectID:          project.ID,
			ProjectRevision:    scheduler.Revision,
			AgentName:          agent.Name,
			ProjectSchedulerID: scheduler.SchedulerID,
		},
		Script:     script,
		Model:      strings.TrimSpace(agent.Scheduler.Model),
		AgentModel: strings.TrimSpace(agent.Model),
		Triggers:   triggers,
		EnvItems:   SandboxEnvItemsFromCompose(agent.Env),
		Volumes:    VolumeMountSpecsFromCompose(agent.Volumes),
	}, nil
}

func projectSchedulerDisplayName(projectName string, agent compose.NormalizedAgentSpec) string {
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
	sourcePath = NormalizeProjectSourcePath(sourcePath)
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
	Record             domain.ProjectSchedulerRecord
	Definition         domain.Scheduler
	ValidationTriggers []domain.SchedulerTrigger
}

func SchedulerRecords(builds []SchedulerBuild) []domain.ProjectSchedulerRecord {
	schedulers := make([]domain.ProjectSchedulerRecord, 0, len(builds))
	for _, build := range builds {
		schedulers = append(schedulers, build.Record)
	}
	return schedulers
}

func syncProjectAgentSchedulerState(agents []domain.ProjectAgentRecord, schedulers []domain.ProjectSchedulerRecord) {
	enabledByAgent := make(map[string]bool, len(schedulers))
	for _, scheduler := range schedulers {
		enabledByAgent[scheduler.AgentName] = scheduler.Enabled
	}
	for index := range agents {
		if enabled, ok := enabledByAgent[agents[index].AgentName]; ok {
			agents[index].SchedulerEnabled = enabled
		}
	}
}

func SchedulerDefinitions(builds []SchedulerBuild) []domain.Scheduler {
	definitions := make([]domain.Scheduler, 0, len(builds))
	for _, build := range builds {
		definitions = append(definitions, build.Definition)
	}
	return definitions
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
		definition, err := NewSchedulerDefinition(project, record, agent)
		if err != nil {
			return nil, err
		}
		builds = append(builds, SchedulerBuild{
			Record:             record,
			Definition:         definition,
			ValidationTriggers: definition.Triggers,
		})
	}
	return builds, nil
}

func NewSchedulerRecordFromSpec(projectID string, revision int64, agent compose.NormalizedAgentSpec) (domain.ProjectSchedulerRecord, bool, error) {
	if agent.Scheduler == nil {
		return domain.ProjectSchedulerRecord{}, false, nil
	}
	schedulerID, err := StableProjectSchedulerID(projectID, agent.Name, "")
	if err != nil {
		return domain.ProjectSchedulerRecord{}, false, err
	}
	specJSON, err := MarshalCanonicalJSON(agent.Scheduler)
	if err != nil {
		return domain.ProjectSchedulerRecord{}, false, fmt.Errorf("marshal project scheduler %s spec: %w", agent.Name, err)
	}
	return domain.ProjectSchedulerRecord{
		ID:           schedulerID,
		ShortID:      identity.ShortID(schedulerID),
		ProjectID:    strings.TrimSpace(projectID),
		SchedulerID:  schedulerID,
		AgentName:    strings.TrimSpace(agent.Name),
		Revision:     revision,
		Enabled:      agent.Enabled && agent.Scheduler.Enabled,
		TriggerCount: len(agent.Scheduler.Triggers),
		SpecJSON:     string(specJSON),
	}, true, nil
}

func EncodeSourceJSON(sourcePath string) (string, error) {
	data, err := json.Marshal(struct {
		ComposePath string `json:"compose_path,omitempty"`
	}{ComposePath: NormalizeProjectSourcePath(sourcePath)})
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
