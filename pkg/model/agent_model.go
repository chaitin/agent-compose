package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	DefaultAgentProvider = "codex"

	AgentSandboxTagSource    = "source"
	AgentSandboxTagSourceVal = "agent"
	AgentSandboxTagID        = "agent_id"
	AgentSandboxTagName      = "agent_name"
)

type AgentDefinition struct {
	ID                     string            `json:"id"`
	Name                   string            `json:"name"`
	Description            string            `json:"description,omitempty"`
	Enabled                bool              `json:"enabled"`
	DeletedAt              time.Time         `json:"deleted_at,omitempty"`
	Provider               string            `json:"provider"`
	Model                  string            `json:"model,omitempty"`
	SystemPrompt           string            `json:"system_prompt,omitempty"`
	Driver                 string            `json:"driver,omitempty"`
	GuestImage             string            `json:"guest_image,omitempty"`
	WorkspaceID            string            `json:"workspace_id,omitempty"`
	EnvItems               []SandboxEnvVar   `json:"env_items,omitempty"`
	Volumes                []VolumeMountSpec `json:"volumes,omitempty"`
	ConfigJSON             string            `json:"config_json"`
	CapsetIDs              []string          `json:"capset_ids,omitempty"`
	Skills                 []AgentSkill      `json:"skills,omitempty"`
	ManagedProjectID       string            `json:"managed_project_id,omitempty"`
	ManagedProjectRevision int64             `json:"managed_project_revision,omitempty"`
	ManagedAgentName       string            `json:"managed_agent_name,omitempty"`
	CreatedAt              time.Time         `json:"created_at"`
	UpdatedAt              time.Time         `json:"updated_at"`
}

type AgentSkill struct {
	Name     string `json:"name,omitempty"`
	Source   string `json:"source,omitempty"`
	URL      string `json:"url,omitempty"`
	Path     string `json:"path,omitempty"`
	Ref      string `json:"ref,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Token    string `json:"token,omitempty"`
	// SourceRoot is an internal host-side boundary for local file and zip
	// sources. It is not part of the compose schema.
	SourceRoot string `json:"source_root,omitempty"`
}

type AgentDefinitionListOptions struct {
	Query           string
	IncludeDisabled bool
	Offset          int
	Limit           int
}

type AgentDefinitionListResult struct {
	Agents     []AgentDefinition
	TotalCount int
	HasMore    bool
	NextOffset int
}

type AgentCurrentRunSummary struct {
	RunningSessionCount int
}

type AgentLatestRunSummary struct {
	RunType string
	Status  string
	RunID   string
	Title   string
	At      time.Time
}

func NormalizeAgentKind(agent string) string {
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

func NormalizeAgentDefinition(item AgentDefinition, assignDefaults bool) (AgentDefinition, error) {
	item.ID = strings.TrimSpace(item.ID)
	item.Name = strings.TrimSpace(item.Name)
	item.Description = strings.TrimSpace(item.Description)
	item.Provider = NormalizeAgentKind(item.Provider)
	if item.Provider == "" && assignDefaults {
		item.Provider = DefaultAgentProvider
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
	item.Skills = NormalizeAgentSkills(item.Skills)
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
	item.EnvItems = NormalizeEnvItems(item.EnvItems)
	volumes, err := NormalizeVolumeMountSpecs(item.Volumes)
	if err != nil {
		return AgentDefinition{}, fmt.Errorf("agent definition volumes: %w", err)
	}
	item.Volumes = volumes
	return item, nil
}

func NormalizeAgentSkills(skills []AgentSkill) []AgentSkill {
	if len(skills) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(skills))
	out := make([]AgentSkill, 0, len(skills))
	for _, skill := range skills {
		skill.Name = strings.TrimSpace(skill.Name)
		skill.Source = strings.ToLower(strings.TrimSpace(skill.Source))
		skill.URL = strings.TrimSpace(skill.URL)
		skill.Path = strings.TrimSpace(skill.Path)
		skill.Ref = strings.TrimSpace(skill.Ref)
		skill.Username = strings.TrimSpace(skill.Username)
		skill.Password = strings.TrimSpace(skill.Password)
		skill.Token = strings.TrimSpace(skill.Token)
		skill.SourceRoot = strings.TrimSpace(skill.SourceRoot)
		if skill.Name == "" {
			continue
		}
		if _, ok := seen[skill.Name]; ok {
			continue
		}
		seen[skill.Name] = struct{}{}
		out = append(out, skill)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeCapsetIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func isJSONObject(raw string) bool {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &decoded); err != nil {
		return false
	}
	return decoded != nil
}

func SandboxHasAgentTag(session *Sandbox, agentID string) bool {
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
		if name == AgentSandboxTagSource && value == AgentSandboxTagSourceVal {
			hasSource = true
		}
		if name == AgentSandboxTagID && value == agentID {
			hasAgentID = true
		}
	}
	return hasSource && hasAgentID
}

func AgentRunSummaries(agentID string, sessions []*Sandbox) (AgentCurrentRunSummary, *AgentLatestRunSummary) {
	current := AgentCurrentRunSummary{}
	var latest *AgentLatestRunSummary
	for _, session := range sessions {
		if !SandboxHasAgentTag(session, agentID) {
			continue
		}
		switch session.Summary.VMStatus {
		case VMStatusPending, VMStatusRunning:
			current.RunningSessionCount++
		}
		if latest == nil || session.Summary.UpdatedAt.After(latest.At) {
			latest = &AgentLatestRunSummary{
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

func ValidateAgentWorkspaceValue(workspaceID string, workspace *WorkspaceConfig, lookupErr error) error {
	if strings.TrimSpace(workspaceID) == "" {
		return nil
	}
	if lookupErr != nil {
		return lookupErr
	}
	if workspace == nil {
		return fmt.Errorf("workspace config %s not found", strings.TrimSpace(workspaceID))
	}
	switch strings.ToLower(strings.TrimSpace(workspace.Type)) {
	case "file", "git":
		return nil
	default:
		return fmt.Errorf("unsupported agent workspace type %q", workspace.Type)
	}
}
