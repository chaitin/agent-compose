package projects

import (
	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
	"encoding/json"
	"fmt"
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
		ProjectID:        strings.TrimSpace(projectID),
		AgentName:        strings.TrimSpace(agent.Name),
		ManagedAgentID:   managedAgentID,
		Revision:         revision,
		Provider:         strings.TrimSpace(agent.Provider),
		Model:            strings.TrimSpace(agent.Model),
		Image:            strings.TrimSpace(agent.Image),
		Driver:           strings.TrimSpace(driver),
		SchedulerEnabled: agent.Scheduler != nil && agent.Scheduler.Enabled,
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
		record, err := NewAgentDefinitionFromSpec(project, revision, agent)
		if err != nil {
			return nil, err
		}
		agents = append(agents, record)
	}
	return agents, nil
}

func NewAgentDefinitionFromSpec(project domain.ProjectRecord, revision int64, agent compose.NormalizedAgentSpec) (domain.AgentDefinition, error) {
	managedAgentID, err := domain.StableManagedAgentID(project.ID, agent.Name)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	configJSON, err := agentDefinitionConfigJSON(agent)
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
		Enabled:                true,
		Provider:               agent.Provider,
		Model:                  agent.Model,
		SystemPrompt:           agent.SystemPrompt,
		Driver:                 driver,
		GuestImage:             agent.Image,
		EnvItems:               SessionEnvItemsFromCompose(agent.Env),
		ConfigJSON:             configJSON,
		CapsetIDs:              capabilities.NormalizeCapsetIDs(agent.CapsetIDs),
		ManagedProjectID:       project.ID,
		ManagedProjectRevision: revision,
		ManagedAgentName:       agent.Name,
	}, nil
}

func agentDefinitionConfigJSON(agent compose.NormalizedAgentSpec) (string, error) {
	if agent.Jupyter == nil {
		return "{}", nil
	}
	data, err := MarshalCanonicalJSON(struct {
		Jupyter *compose.JupyterSpec `json:"jupyter,omitempty"`
	}{
		Jupyter: agent.Jupyter,
	})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func NewSchedulerExecutionFromRecord(project domain.ProjectRecord, scheduler domain.ProjectSchedulerRecord, agent compose.NormalizedAgentSpec) (domain.Loader, error) {
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
			Name:               fmt.Sprintf("%s/%s scheduler", project.Name, agent.Name),
			Enabled:            scheduler.Enabled,
			Runtime:            domain.LoaderRuntimeScheduler,
			AgentID:            managedAgentID,
			Driver:             driver,
			GuestImage:         agent.Image,
			DefaultAgent:       agent.Provider,
			SessionPolicy:      domain.LoaderSessionPolicyNew,
			ConcurrencyPolicy:  domain.LoaderConcurrencyPolicySkip,
			CapsetIDs:          capabilities.NormalizeCapsetIDs(agent.CapsetIDs),
			ManagedProjectID:   project.ID,
			ManagedRevision:    scheduler.Revision,
			ManagedAgentName:   agent.Name,
			ManagedSchedulerID: scheduler.SchedulerID,
		},
		Script:   script,
		Triggers: triggers,
		EnvItems: SessionEnvItemsFromCompose(agent.Env),
	}, nil
}

type SchedulerBuild struct {
	Scheduler          domain.ProjectSchedulerRecord
	Execution          domain.Loader
	ValidationTriggers []domain.LoaderTrigger
}

func SchedulerRecords(builds []SchedulerBuild) []domain.ProjectSchedulerRecord {
	schedulers := make([]domain.ProjectSchedulerRecord, 0, len(builds))
	for _, build := range builds {
		schedulers = append(schedulers, build.Scheduler)
	}
	return schedulers
}

func SchedulerExecutions(builds []SchedulerBuild) []domain.Loader {
	executions := make([]domain.Loader, 0, len(builds))
	for _, build := range builds {
		executions = append(executions, build.Execution)
	}
	return executions
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
		execution, err := NewSchedulerExecutionFromRecord(project, record, agent)
		if err != nil {
			return nil, err
		}
		builds = append(builds, SchedulerBuild{
			Scheduler:          record,
			Execution:          execution,
			ValidationTriggers: execution.Triggers,
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
		ProjectID:       strings.TrimSpace(projectID),
		SchedulerID:     schedulerID,
		AgentName:       strings.TrimSpace(agent.Name),
		ManagedLoaderID: loaderID,
		Revision:        revision,
		Enabled:         agent.Scheduler.Enabled,
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
