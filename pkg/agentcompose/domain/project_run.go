package domain

import (
	"fmt"
	"strings"
)

const (
	ProjectRunStatusPending   = "pending"
	ProjectRunStatusRunning   = "running"
	ProjectRunStatusSucceeded = "succeeded"
	ProjectRunStatusFailed    = "failed"
	ProjectRunStatusCanceled  = "canceled"

	ProjectRunSourceManual    = "manual"
	ProjectRunSourceScheduler = "scheduler"
	ProjectRunSourceAPI       = "api"
)

func NormalizeProjectRunStatus(status string) string {
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

func NormalizeProjectRunSource(source string) string {
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

func ValidateProjectRunTransition(from, to string) error {
	from = NormalizeProjectRunStatus(from)
	to = NormalizeProjectRunStatus(to)
	if from == to {
		return nil
	}
	if ProjectRunStatusIsTerminal(from) {
		return fmt.Errorf("project run transition %s -> %s is not allowed: run is already terminal", from, to)
	}
	switch from {
	case ProjectRunStatusPending:
		switch to {
		case ProjectRunStatusRunning, ProjectRunStatusFailed, ProjectRunStatusCanceled:
			return nil
		}
	case ProjectRunStatusRunning:
		switch to {
		case ProjectRunStatusSucceeded, ProjectRunStatusFailed, ProjectRunStatusCanceled:
			return nil
		}
	}
	return fmt.Errorf("project run transition %s -> %s is not allowed", from, to)
}

func ProjectRunStatusIsTerminal(status string) bool {
	switch NormalizeProjectRunStatus(status) {
	case ProjectRunStatusSucceeded, ProjectRunStatusFailed, ProjectRunStatusCanceled:
		return true
	default:
		return false
	}
}
