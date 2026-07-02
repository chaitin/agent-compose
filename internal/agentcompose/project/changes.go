package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	agentdomain "agent-compose/internal/agentcompose/agent"
	loaderdomain "agent-compose/internal/agentcompose/loader"
	sessiondomain "agent-compose/internal/agentcompose/session"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type ManagedAgentDefinitionStore interface {
	GetAgentDefinitionIfExists(ctx context.Context, id string, includeDeleted bool) (agentdomain.Definition, bool, error)
	UpsertManagedAgentDefinition(ctx context.Context, item agentdomain.Definition) (agentdomain.Definition, error)
	ListManagedAgentDefinitions(ctx context.Context, projectID string, includeDeleted bool) ([]agentdomain.Definition, error)
	SetAgentDefinitionEnabled(ctx context.Context, id string, enabled bool) (agentdomain.Definition, error)
}

func ReconcileManagedAgentDefinitions(ctx context.Context, store ManagedAgentDefinitionStore, project ProjectRecord, current []agentdomain.Definition) ([]*agentcomposev2.ProjectChange, bool, error) {
	if store == nil {
		return nil, false, fmt.Errorf("config store is required")
	}
	currentByID := make(map[string]agentdomain.Definition, len(current))
	for _, agent := range current {
		currentByID[agent.ID] = agent
	}
	changes := make([]*agentcomposev2.ProjectChange, 0, len(current))
	unchanged := true
	for _, agent := range current {
		existing, found, err := store.GetAgentDefinitionIfExists(ctx, agent.ID, true)
		if err != nil {
			return nil, false, fmt.Errorf("load managed agent definition %s: %w", agent.ID, err)
		}
		saved, err := store.UpsertManagedAgentDefinition(ctx, agent)
		if err != nil {
			return nil, false, fmt.Errorf("upsert managed agent definition %s: %w", agent.ID, err)
		}
		action := ManagedAgentDefinitionChangeAction(existing, found, agent)
		if action != agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED {
			unchanged = false
		}
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       action,
			ResourceType: "agent_definition",
			ResourceId:   saved.ID,
			Name:         saved.Name,
		})
	}

	existingManaged, err := store.ListManagedAgentDefinitions(ctx, project.ID, false)
	if err != nil {
		return nil, false, fmt.Errorf("list managed agent definitions: %w", err)
	}
	for _, existing := range existingManaged {
		if _, ok := currentByID[existing.ID]; ok {
			continue
		}
		if !existing.Enabled {
			continue
		}
		disabled, err := store.SetAgentDefinitionEnabled(ctx, existing.ID, false)
		if err != nil {
			return nil, false, fmt.Errorf("disable removed managed agent definition %s: %w", existing.ID, err)
		}
		unchanged = false
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED,
			ResourceType: "agent_definition",
			ResourceId:   disabled.ID,
			Name:         disabled.Name,
			Message:      "disabled because the agent is no longer present in the project spec",
		})
	}
	return changes, unchanged, nil
}

func GetProjectAgentIfExists(ctx context.Context, store interface {
	GetProjectAgent(context.Context, string, string) (ProjectAgentRecord, error)
}, projectID, agentName string) (ProjectAgentRecord, bool, error) {
	agent, err := store.GetProjectAgent(ctx, projectID, agentName)
	if err == nil {
		return agent, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectAgentRecord{}, false, nil
	}
	return ProjectAgentRecord{}, false, err
}

func GetProjectSchedulerIfExists(ctx context.Context, store interface {
	GetProjectScheduler(context.Context, string, string) (ProjectSchedulerRecord, error)
}, projectID, schedulerID string) (ProjectSchedulerRecord, bool, error) {
	scheduler, err := store.GetProjectScheduler(ctx, projectID, schedulerID)
	if err == nil {
		return scheduler, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectSchedulerRecord{}, false, nil
	}
	return ProjectSchedulerRecord{}, false, err
}

func ProjectApplyChanges(project ProjectRecord, existing ProjectRecord, found bool, revision ProjectRevisionRecord, revisionCreated bool) []*agentcomposev2.ProjectChange {
	projectAction := agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED
	if found {
		projectAction = agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED
		if !ProjectRecordUnchanged(existing, project) {
			projectAction = agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED
		}
	}
	revisionAction := agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED
	if revisionCreated {
		revisionAction = agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED
	}
	return []*agentcomposev2.ProjectChange{
		{
			Action:       projectAction,
			ResourceType: "project",
			ResourceId:   project.ID,
			Name:         project.Name,
		},
		{
			Action:       revisionAction,
			ResourceType: "project_revision",
			ResourceId:   fmt.Sprintf("%s/%d", revision.ProjectID, revision.Revision),
			Name:         revision.SpecHash,
		},
	}
}

func DryRunProjectChanges(project ProjectRecord, agents []ProjectAgentRecord, agentDefinitions []agentdomain.Definition, schedulers []ProjectSchedulerRecord, loaders []loaderdomain.Definition) []*agentcomposev2.ProjectChange {
	changes := []*agentcomposev2.ProjectChange{{
		Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED,
		ResourceType: "project",
		ResourceId:   project.ID,
		Name:         project.Name,
	}}
	for _, agent := range agents {
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED,
			ResourceType: "project_agent",
			ResourceId:   agent.ManagedAgentID,
			Name:         agent.AgentName,
		})
	}
	for _, agent := range agentDefinitions {
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED,
			ResourceType: "agent_definition",
			ResourceId:   agent.ID,
			Name:         agent.Name,
		})
	}
	for _, scheduler := range schedulers {
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED,
			ResourceType: "project_scheduler",
			ResourceId:   scheduler.SchedulerID,
			Name:         scheduler.AgentName,
		})
	}
	for _, loader := range loaders {
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED,
			ResourceType: "loader",
			ResourceId:   loader.Summary.ID,
			Name:         loader.Summary.Name,
		})
	}
	return changes
}

func ProjectRecordUnchanged(existing ProjectRecord, current ProjectRecord) bool {
	return existing.ID == current.ID &&
		existing.Name == current.Name &&
		existing.SourcePath == current.SourcePath &&
		existing.SpecHash == current.SpecHash &&
		existing.CurrentRevision == current.CurrentRevision &&
		existing.RemovedAt.IsZero()
}

func AgentChangeAction(existing ProjectAgentRecord, found bool, current ProjectAgentRecord) agentcomposev2.ProjectChangeAction {
	if !found {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED
	}
	if existing.ManagedAgentID == current.ManagedAgentID &&
		existing.Revision == current.Revision &&
		existing.Provider == current.Provider &&
		existing.Model == current.Model &&
		existing.Image == current.Image &&
		existing.Driver == current.Driver &&
		existing.SchedulerEnabled == current.SchedulerEnabled &&
		existing.SpecJSON == current.SpecJSON {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED
	}
	return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED
}

func ManagedAgentDefinitionChangeAction(existing agentdomain.Definition, found bool, current agentdomain.Definition) agentcomposev2.ProjectChangeAction {
	if !found {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED
	}
	if !existing.DeletedAt.IsZero() || !existing.Enabled {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED
	}
	if existing.Name == current.Name &&
		existing.Description == current.Description &&
		existing.Provider == current.Provider &&
		existing.Model == current.Model &&
		existing.SystemPrompt == current.SystemPrompt &&
		existing.Driver == current.Driver &&
		existing.GuestImage == current.GuestImage &&
		existing.WorkspaceID == current.WorkspaceID &&
		existing.ConfigJSON == current.ConfigJSON &&
		SameSessionEnvItems(existing.EnvItems, current.EnvItems) &&
		SameStringSlices(existing.CapsetIDs, current.CapsetIDs) &&
		existing.ManagedProjectID == current.ManagedProjectID &&
		existing.ManagedProjectRevision == current.ManagedProjectRevision &&
		existing.ManagedAgentName == current.ManagedAgentName {
		return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED
	}
	return agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED
}

func SameSessionEnvItems(a, b []sessiondomain.EnvVar) bool {
	a = agentdomain.NormalizeEnvItems(a)
	b = agentdomain.NormalizeEnvItems(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func ProjectSessionHasTag(tags []sessiondomain.Tag, name, value string) bool {
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	for _, tag := range tags {
		if strings.TrimSpace(tag.Name) == name && strings.TrimSpace(tag.Value) == value {
			return true
		}
	}
	return false
}
