package agent

import (
	capabilitydomain "agent-compose/internal/agentcompose/capability"
	configdomain "agent-compose/internal/agentcompose/config"
	sessiondomain "agent-compose/internal/agentcompose/session"
	agentworkspace "agent-compose/internal/agentcompose/workspace"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

const (
	DefaultProvider = "codex"

	SessionTagSource      = "source"
	SessionTagSourceValue = "agent"
	SessionTagID          = "agent_id"
	SessionTagName        = "agent_name"
)

type Definition struct {
	ID                     string                 `json:"id"`
	Name                   string                 `json:"name"`
	Description            string                 `json:"description,omitempty"`
	Enabled                bool                   `json:"enabled"`
	DeletedAt              time.Time              `json:"deleted_at,omitempty"`
	Provider               string                 `json:"provider"`
	Model                  string                 `json:"model,omitempty"`
	SystemPrompt           string                 `json:"system_prompt,omitempty"`
	Driver                 string                 `json:"driver,omitempty"`
	GuestImage             string                 `json:"guest_image,omitempty"`
	WorkspaceID            string                 `json:"workspace_id,omitempty"`
	EnvItems               []sessiondomain.EnvVar `json:"env_items,omitempty"`
	ConfigJSON             string                 `json:"config_json"`
	CapsetIDs              []string               `json:"capset_ids,omitempty"`
	ManagedProjectID       string                 `json:"managed_project_id,omitempty"`
	ManagedProjectRevision int64                  `json:"managed_project_revision,omitempty"`
	ManagedAgentName       string                 `json:"managed_agent_name,omitempty"`
	CreatedAt              time.Time              `json:"created_at"`
	UpdatedAt              time.Time              `json:"updated_at"`
}

type ListOptions struct {
	Query           string
	IncludeDisabled bool
	Offset          int
	Limit           int
}

type ListResult struct {
	Agents     []Definition
	TotalCount int
	HasMore    bool
	NextOffset int
}

type ValidationResult struct {
	Availability agentcomposev1.AgentAvailabilityStatus
	Health       agentcomposev1.AgentHealthStatus
	Warnings     []string
	Errors       []string
}

type CurrentRunSummary struct {
	RunningSessionCount int
}

type LatestRunSummary struct {
	RunType string
	Status  string
	RunID   string
	Title   string
	At      time.Time
}

func NormalizeDefinition(item Definition, assignDefaults bool) (Definition, error) {
	item.ID = strings.TrimSpace(item.ID)
	item.Name = strings.TrimSpace(item.Name)
	item.Description = strings.TrimSpace(item.Description)
	item.Provider = NormalizeKind(item.Provider)
	if item.Provider == "" && assignDefaults {
		item.Provider = DefaultProvider
	}
	item.Model = strings.TrimSpace(item.Model)
	item.SystemPrompt = strings.TrimSpace(item.SystemPrompt)
	item.Driver = strings.TrimSpace(item.Driver)
	item.GuestImage = strings.TrimSpace(item.GuestImage)
	item.WorkspaceID = strings.TrimSpace(item.WorkspaceID)
	item.CapsetIDs = capabilitydomain.NormalizeCapsetIDs(item.CapsetIDs)
	item.ManagedProjectID = strings.TrimSpace(item.ManagedProjectID)
	item.ManagedAgentName = strings.TrimSpace(item.ManagedAgentName)
	item.ConfigJSON = strings.TrimSpace(item.ConfigJSON)
	if item.ConfigJSON == "" {
		item.ConfigJSON = "{}"
	}
	if item.ID == "" {
		return Definition{}, fmt.Errorf("agent definition id is required")
	}
	if item.Name == "" {
		return Definition{}, fmt.Errorf("agent definition name is required")
	}
	if item.Provider == "" {
		return Definition{}, fmt.Errorf("agent definition provider is required")
	}
	if item.Provider != "codex" && item.Provider != "claude" && item.Provider != "gemini" && item.Provider != "opencode" {
		return Definition{}, fmt.Errorf("agent definition provider %q is not supported", item.Provider)
	}
	if !IsJSONObject(item.ConfigJSON) {
		return Definition{}, fmt.Errorf("agent definition config_json must be a JSON object")
	}
	if item.ManagedProjectID == "" {
		item.ManagedProjectRevision = 0
		item.ManagedAgentName = ""
	} else {
		if item.ManagedAgentName == "" {
			return Definition{}, fmt.Errorf("managed agent name is required")
		}
		if item.ManagedProjectRevision < 0 {
			return Definition{}, fmt.Errorf("managed project revision cannot be negative")
		}
	}
	item.EnvItems = NormalizeEnvItems(item.EnvItems)
	return item, nil
}

func NormalizeKind(agent string) string {
	agent = strings.ToLower(strings.TrimSpace(agent))
	switch agent {
	case "":
		return ""
	case "codex":
		return "codex"
	case "claude", "claude-code", "claude_code":
		return "claude"
	case "gemini", "gemini-cli", "gemini_cli":
		return "gemini"
	case "opencode", "open-code", "open_code":
		return "opencode"
	default:
		return agent
	}
}

func NormalizeEnvItems(items []sessiondomain.EnvVar) []sessiondomain.EnvVar {
	normalized := configdomain.NormalizeEnvItems(sessionEnvVarsToConfig(items))
	return configEnvVarsToSession(normalized)
}

func IsJSONObject(raw string) bool {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &decoded); err != nil {
		return false
	}
	return decoded != nil
}

func DefinitionTags(agent Definition) []*agentcomposev1.SessionTag {
	return []*agentcomposev1.SessionTag{
		{Name: SessionTagSource, Value: SessionTagSourceValue},
		{Name: SessionTagID, Value: agent.ID},
		{Name: SessionTagName, Value: agent.Name},
	}
}

func SessionHasAgentTag(session *sessiondomain.Session, agentID string) bool {
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
		if name == SessionTagSource && value == SessionTagSourceValue {
			hasSource = true
		}
		if name == SessionTagID && value == agentID {
			hasAgentID = true
		}
	}
	return hasSource && hasAgentID
}

func ToProtoDefinition(item Definition, workspace *agentworkspace.Config, validation ValidationResult, current CurrentRunSummary, latest *LatestRunSummary) *agentcomposev1.AgentDefinition {
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
		WorkFiles:          ToProtoWorkFiles(item.WorkspaceID, workspace),
		EnvItems:           ToProtoEnvItems(item.EnvItems),
		ConfigJson:         item.ConfigJSON,
		CapsetIds:          item.CapsetIDs,
		AvailabilityStatus: validation.Availability,
		HealthStatus:       validation.Health,
		CurrentRunSummary:  ToProtoCurrentRunSummary(current),
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

func ToProtoEnvItems(items []sessiondomain.EnvVar) []*agentcomposev1.SessionEnvVar {
	resp := make([]*agentcomposev1.SessionEnvVar, 0, len(items))
	for _, item := range items {
		resp = append(resp, &agentcomposev1.SessionEnvVar{Name: item.Name, Value: item.Value, Secret: item.Secret})
	}
	return resp
}

func ToProtoWorkFiles(workspaceID string, workspace *agentworkspace.Config) *agentcomposev1.AgentWorkFiles {
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
		Summary:       WorkspaceSummary(*workspace),
		ConfigJson:    workspace.ConfigJSON,
	}
}

func WorkspaceSummary(workspace agentworkspace.Config) string {
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

func ToProtoCurrentRunSummary(item CurrentRunSummary) *agentcomposev1.AgentCurrentRunSummary {
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

func RunSummaries(agentID string, sessions []*sessiondomain.Session) (CurrentRunSummary, *LatestRunSummary) {
	current := CurrentRunSummary{}
	var latest *LatestRunSummary
	for _, session := range sessions {
		if !SessionHasAgentTag(session, agentID) {
			continue
		}
		switch session.Summary.VMStatus {
		case sessiondomain.VMStatusPending, sessiondomain.VMStatusRunning:
			current.RunningSessionCount++
		}
		if latest == nil || session.Summary.UpdatedAt.After(latest.At) {
			latest = &LatestRunSummary{
				RunType: "work_session",
				Status:  session.Summary.VMStatus,
				RunID:   session.Summary.ID,
				Title:   session.Summary.Title,
				At:      session.Summary.UpdatedAt,
			}
		}
	}
	return current, latest
}

func sessionEnvVarsToConfig(items []sessiondomain.EnvVar) []configdomain.EnvVar {
	result := make([]configdomain.EnvVar, 0, len(items))
	for _, item := range items {
		result = append(result, configdomain.EnvVar{Name: item.Name, Value: item.Value, Secret: item.Secret})
	}
	return result
}

func configEnvVarsToSession(items []configdomain.EnvVar) []sessiondomain.EnvVar {
	result := make([]sessiondomain.EnvVar, 0, len(items))
	for _, item := range items {
		result = append(result, sessiondomain.EnvVar{Name: item.Name, Value: item.Value, Secret: item.Secret})
	}
	return result
}
