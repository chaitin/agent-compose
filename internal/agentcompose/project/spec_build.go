package project

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	agentdomain "agent-compose/internal/agentcompose/agent"
	capabilitydomain "agent-compose/internal/agentcompose/capability"
	loaderdomain "agent-compose/internal/agentcompose/loader"
	sessiondomain "agent-compose/internal/agentcompose/session"
	driverpkg "agent-compose/internal/driver"
	"agent-compose/pkg/compose"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type ManagedSchedulerBuild struct {
	Scheduler          ProjectSchedulerRecord
	Loader             loaderdomain.Definition
	ValidationTriggers []loaderdomain.Trigger
}

type InlineSchedulerScriptValidator interface {
	ValidateInlineSchedulerScript(ctx context.Context, agentName, script string) ([]loaderdomain.Trigger, error)
}

type ManagedSchedulerBuildError struct {
	Path    string
	Message string
}

func (e *ManagedSchedulerBuildError) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return e.Path + ": " + e.Message
}

func AgentRecordsFromSpec(projectID string, revision int64, spec *compose.NormalizedProjectSpec) ([]ProjectAgentRecord, error) {
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

func ManagedAgentDefinitionsFromSpec(project ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec) ([]agentdomain.Definition, error) {
	agents := make([]agentdomain.Definition, 0, len(spec.Agents))
	for _, agent := range spec.Agents {
		record, err := ManagedAgentDefinitionFromSpec(project, revision, agent)
		if err != nil {
			return nil, err
		}
		agents = append(agents, record)
	}
	return agents, nil
}

func ManagedAgentDefinitionFromSpec(project ProjectRecord, revision int64, agent compose.NormalizedAgentSpec) (agentdomain.Definition, error) {
	managedAgentID, err := StableManagedAgentID(project.ID, agent.Name)
	if err != nil {
		return agentdomain.Definition{}, err
	}
	driver := ""
	if agent.Driver != nil {
		driver = agent.Driver.Name
	}
	return agentdomain.Definition{
		ID:                     managedAgentID,
		Name:                   agent.Name,
		Enabled:                true,
		Provider:               agent.Provider,
		Model:                  agent.Model,
		SystemPrompt:           agent.SystemPrompt,
		Driver:                 driver,
		GuestImage:             agent.Image,
		EnvItems:               SessionEnvItemsFromCompose(agent.Env),
		ConfigJSON:             "{}",
		CapsetIDs:              capabilitydomain.NormalizeCapsetIDs(agent.CapsetIDs),
		ManagedProjectID:       project.ID,
		ManagedProjectRevision: revision,
		ManagedAgentName:       agent.Name,
	}, nil
}

func SessionEnvItemsFromCompose(values map[string]compose.EnvVarSpec) []sessiondomain.EnvVar {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	items := make([]sessiondomain.EnvVar, 0, len(values))
	for _, name := range names {
		value := values[name]
		items = append(items, sessiondomain.EnvVar{Name: name, Value: value.Value, Secret: value.Secret})
	}
	return items
}

func ManagedSchedulerRecords(builds []ManagedSchedulerBuild) []ProjectSchedulerRecord {
	schedulers := make([]ProjectSchedulerRecord, 0, len(builds))
	for _, build := range builds {
		schedulers = append(schedulers, build.Scheduler)
	}
	return schedulers
}

func ManagedSchedulerLoaders(builds []ManagedSchedulerBuild) []loaderdomain.Definition {
	loaders := make([]loaderdomain.Definition, 0, len(builds))
	for _, build := range builds {
		loaders = append(loaders, build.Loader)
	}
	return loaders
}

func ManagedSchedulerBuildsFromSpec(ctx context.Context, project ProjectRecord, revision int64, spec *compose.NormalizedProjectSpec, validator InlineSchedulerScriptValidator) ([]ManagedSchedulerBuild, error) {
	builds := make([]ManagedSchedulerBuild, 0)
	for _, agent := range spec.Agents {
		record, ok, err := NewProjectSchedulerRecordFromSpec(project.ID, revision, agent)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		loader, err := ManagedLoaderFromScheduler(project, record, agent)
		if err != nil {
			return nil, err
		}
		validationTriggers := loader.Triggers
		if strings.TrimSpace(agent.Scheduler.Script) != "" {
			if validator == nil {
				return nil, &ManagedSchedulerBuildError{
					Path:    "agents." + agent.Name + ".scheduler.script",
					Message: "loader manager is required to validate scheduler script",
				}
			}
			validation, err := validator.ValidateInlineSchedulerScript(ctx, agent.Name, agent.Scheduler.Script)
			if err != nil {
				return nil, err
			}
			validationTriggers = validation
			loader.Triggers = validation
			record.TriggerCount = len(validation)
		}
		builds = append(builds, ManagedSchedulerBuild{
			Scheduler:          record,
			Loader:             loader,
			ValidationTriggers: validationTriggers,
		})
	}
	return builds, nil
}

func ManagedLoaderFromScheduler(project ProjectRecord, scheduler ProjectSchedulerRecord, agent compose.NormalizedAgentSpec) (loaderdomain.Definition, error) {
	managedAgentID, err := StableManagedAgentID(project.ID, agent.Name)
	if err != nil {
		return loaderdomain.Definition{}, err
	}
	driver := ""
	if agent.Driver != nil {
		driver = agent.Driver.Name
	}
	var triggers []loaderdomain.Trigger
	script := agent.Scheduler.Script
	if strings.TrimSpace(script) == "" {
		triggerBuilds, generatedScript, err := ManagedLoaderTriggersAndScript(project.ID, agent.Name, "", agent.Scheduler)
		if err != nil {
			return loaderdomain.Definition{}, err
		}
		triggers = make([]loaderdomain.Trigger, 0, len(triggerBuilds))
		for _, trigger := range triggerBuilds {
			triggers = append(triggers, LoaderTriggerFromSchedulerBuild(trigger))
		}
		script = generatedScript
	}
	return loaderdomain.Definition{
		Summary: loaderdomain.Summary{
			ID:                 scheduler.ManagedLoaderID,
			Name:               fmt.Sprintf("%s/%s scheduler", project.Name, agent.Name),
			Enabled:            scheduler.Enabled,
			Runtime:            loaderdomain.RuntimeScheduler,
			AgentID:            managedAgentID,
			Driver:             driver,
			GuestImage:         agent.Image,
			DefaultAgent:       agent.Provider,
			SessionPolicy:      loaderdomain.SessionPolicyNew,
			ConcurrencyPolicy:  loaderdomain.ConcurrencyPolicySkip,
			CapsetIDs:          capabilitydomain.NormalizeCapsetIDs(agent.CapsetIDs),
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

func LoaderTriggerFromSchedulerBuild(trigger SchedulerTriggerBuild) loaderdomain.Trigger {
	return loaderdomain.Trigger{
		ID:         trigger.ID,
		Kind:       trigger.Kind,
		Topic:      trigger.Topic,
		IntervalMs: trigger.IntervalMs,
		Enabled:    true,
		SpecJSON:   trigger.SpecJSON,
	}
}

func ValidateManagedAgentDefinitions(project ProjectRecord, spec *compose.NormalizedProjectSpec, defaultDriver string) []*agentcomposev2.ProjectValidationIssue {
	agents, err := ManagedAgentDefinitionsFromSpec(project, 0, spec)
	if err != nil {
		return []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue("agents", err.Error())}
	}
	if strings.TrimSpace(defaultDriver) == "" {
		defaultDriver = driverpkg.RuntimeDriverDocker
	}
	var issues []*agentcomposev2.ProjectValidationIssue
	for _, agent := range agents {
		path := "agents." + agent.ManagedAgentName
		if _, err := agentdomain.NormalizeDefinition(agent, true); err != nil {
			issues = append(issues, ProjectValidationIssue(path, err.Error()))
			continue
		}
		if strings.TrimSpace(agent.Driver) != "" {
			if _, err := driverpkg.ResolveSessionRuntimeDriver(agent.Driver, defaultDriver); err != nil {
				issues = append(issues, ProjectValidationIssue(path+".driver", err.Error()))
			}
		}
	}
	return issues
}

func ValidateManagedSchedulers(ctx context.Context, project ProjectRecord, spec *compose.NormalizedProjectSpec, validator InlineSchedulerScriptValidator) []*agentcomposev2.ProjectValidationIssue {
	builds, err := ManagedSchedulerBuildsFromSpec(ctx, project, 0, spec, validator)
	if err != nil {
		return []*agentcomposev2.ProjectValidationIssue{ManagedSchedulerBuildIssue(err)}
	}
	loaders := ManagedSchedulerLoaders(builds)
	for _, loader := range loaders {
		if _, err := loaderdomain.NormalizeLoader(loader, false); err != nil {
			return []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue("schedulers."+loader.Summary.ManagedAgentName, err.Error())}
		}
		for _, trigger := range loader.Triggers {
			if _, err := loaderdomain.NormalizeLoaderTrigger(loader.Summary.ID, trigger); err != nil {
				return []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue("schedulers."+loader.Summary.ManagedAgentName+".triggers", err.Error())}
			}
		}
	}
	return nil
}

func ManagedSchedulerBuildIssue(err error) *agentcomposev2.ProjectValidationIssue {
	var buildErr *ManagedSchedulerBuildError
	if errors.As(err, &buildErr) {
		return ProjectValidationIssue(buildErr.Path, buildErr.Message)
	}
	return ProjectValidationIssue("schedulers", err.Error())
}
