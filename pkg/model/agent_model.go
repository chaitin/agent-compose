package model

import (
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/sources"
)

const (
	DefaultAgentProvider = "codex"

	AgentSandboxTagSource    = "source"
	AgentSandboxTagSourceVal = "agent"
	AgentSandboxTagID        = "agent_id"
	AgentSandboxTagName      = "agent_name"
	AgentSandboxTagProvider  = "agent_provider"
)

type AgentDefinition struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	Enabled      bool              `json:"enabled"`
	DeletedAt    time.Time         `json:"deleted_at,omitempty"`
	Provider     string            `json:"provider"`
	Model        string            `json:"model,omitempty"`
	SystemPrompt string            `json:"system_prompt,omitempty"`
	Driver       string            `json:"driver,omitempty"`
	GuestImage   string            `json:"guest_image,omitempty"`
	WorkspaceID  string            `json:"workspace_id,omitempty"`
	EnvItems     []SandboxEnvVar   `json:"env_items,omitempty"`
	Volumes      []VolumeMountSpec `json:"volumes,omitempty"`
	ConfigJSON   string            `json:"config_json"`
	CapsetIDs    []string          `json:"capset_ids,omitempty"`
	Skills       []AgentSkill      `json:"skills,omitempty"`
	// Project ownership uses native v2 names internally. The JSON tags retain
	// their historical names for existing event and runtime consumers.
	ProjectID       string    `json:"managed_project_id,omitempty"`
	ProjectRevision int64     `json:"managed_project_revision,omitempty"`
	AgentName       string    `json:"managed_agent_name,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type AgentSkill struct {
	Name     string `json:"name,omitempty"`
	Provider string `json:"provider,omitempty"`
	URL      string `json:"url,omitempty"`
	Ref      string `json:"ref,omitempty"`
	Path     string `json:"path,omitempty"`
	Format   string `json:"format,omitempty"`
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
	case "pi", "pi-agent", "pi_agent":
		return "pi"
	case "dsh", "deepseek", "deepseek-harness", "deepseek_harness":
		return "dsh"
	default:
		return agent
	}
}

func NormalizeAgentSkills(skills []AgentSkill) []AgentSkill {
	if len(skills) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(skills))
	out := make([]AgentSkill, 0, len(skills))
	for _, skill := range skills {
		skill.Name = strings.TrimSpace(skill.Name)
		source := AgentSkillSource(skill)
		skill.Provider = source.Provider
		skill.URL = source.URL
		skill.Ref = source.Ref
		skill.Path = source.Path
		skill.Format = source.Format
		skill.Username = source.Username
		skill.Password = source.Password
		skill.Token = source.Token
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

func AgentSkillSource(skill AgentSkill) sources.Source {
	return sources.Source{
		Provider: skill.Provider,
		URL:      skill.URL,
		Ref:      skill.Ref,
		Path:     skill.Path,
		Format:   skill.Format,
		Username: skill.Username,
		Password: skill.Password,
		Token:    skill.Token,
	}.Normalized()
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
