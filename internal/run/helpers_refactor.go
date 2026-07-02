package run

import "strings"

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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
