package main

import (
	"fmt"
	"github.com/chaitin/agent-compose/pkg/compose"
	"github.com/chaitin/agent-compose/pkg/identity"
	"slices"
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

// interactivePromptProviders is the ordered set of providers whose guest runner
// can drive the interactive `run -i --prompt` loop, and it is also the list the
// unsupported error reports. It mirrors the daemon's prompt attach support in
// pkg/runs/prompt_attach.go: a provider absent from either set has no resumable
// provider session, so the loop would silently lose the previous turn's context.
// Keep both sets in the same commit when a provider gains or loses resume.
var interactivePromptProviders = [...]string{"codex", "claude", "opencode", "pi", "dsh"}

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
	if slices.Contains(interactivePromptProviders[:], provider) {
		return nil
	}
	flag := "run -i --prompt"
	if attach {
		flag = "run --prompt -it"
	}
	return commandExitError{
		Code: exitCodeUnsupported,
		Err:  fmt.Errorf("%s is unsupported for provider %s; supported providers: %s", flag, provider, strings.Join(interactivePromptProviders[:], ", ")),
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
	case "pi-agent", "pi_agent":
		return "pi"
	case "deepseek", "deepseek-harness", "deepseek_harness":
		return "dsh"
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

func composeDisplayResourceType(resourceType string) string {
	switch resourceType {
	case "agent_definition", "project_agent":
		return "agent"
	// These values are accepted only when rendering pre-cutover responses.
	// Current project reconciliation emits one native scheduler change.
	case "project_scheduler":
		return "trigger"
	case "loader":
		return ""
	default:
		return resourceType
	}
}
