package app

import (
	"strings"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

const (
	agentSessionTagSource    = "source"
	agentSessionTagSourceVal = "agent"
	agentSessionTagID        = "agent_id"
	agentSessionTagName      = "agent_name"
)

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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

func agentDefinitionTags(agent AgentDefinition) []*agentcomposev1.SessionTag {
	return []*agentcomposev1.SessionTag{
		{Name: agentSessionTagSource, Value: agentSessionTagSourceVal},
		{Name: agentSessionTagID, Value: agent.ID},
		{Name: agentSessionTagName, Value: agent.Name},
	}
}

func filterPersistedRuntimeEnv(items []SessionEnvVar) []SessionEnvVar {
	result := make([]SessionEnvVar, 0, len(items))
	for _, item := range items {
		name := strings.ToUpper(strings.TrimSpace(item.Name))
		switch name {
		case "LLM_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN":
			continue
		}
		result = append(result, item)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func normalizeEnvItems(items []SessionEnvVar) []SessionEnvVar {
	result := make([]SessionEnvVar, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		key := strings.ToUpper(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		item.Name = name
		result = append(result, item)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func normalizeProjectRunStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case ProjectRunStatusPending:
		return ProjectRunStatusPending
	case ProjectRunStatusRunning:
		return ProjectRunStatusRunning
	case ProjectRunStatusSucceeded:
		return ProjectRunStatusSucceeded
	case ProjectRunStatusFailed:
		return ProjectRunStatusFailed
	case ProjectRunStatusCanceled:
		return ProjectRunStatusCanceled
	default:
		return ProjectRunStatusPending
	}
}

func normalizeProjectRunSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case ProjectRunSourceScheduler:
		return ProjectRunSourceScheduler
	case ProjectRunSourceAPI:
		return ProjectRunSourceAPI
	case ProjectRunSourceManual:
		return ProjectRunSourceManual
	default:
		return ProjectRunSourceManual
	}
}
