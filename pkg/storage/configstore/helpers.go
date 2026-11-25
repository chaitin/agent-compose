package configstore

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func NormalizeWorkspaceConfig(item domain.WorkspaceConfig, assignID bool) (domain.WorkspaceConfig, error) {
	item.ID = strings.TrimSpace(item.ID)
	item.Name = strings.TrimSpace(item.Name)
	item.Type = strings.ToLower(strings.TrimSpace(item.Type))
	item.ConfigJSON = strings.TrimSpace(item.ConfigJSON)
	item.Comment = strings.TrimSpace(item.Comment)
	if assignID && item.ID == "" {
		item.ID = uuid.NewString()
	}
	if item.ID == "" {
		return domain.WorkspaceConfig{}, fmt.Errorf("workspace config id is required")
	}
	if item.Name == "" {
		return domain.WorkspaceConfig{}, fmt.Errorf("workspace config name is required")
	}
	if item.Type == "" {
		return domain.WorkspaceConfig{}, fmt.Errorf("workspace config type is required")
	}
	if item.Type != "git" && item.Type != "file" {
		return domain.WorkspaceConfig{}, fmt.Errorf("unsupported workspace config type %q", item.Type)
	}
	if item.ConfigJSON == "" {
		item.ConfigJSON = "{}"
	}
	return item, nil
}

func BoolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
