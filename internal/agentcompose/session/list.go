package session

import (
	"strings"
	"time"
)

const DefaultListLimit = 50

func NormalizeTriggerSource(value string, tags []Tag) string {
	value = strings.TrimSpace(value)
	if value != "" {
		if value == TypeManual || strings.HasPrefix(value, TypeScript+":") {
			return value
		}
	}
	loaderID := ""
	hasLoaderOrigin := false
	for _, tag := range tags {
		name := strings.ToLower(strings.TrimSpace(tag.Name))
		value := strings.TrimSpace(tag.Value)
		switch name {
		case "origin":
			hasLoaderOrigin = strings.EqualFold(value, "loader")
		case "loader_id":
			loaderID = value
		}
	}
	if hasLoaderOrigin && loaderID != "" {
		return TypeScript + ":" + loaderID
	}
	return TypeManual
}

func TypeFromTriggerSource(value string) string {
	value = NormalizeTriggerSource(value, nil)
	if strings.HasPrefix(value, TypeScript+":") {
		return TypeScript
	}
	return TypeManual
}

func NormalizeListBounds(offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = DefaultListLimit
	}
	return offset, limit
}

func Paginate(items []*Session, offset, limit int) []*Session {
	if offset >= len(items) {
		return nil
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

func MatchesListOptions(session *Session, options ListOptions) bool {
	if session == nil {
		return false
	}
	summary := session.Summary
	if value := strings.ToLower(strings.TrimSpace(options.SessionType)); value != "" {
		if TypeFromTriggerSource(summary.TriggerSource) != value {
			return false
		}
	}
	if value := strings.ToLower(strings.TrimSpace(options.TriggerSourceQuery)); value != "" {
		if !strings.Contains(strings.ToLower(summary.TriggerSource), value) {
			return false
		}
	}
	if value := strings.ToLower(strings.TrimSpace(options.TitleQuery)); value != "" {
		if !strings.Contains(strings.ToLower(summary.Title), value) {
			return false
		}
	}
	if value := strings.ToLower(strings.TrimSpace(options.WorkspaceQuery)); value != "" {
		workspaceValues := []string{
			summary.WorkspacePath,
			session.WorkspaceID,
		}
		if session.Workspace != nil {
			workspaceValues = append(workspaceValues, session.Workspace.ID, session.Workspace.Name, session.Workspace.Type)
		}
		matched := false
		for _, item := range workspaceValues {
			if strings.Contains(strings.ToLower(strings.TrimSpace(item)), value) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if value := strings.ToLower(strings.TrimSpace(options.Driver)); value != "" {
		if strings.ToLower(strings.TrimSpace(summary.Driver)) != value {
			return false
		}
	}
	if value := strings.ToUpper(strings.TrimSpace(options.VMStatus)); value != "" {
		if strings.ToUpper(strings.TrimSpace(summary.VMStatus)) != value {
			return false
		}
	}
	if !MatchesTimeRange(summary.CreatedAt, options.CreatedFrom, options.CreatedTo) {
		return false
	}
	if !MatchesTimeRange(summary.UpdatedAt, options.UpdatedFrom, options.UpdatedTo) {
		return false
	}
	return true
}

func MatchesTimeRange(value, from, to time.Time) bool {
	if !from.IsZero() && value.Before(from) {
		return false
	}
	if !to.IsZero() && value.After(to) {
		return false
	}
	return true
}
