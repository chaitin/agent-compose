package model

import (
	"agent-compose/pkg/identity"
	"fmt"
	"path/filepath"
	"strings"
)

func StableProjectID(name, sourcePath string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("project name is required")
	}
	if !IsProjectStableIdentifier(name) {
		return "", fmt.Errorf("project name %q is not a stable identifier", name)
	}
	return identity.NewID(identity.ResourceProject, name, NormalizeProjectSourcePath(sourcePath)), nil
}

func StableManagedAgentID(projectID, agentName string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	agentName = strings.TrimSpace(agentName)
	if projectID == "" || agentName == "" {
		return "", fmt.Errorf("project id and agent name are required")
	}
	if !IsProjectStableIdentifier(agentName) {
		return "", fmt.Errorf("agent name %q is not a stable identifier", agentName)
	}
	return identity.NewID(identity.ResourceAgent, projectID, agentName), nil
}

func StableProjectSchedulerID(projectID, agentName, schedulerName string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	agentName = strings.TrimSpace(agentName)
	schedulerName = strings.TrimSpace(schedulerName)
	if schedulerName == "" {
		schedulerName = "default"
	}
	if projectID == "" || agentName == "" {
		return "", fmt.Errorf("project id and agent name are required")
	}
	if !IsProjectStableIdentifier(agentName) {
		return "", fmt.Errorf("agent name %q is not a stable identifier", agentName)
	}
	if !IsProjectStableIdentifier(schedulerName) {
		return "", fmt.Errorf("scheduler name %q is not a stable identifier", schedulerName)
	}
	return identity.NewID(identity.ResourceScheduler, projectID, agentName, schedulerName), nil
}

func StableManagedLoaderID(projectID, agentName, schedulerName string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	agentName = strings.TrimSpace(agentName)
	schedulerName = strings.TrimSpace(schedulerName)
	if schedulerName == "" {
		schedulerName = "default"
	}
	if projectID == "" || agentName == "" {
		return "", fmt.Errorf("project id and agent name are required")
	}
	if !IsProjectStableIdentifier(agentName) {
		return "", fmt.Errorf("agent name %q is not a stable identifier", agentName)
	}
	if !IsProjectStableIdentifier(schedulerName) {
		return "", fmt.Errorf("scheduler name %q is not a stable identifier", schedulerName)
	}
	return identity.NewID(identity.ResourceLoader, projectID, agentName, schedulerName), nil
}

func StableManagedTriggerID(projectID, agentName, schedulerName, triggerName string, triggerIndex int) (string, error) {
	projectID = strings.TrimSpace(projectID)
	agentName = strings.TrimSpace(agentName)
	schedulerName = strings.TrimSpace(schedulerName)
	triggerName = strings.TrimSpace(triggerName)
	if schedulerName == "" {
		schedulerName = "default"
	}
	if projectID == "" || agentName == "" || triggerIndex < 0 {
		return "", fmt.Errorf("project id, agent name, and trigger index are required")
	}
	if !IsProjectStableIdentifier(agentName) {
		return "", fmt.Errorf("agent name %q is not a stable identifier", agentName)
	}
	if !IsProjectStableIdentifier(schedulerName) {
		return "", fmt.Errorf("scheduler name %q is not a stable identifier", schedulerName)
	}
	readable := triggerName
	seedPart := "name:" + triggerName
	if readable == "" {
		seedPart = fmt.Sprintf("path:triggers[%d]", triggerIndex)
	}
	return identity.NewID(identity.ResourceTrigger, projectID, agentName, schedulerName, seedPart), nil
}

func StableProjectRunID(projectID, agentName, source, idempotencyKey string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	agentName = strings.TrimSpace(agentName)
	source = strings.TrimSpace(source)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if projectID == "" || agentName == "" || source == "" || idempotencyKey == "" {
		return "", fmt.Errorf("project id, agent name, source, and idempotency key are required")
	}
	if !IsProjectStableIdentifier(agentName) {
		return "", fmt.Errorf("agent name %q is not a stable identifier", agentName)
	}
	return identity.NewID(identity.ResourceRun, projectID, agentName, source, idempotencyKey), nil
}

func NormalizeProjectSourcePath(sourcePath string) string {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return ""
	}
	if abs, err := filepath.Abs(sourcePath); err == nil {
		sourcePath = abs
	}
	return filepath.Clean(sourcePath)
}

func StableReadableID(prefix, readable, seed string) string {
	return identity.NewID(identity.ResourceKind(prefix), readable, seed)
}

func IsProjectStableIdentifier(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for i, r := range value {
		switch {
		case i == 0 && r >= 'a' && r <= 'z':
		case i > 0 && r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		case i > 0 && (r == '-' || r == '_'):
		default:
			return false
		}
	}
	return true
}
