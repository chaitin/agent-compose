package agentcompose

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"agent-compose/pkg/model"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

const (
	defaultAgentProvider = model.DefaultAgentProvider

	agentSessionTagSource    = "source"
	agentSessionTagSourceVal = "agent"
	agentSessionTagID        = "agent_id"
	agentSessionTagName      = "agent_name"
)

type AgentDefinition = model.AgentDefinition
type AgentDefinitionListOptions = model.AgentDefinitionListOptions
type AgentDefinitionListResult = model.AgentDefinitionListResult

type AgentValidationResult struct {
	Availability agentcomposev1.AgentAvailabilityStatus
	Health       agentcomposev1.AgentHealthStatus
	Warnings     []string
	Errors       []string
}

func normalizeAgentDefinition(item AgentDefinition, assignDefaults bool) (AgentDefinition, error) {
	item.ID = strings.TrimSpace(item.ID)
	item.Name = strings.TrimSpace(item.Name)
	item.Description = strings.TrimSpace(item.Description)
	item.Provider = normalizeAgentKind(item.Provider)
	if item.Provider == "" && assignDefaults {
		item.Provider = defaultAgentProvider
	}
	item.Model = strings.TrimSpace(item.Model)
	item.SystemPrompt = strings.TrimSpace(item.SystemPrompt)
	item.Driver = strings.TrimSpace(item.Driver)
	item.GuestImage = strings.TrimSpace(item.GuestImage)
	item.WorkspaceID = strings.TrimSpace(item.WorkspaceID)
	item.CapsetIDs = normalizeCapsetIDs(item.CapsetIDs)
	item.ManagedProjectID = strings.TrimSpace(item.ManagedProjectID)
	item.ManagedAgentName = strings.TrimSpace(item.ManagedAgentName)
	item.ConfigJSON = strings.TrimSpace(item.ConfigJSON)
	if item.ConfigJSON == "" {
		item.ConfigJSON = "{}"
	}
	if item.ID == "" {
		return AgentDefinition{}, fmt.Errorf("agent definition id is required")
	}
	if item.Name == "" {
		return AgentDefinition{}, fmt.Errorf("agent definition name is required")
	}
	if item.Provider == "" {
		return AgentDefinition{}, fmt.Errorf("agent definition provider is required")
	}
	if item.Provider != "codex" && item.Provider != "claude" && item.Provider != "gemini" && item.Provider != "opencode" {
		return AgentDefinition{}, fmt.Errorf("agent definition provider %q is not supported", item.Provider)
	}
	if !isJSONObject(item.ConfigJSON) {
		return AgentDefinition{}, fmt.Errorf("agent definition config_json must be a JSON object")
	}
	if item.ManagedProjectID == "" {
		item.ManagedProjectRevision = 0
		item.ManagedAgentName = ""
	} else {
		if item.ManagedAgentName == "" {
			return AgentDefinition{}, fmt.Errorf("managed agent name is required")
		}
		if item.ManagedProjectRevision < 0 {
			return AgentDefinition{}, fmt.Errorf("managed project revision cannot be negative")
		}
	}
	item.EnvItems = normalizeEnvItems(item.EnvItems)
	return item, nil
}

func isJSONObject(raw string) bool {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &decoded); err != nil {
		return false
	}
	return decoded != nil
}

func agentDefinitionTags(agent AgentDefinition) []*agentcomposev1.SessionTag {
	return []*agentcomposev1.SessionTag{
		{Name: agentSessionTagSource, Value: agentSessionTagSourceVal},
		{Name: agentSessionTagID, Value: agent.ID},
		{Name: agentSessionTagName, Value: agent.Name},
	}
}

func sessionHasAgentTag(session *Session, agentID string) bool {
	if session == nil {
		return false
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return false
	}
	hasSource := false
	hasAgentID := false
	for _, tag := range session.Summary.Tags {
		name := strings.TrimSpace(tag.Name)
		value := strings.TrimSpace(tag.Value)
		if name == agentSessionTagSource && value == agentSessionTagSourceVal {
			hasSource = true
		}
		if name == agentSessionTagID && value == agentID {
			hasAgentID = true
		}
	}
	return hasSource && hasAgentID
}

func toProtoAgentDefinition(item AgentDefinition, workspace *WorkspaceConfig, validation AgentValidationResult, current AgentCurrentRunSummary, latest *AgentLatestRunSummary) *agentcomposev1.AgentDefinition {
	resp := &agentcomposev1.AgentDefinition{
		AgentId:            item.ID,
		Name:               item.Name,
		Description:        item.Description,
		Enabled:            item.Enabled,
		Provider:           item.Provider,
		Model:              item.Model,
		SystemPrompt:       item.SystemPrompt,
		RuntimeImageId:     "",
		Driver:             item.Driver,
		GuestImage:         item.GuestImage,
		WorkFiles:          toProtoAgentWorkFiles(item.WorkspaceID, workspace),
		EnvItems:           toProtoEnvItems(item.EnvItems),
		ConfigJson:         item.ConfigJSON,
		CapsetIds:          item.CapsetIDs,
		AvailabilityStatus: validation.Availability,
		HealthStatus:       validation.Health,
		CurrentRunSummary:  toProtoAgentCurrentRunSummary(current),
		CreatedAt:          formatProtoTime(item.CreatedAt),
		UpdatedAt:          formatProtoTime(item.UpdatedAt),
		DeletedAt:          formatProtoTime(item.DeletedAt),
	}
	if latest != nil {
		resp.LatestRunSummary = &agentcomposev1.AgentLatestRunSummary{
			RunType: latest.RunType,
			Status:  latest.Status,
			RunId:   latest.RunID,
			Title:   latest.Title,
			At:      formatProtoTime(latest.At),
		}
	}
	return resp
}

func toProtoEnvItems(items []SessionEnvVar) []*agentcomposev1.SessionEnvVar {
	resp := make([]*agentcomposev1.SessionEnvVar, 0, len(items))
	for _, item := range items {
		resp = append(resp, &agentcomposev1.SessionEnvVar{Name: item.Name, Value: item.Value, Secret: item.Secret})
	}
	return resp
}

func toProtoAgentWorkFiles(workspaceID string, workspace *WorkspaceConfig) *agentcomposev1.AgentWorkFiles {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" || workspace == nil {
		return &agentcomposev1.AgentWorkFiles{
			Source:        agentcomposev1.AgentWorkFilesSource_AGENT_WORK_FILES_SOURCE_EMPTY,
			WorkspaceType: "empty",
		}
	}
	source := agentcomposev1.AgentWorkFilesSource_AGENT_WORK_FILES_SOURCE_UNSPECIFIED
	switch strings.ToLower(strings.TrimSpace(workspace.Type)) {
	case "file":
		source = agentcomposev1.AgentWorkFilesSource_AGENT_WORK_FILES_SOURCE_FILE_WORKSPACE
	case "git":
		source = agentcomposev1.AgentWorkFilesSource_AGENT_WORK_FILES_SOURCE_GIT_WORKSPACE
	}
	return &agentcomposev1.AgentWorkFiles{
		Source:        source,
		WorkspaceId:   workspace.ID,
		WorkspaceName: workspace.Name,
		WorkspaceType: workspace.Type,
		Summary:       agentWorkspaceSummary(*workspace),
		ConfigJson:    workspace.ConfigJSON,
	}
}

func agentWorkspaceSummary(workspace WorkspaceConfig) string {
	switch strings.ToLower(strings.TrimSpace(workspace.Type)) {
	case "git":
		var config map[string]any
		if err := json.Unmarshal([]byte(workspace.ConfigJSON), &config); err == nil {
			repo := strings.TrimSpace(fmt.Sprint(config["repo_url"]))
			if repo == "" {
				repo = strings.TrimSpace(fmt.Sprint(config["repoUrl"]))
			}
			branch := strings.TrimSpace(fmt.Sprint(config["branch"]))
			if repo != "" && branch != "" {
				return repo + "#" + branch
			}
			if repo != "" {
				return repo
			}
		}
	case "file":
		if strings.TrimSpace(workspace.Comment) != "" {
			return strings.TrimSpace(workspace.Comment)
		}
	}
	return workspace.Name
}

func toProtoAgentCurrentRunSummary(item AgentCurrentRunSummary) *agentcomposev1.AgentCurrentRunSummary {
	status := agentcomposev1.AgentCurrentRunStatus_AGENT_CURRENT_RUN_STATUS_IDLE
	text := "空闲"
	if item.RunningSessionCount > 0 {
		status = agentcomposev1.AgentCurrentRunStatus_AGENT_CURRENT_RUN_STATUS_HAS_RUNNING_SESSION
		text = "有运行中会话"
	}
	return &agentcomposev1.AgentCurrentRunSummary{
		Status:                status,
		Text:                  text,
		RunningSessionCount:   uint32(item.RunningSessionCount),
		RunningLoaderRunCount: 0,
	}
}

func formatProtoTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

type AgentCurrentRunSummary = model.AgentCurrentRunSummary
type AgentLatestRunSummary = model.AgentLatestRunSummary
