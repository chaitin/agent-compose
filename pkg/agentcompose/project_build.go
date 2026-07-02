package agentcompose

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"agent-compose/pkg/compose"
)

type projectManagedSchedulerBuild struct {
	scheduler          ProjectSchedulerRecord
	loader             Loader
	validationTriggers []LoaderTrigger
}

func projectAgentRecordsFromSpec(projectID string, revision int64, spec *compose.NormalizedProjectSpec) ([]ProjectAgentRecord, error) {
	agents := make([]ProjectAgentRecord, 0, len(spec.Agents))
	for _, agent := range spec.Agents {
		record, err := NewProjectAgentRecordFromSpec(projectID, revision, agent)
		if err != nil {
			return nil, err
		}
		agents = append(agents, record)
	}
	return agents, nil
}

func projectManagedAgentDefinitionsFromSpec(project ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]AgentDefinition, error) {
	agents := make([]AgentDefinition, 0, len(spec.Agents))
	for _, agent := range spec.Agents {
		record, err := projectManagedAgentDefinitionFromSpec(project, revision, agent)
		if err != nil {
			return nil, err
		}
		agents = append(agents, record)
	}
	return agents, nil
}

func projectManagedAgentDefinitionFromSpec(project ProjectRecord, revision int64, agent compose.NormalizedAgentSpec) (AgentDefinition, error) {
	managedAgentID, err := StableManagedAgentID(project.ID, agent.Name)
	if err != nil {
		return AgentDefinition{}, err
	}
	driver := ""
	if agent.Driver != nil {
		driver = agent.Driver.Name
	}
	return AgentDefinition{
		ID:                     managedAgentID,
		Name:                   agent.Name,
		Enabled:                true,
		Provider:               agent.Provider,
		Model:                  agent.Model,
		SystemPrompt:           agent.SystemPrompt,
		Driver:                 driver,
		GuestImage:             agent.Image,
		EnvItems:               sessionEnvItemsFromCompose(agent.Env),
		ConfigJSON:             "{}",
		CapsetIDs:              normalizeCapsetIDs(agent.CapsetIDs),
		ManagedProjectID:       project.ID,
		ManagedProjectRevision: revision,
		ManagedAgentName:       agent.Name,
	}, nil
}

func sessionEnvItemsFromCompose(values map[string]compose.EnvVarSpec) []SessionEnvVar {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	items := make([]SessionEnvVar, 0, len(values))
	for _, name := range names {
		value := values[name]
		items = append(items, SessionEnvVar{Name: name, Value: value.Value, Secret: value.Secret})
	}
	return items
}

func projectManagedSchedulerRecords(builds []projectManagedSchedulerBuild) []ProjectSchedulerRecord {
	schedulers := make([]ProjectSchedulerRecord, 0, len(builds))
	for _, build := range builds {
		schedulers = append(schedulers, build.scheduler)
	}
	return schedulers
}

func projectManagedSchedulerLoaders(builds []projectManagedSchedulerBuild) []Loader {
	loaders := make([]Loader, 0, len(builds))
	for _, build := range builds {
		loaders = append(loaders, build.loader)
	}
	return loaders
}

func projectManagedSchedulerBuildsFromSpec(project ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]projectManagedSchedulerBuild, error) {
	builds := make([]projectManagedSchedulerBuild, 0)
	for _, agent := range spec.Agents {
		record, ok, err := NewProjectSchedulerRecordFromSpec(project.ID, revision, agent)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		loader, err := projectManagedLoaderFromScheduler(project, record, agent)
		if err != nil {
			return nil, err
		}
		builds = append(builds, projectManagedSchedulerBuild{
			scheduler:          record,
			loader:             loader,
			validationTriggers: loader.Triggers,
		})
	}
	return builds, nil
}

func projectManagedLoaderFromScheduler(project ProjectRecord, scheduler ProjectSchedulerRecord, agent compose.NormalizedAgentSpec) (Loader, error) {
	managedAgentID, err := StableManagedAgentID(project.ID, agent.Name)
	if err != nil {
		return Loader{}, err
	}
	driver := ""
	if agent.Driver != nil {
		driver = agent.Driver.Name
	}
	var triggers []LoaderTrigger
	script := agent.Scheduler.Script
	if strings.TrimSpace(script) == "" {
		var err error
		triggers, script, err = projectManagedLoaderTriggersAndScript(project.ID, agent.Name, "", agent.Scheduler)
		if err != nil {
			return Loader{}, err
		}
	}
	return Loader{
		Summary: LoaderSummary{
			ID:                 scheduler.ManagedLoaderID,
			Name:               fmt.Sprintf("%s/%s scheduler", project.Name, agent.Name),
			Enabled:            scheduler.Enabled,
			Runtime:            LoaderRuntimeScheduler,
			AgentID:            managedAgentID,
			Driver:             driver,
			GuestImage:         agent.Image,
			DefaultAgent:       agent.Provider,
			SessionPolicy:      LoaderSessionPolicyNew,
			ConcurrencyPolicy:  LoaderConcurrencyPolicySkip,
			CapsetIDs:          normalizeCapsetIDs(agent.CapsetIDs),
			ManagedProjectID:   project.ID,
			ManagedRevision:    scheduler.Revision,
			ManagedAgentName:   agent.Name,
			ManagedSchedulerID: scheduler.SchedulerID,
		},
		Script:   script,
		Triggers: triggers,
		EnvItems: sessionEnvItemsFromCompose(agent.Env),
	}, nil
}

func projectManagedLoaderTriggersAndScript(projectID, agentName, schedulerName string, scheduler *compose.NormalizedSchedulerSpec) ([]LoaderTrigger, string, error) {
	if scheduler == nil {
		return nil, "", fmt.Errorf("scheduler is required")
	}
	triggers := make([]LoaderTrigger, 0, len(scheduler.Triggers))
	seenNames := make(map[string]struct{}, len(scheduler.Triggers))
	var script strings.Builder
	script.WriteString("// Generated by agent-compose project scheduler reconcile.\n")
	for i, trigger := range scheduler.Triggers {
		name := strings.TrimSpace(trigger.Name)
		if name != "" {
			if _, ok := seenNames[name]; ok {
				return nil, "", fmt.Errorf("duplicate scheduler trigger name %q", name)
			}
			seenNames[name] = struct{}{}
		}
		id, err := StableManagedTriggerID(projectID, agentName, schedulerName, name, i)
		if err != nil {
			return nil, "", err
		}
		loaderTrigger, registration, err := projectManagedLoaderTriggerAndRegistration(id, agentName, trigger)
		if err != nil {
			return nil, "", err
		}
		triggers = append(triggers, loaderTrigger)
		script.WriteString(registration)
	}
	if len(triggers) == 0 {
		script.WriteString("function main() { return { status: \"idle\" }; }\n")
	}
	return triggers, script.String(), nil
}

func projectManagedLoaderTriggerAndRegistration(id, agentName string, trigger compose.NormalizedTriggerSpec) (LoaderTrigger, string, error) {
	prompt := strings.TrimSpace(trigger.Prompt)
	if prompt == "" {
		prompt = fmt.Sprintf("Run agent %s.", agentName)
	}
	callback := fmt.Sprintf("async function(event) { return scheduler.agent(%s); }", jsStringLiteral(prompt))
	switch trigger.Kind {
	case "cron":
		specJSON, err := loaderCronSpecJSON(trigger.Cron, "")
		if err != nil {
			return LoaderTrigger{}, "", err
		}
		return LoaderTrigger{
			ID:       id,
			Kind:     LoaderTriggerKindCron,
			Enabled:  true,
			SpecJSON: specJSON,
		}, fmt.Sprintf("scheduler.cron(%s, %s, %s);\n", jsStringLiteral(id), jsStringLiteral(trigger.Cron), callback), nil
	case "interval":
		interval, err := time.ParseDuration(trigger.Interval)
		if err != nil {
			return LoaderTrigger{}, "", err
		}
		intervalMs := interval.Milliseconds()
		if intervalMs <= 0 {
			return LoaderTrigger{}, "", fmt.Errorf("interval trigger %s must be at least 1ms", id)
		}
		specJSON, err := marshalJSONCompact(map[string]any{"kind": LoaderTriggerKindInterval, "interval": trigger.Interval})
		if err != nil {
			return LoaderTrigger{}, "", err
		}
		return LoaderTrigger{
			ID:         id,
			Kind:       LoaderTriggerKindInterval,
			IntervalMs: intervalMs,
			Enabled:    true,
			SpecJSON:   specJSON,
		}, fmt.Sprintf("scheduler.interval(%s, %s, %d);\n", jsStringLiteral(id), callback, intervalMs), nil
	case "timeout":
		delay, err := time.ParseDuration(trigger.Timeout)
		if err != nil {
			return LoaderTrigger{}, "", err
		}
		delayMs := delay.Milliseconds()
		if delayMs <= 0 {
			return LoaderTrigger{}, "", fmt.Errorf("timeout trigger %s must be at least 1ms", id)
		}
		specJSON, err := marshalJSONCompact(map[string]any{"kind": LoaderTriggerKindTimeout, "timeout": trigger.Timeout})
		if err != nil {
			return LoaderTrigger{}, "", err
		}
		return LoaderTrigger{
			ID:         id,
			Kind:       LoaderTriggerKindTimeout,
			IntervalMs: delayMs,
			Enabled:    true,
			SpecJSON:   specJSON,
		}, fmt.Sprintf("scheduler.timeout(%s, %s, %d);\n", jsStringLiteral(id), callback, delayMs), nil
	case "event":
		if trigger.Event == nil {
			return LoaderTrigger{}, "", fmt.Errorf("event trigger topic is required")
		}
		topic := strings.TrimSpace(trigger.Event.Topic)
		specJSON, err := marshalJSONCompact(map[string]any{"kind": LoaderTriggerKindEvent, "topic": topic})
		if err != nil {
			return LoaderTrigger{}, "", err
		}
		return LoaderTrigger{
			ID:       id,
			Kind:     LoaderTriggerKindEvent,
			Topic:    topic,
			Enabled:  true,
			SpecJSON: specJSON,
		}, fmt.Sprintf("scheduler.on(%s, %s, %s);\n", jsStringLiteral(topic), jsStringLiteral(id), callback), nil
	default:
		return LoaderTrigger{}, "", fmt.Errorf("unsupported scheduler trigger kind %q", trigger.Kind)
	}
}

func jsStringLiteral(value string) string {
	data, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(data)
}
