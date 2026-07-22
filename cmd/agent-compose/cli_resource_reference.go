package main

import (
	"agent-compose/pkg/compose"
	"agent-compose/pkg/identity"
	"fmt"
	"strings"
)

func resourceRefMatches(ref string, values ...string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == ref || (len(ref) >= 6 && strings.HasPrefix(value, ref)) {
			return true
		}
	}
	return false
}

type composeAgentRefCandidate struct {
	Name    string
	ID      string
	ShortID string
}

func resourceIDMatchesRef(id, shortID, ref string) bool {
	id = strings.TrimSpace(id)
	shortID = strings.TrimSpace(shortID)
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	if ref == id || (shortID != "" && ref == shortID) {
		return true
	}
	normalizedRef := strings.TrimPrefix(strings.ToLower(ref), identity.Prefix)
	normalizedID := strings.TrimPrefix(strings.ToLower(id), identity.Prefix)
	if !identity.IsIDPrefix(normalizedRef) {
		return false
	}
	return strings.HasPrefix(normalizedID, normalizedRef)
}

func validateInteractivePromptProvider(project *compose.NormalizedProjectSpec, agentName string, attach bool) error {
	provider := "codex"
	for _, agent := range project.Agents {
		if strings.TrimSpace(agent.Name) == strings.TrimSpace(agentName) {
			if normalized := normalizeInteractivePromptProvider(agent.Provider); normalized != "" {
				provider = normalized
			}
			break
		}
	}
	if !attach {
		switch provider {
		case "codex", "claude", "opencode":
			return nil
		default:
			return commandExitError{
				Code: exitCodeUnsupported,
				Err:  fmt.Errorf("run -i --prompt is unsupported for provider %s; supported providers: codex, claude, opencode", provider),
			}
		}
	}
	switch provider {
	case "codex", "claude":
		return nil
	default:
		return commandExitError{
			Code: exitCodeUnsupported,
			Err:  fmt.Errorf("run --prompt -it is unsupported for provider %s; supported providers: codex, claude", provider),
		}
	}
}

func normalizeInteractivePromptProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "":
		return ""
	case "claude-code", "claude_code":
		return "claude"
	case "open-code", "open_code":
		return "opencode"
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

func composeDisplayResourceType(resourceType string) string {
	switch resourceType {
	case "agent_definition", "project_agent":
		return "agent"
	case "project_scheduler":
		return "trigger"
	case "loader":
		return ""
	default:
		return resourceType
	}
}
