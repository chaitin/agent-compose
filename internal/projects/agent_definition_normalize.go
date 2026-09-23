package projects

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/idset"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/volumes"
)

// NormalizeAgentDefinition enforces the invariants an agent definition must
// satisfy before it can be stored or executed. assignDefaults fills in the
// default provider for definitions that have not chosen one; callers that are
// validating user input rather than materializing a revision pass false so a
// missing provider is reported instead of silently defaulted.
func NormalizeAgentDefinition(item domain.AgentDefinition, assignDefaults bool) (domain.AgentDefinition, error) {
	item.ID = strings.TrimSpace(item.ID)
	item.Name = strings.TrimSpace(item.Name)
	item.Description = strings.TrimSpace(item.Description)
	item.Provider = domain.NormalizeAgentKind(item.Provider)
	if item.Provider == "" && assignDefaults {
		item.Provider = domain.DefaultAgentProvider
	}
	item.Model = strings.TrimSpace(item.Model)
	item.SystemPrompt = strings.TrimSpace(item.SystemPrompt)
	item.Driver = strings.TrimSpace(item.Driver)
	item.GuestImage = strings.TrimSpace(item.GuestImage)
	item.WorkspaceID = strings.TrimSpace(item.WorkspaceID)
	item.CapsetIDs = idset.Normalize(item.CapsetIDs)
	item.ProjectID = strings.TrimSpace(item.ProjectID)
	item.AgentName = strings.TrimSpace(item.AgentName)
	item.ConfigJSON = strings.TrimSpace(item.ConfigJSON)
	if item.ConfigJSON == "" {
		item.ConfigJSON = "{}"
	}
	item.Skills = domain.NormalizeAgentSkills(item.Skills)
	if item.ID == "" {
		return domain.AgentDefinition{}, fmt.Errorf("agent definition id is required")
	}
	if item.Name == "" {
		return domain.AgentDefinition{}, fmt.Errorf("agent definition name is required")
	}
	if item.Provider == "" {
		return domain.AgentDefinition{}, fmt.Errorf("agent definition provider is required")
	}
	if item.Provider != "codex" && item.Provider != "claude" && item.Provider != "gemini" && item.Provider != "opencode" && item.Provider != "pi" && item.Provider != "dsh" {
		return domain.AgentDefinition{}, fmt.Errorf("agent definition provider %q is not supported", item.Provider)
	}
	if !isJSONObject(item.ConfigJSON) {
		return domain.AgentDefinition{}, fmt.Errorf("agent definition config_json must be a JSON object")
	}
	if item.ProjectID == "" {
		item.ProjectRevision = 0
		item.AgentName = ""
	} else {
		if item.AgentName == "" {
			return domain.AgentDefinition{}, fmt.Errorf("managed agent name is required")
		}
		if item.ProjectRevision < 0 {
			return domain.AgentDefinition{}, fmt.Errorf("managed project revision cannot be negative")
		}
	}
	item.EnvItems = domain.NormalizeEnvItems(item.EnvItems)
	mounts, err := volumes.NormalizeMountSpecs(item.Volumes)
	if err != nil {
		return domain.AgentDefinition{}, fmt.Errorf("agent definition volumes: %w", err)
	}
	item.Volumes = mounts
	return item, nil
}

func isJSONObject(raw string) bool {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &decoded); err != nil {
		return false
	}
	return decoded != nil
}
