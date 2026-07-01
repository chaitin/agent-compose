package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"agent-compose/pkg/agentcompose/domain"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func AgentDefinitionToProto(item domain.AgentDefinition, workspace *domain.WorkspaceConfig, availability agentcomposev1.AgentAvailabilityStatus, health agentcomposev1.AgentHealthStatus, current domain.AgentCurrentRunSummary, latest *domain.AgentLatestRunSummary) *agentcomposev1.AgentDefinition {
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
		WorkFiles:          AgentWorkFilesToProto(item.WorkspaceID, workspace),
		EnvItems:           EnvItemsToProto(item.EnvItems),
		ConfigJson:         item.ConfigJSON,
		CapsetIds:          item.CapsetIDs,
		AvailabilityStatus: availability,
		HealthStatus:       health,
		CurrentRunSummary:  AgentCurrentRunSummaryToProto(current),
		CreatedAt:          FormatProtoTime(item.CreatedAt),
		UpdatedAt:          FormatProtoTime(item.UpdatedAt),
		DeletedAt:          FormatProtoTime(item.DeletedAt),
	}
	if latest != nil {
		resp.LatestRunSummary = &agentcomposev1.AgentLatestRunSummary{
			RunType: latest.RunType,
			Status:  latest.Status,
			RunId:   latest.RunID,
			Title:   latest.Title,
			At:      FormatProtoTime(latest.At),
		}
	}
	return resp
}

func AgentDefinitionTagsToProto(agent domain.AgentDefinition) []*agentcomposev1.SessionTag {
	return []*agentcomposev1.SessionTag{
		{Name: domain.AgentSessionTagSource, Value: domain.AgentSessionTagSourceVal},
		{Name: domain.AgentSessionTagID, Value: agent.ID},
		{Name: domain.AgentSessionTagName, Value: agent.Name},
	}
}

func EnvItemsToProto(items []domain.SessionEnvVar) []*agentcomposev1.SessionEnvVar {
	resp := make([]*agentcomposev1.SessionEnvVar, 0, len(items))
	for _, item := range items {
		resp = append(resp, &agentcomposev1.SessionEnvVar{Name: item.Name, Value: item.Value, Secret: item.Secret})
	}
	return resp
}

func EnvItemsFromProto(items []*agentcomposev1.SessionEnvVar) []domain.SessionEnvVar {
	result := make([]domain.SessionEnvVar, 0, len(items))
	for _, item := range items {
		result = append(result, domain.SessionEnvVar{Name: item.GetName(), Value: item.GetValue(), Secret: item.GetSecret()})
	}
	return result
}

func SessionTagsFromProto(items []*agentcomposev1.SessionTag) []domain.SessionTag {
	if len(items) == 0 {
		return nil
	}
	result := make([]domain.SessionTag, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		result = append(result, domain.SessionTag{Name: item.GetName(), Value: item.GetValue()})
	}
	return result
}

func AgentWorkFilesToProto(workspaceID string, workspace *domain.WorkspaceConfig) *agentcomposev1.AgentWorkFiles {
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
		Summary:       AgentWorkspaceSummary(*workspace),
		ConfigJson:    workspace.ConfigJSON,
	}
}

func AgentWorkspaceSummary(workspace domain.WorkspaceConfig) string {
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

func AgentCurrentRunSummaryToProto(item domain.AgentCurrentRunSummary) *agentcomposev1.AgentCurrentRunSummary {
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

func FormatProtoTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
