package session

import (
	"strings"
	"time"
)

const defaultSessionListLimit = 50

func normalizeSessionTriggerSource(value string, tags []SessionTag) string {
	value = strings.TrimSpace(value)
	if value != "" {
		if value == SessionTypeManual || strings.HasPrefix(value, SessionTypeScript+":") {
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
		return SessionTypeScript + ":" + loaderID
	}
	return SessionTypeManual
}

func sessionTypeFromTriggerSource(value string) string {
	value = normalizeSessionTriggerSource(value, nil)
	if strings.HasPrefix(value, SessionTypeScript+":") {
		return SessionTypeScript
	}
	return SessionTypeManual
}

func normalizeSessionListBounds(offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = defaultSessionListLimit
	}
	return offset, limit
}

func paginateSessions(items []*Session, offset, limit int) []*Session {
	if offset >= len(items) {
		return nil
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

func sessionMatchesListOptions(session *Session, options SessionListOptions) bool {
	if session == nil {
		return false
	}
	summary := session.Summary
	if value := strings.ToLower(strings.TrimSpace(options.SessionType)); value != "" {
		if sessionTypeFromTriggerSource(summary.TriggerSource) != value {
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
	if !matchesTimeRange(summary.CreatedAt, options.CreatedFrom, options.CreatedTo) {
		return false
	}
	if !matchesTimeRange(summary.UpdatedAt, options.UpdatedFrom, options.UpdatedTo) {
		return false
	}
	return true
}

func matchesTimeRange(value, from, to time.Time) bool {
	if !from.IsZero() && value.Before(from) {
		return false
	}
	if !to.IsZero() && value.After(to) {
		return false
	}
	return true
}
