package llms

import (
	"strings"

	domain "agent-compose/pkg/model"
)

func EnvItemValue(items []domain.SandboxEnvVar, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	for _, item := range domain.NormalizeEnvItems(items) {
		if strings.EqualFold(strings.TrimSpace(item.Name), key) {
			return strings.TrimSpace(item.Value)
		}
	}
	return ""
}

func HasOpenAIEnvProviderInput(envItems []domain.SandboxEnvVar) bool {
	return strings.TrimSpace(firstNonEmpty(
		EnvItemValue(envItems, "LLM_API_ENDPOINT"),
		EnvItemValue(envItems, "LLM_API_PROTOCOL"),
		EnvItemValue(envItems, "LLM_API_KEY"),
		EnvItemValue(envItems, "OPENAI_API_KEY"),
		EnvItemValue(envItems, "LLM_MODEL"),
	)) != ""
}

func HasAnthropicEnvProviderInput(envItems []domain.SandboxEnvVar) bool {
	return strings.TrimSpace(firstNonEmpty(
		EnvItemValue(envItems, "ANTHROPIC_BASE_URL"),
		EnvItemValue(envItems, "ANTHROPIC_API_ENDPOINT"),
		EnvItemValue(envItems, "ANTHROPIC_API_KEY"),
		EnvItemValue(envItems, "ANTHROPIC_AUTH_TOKEN"),
		EnvItemValue(envItems, "ANTHROPIC_MODEL"),
		EnvItemValue(envItems, "CLAUDE_MODEL"),
	)) != ""
}

func HasProviderEnvInputForFamily(envItems []domain.SandboxEnvVar, providerFamily string) bool {
	switch NormalizeOptionalProviderType(providerFamily) {
	case ProviderFamilyAnthropic:
		return HasAnthropicEnvProviderInput(envItems)
	case ProviderFamilyOpenAI:
		return HasOpenAIEnvProviderInput(envItems)
	default:
		return false
	}
}

func SessionAnthropicEnvModel(envItems []domain.SandboxEnvVar) string {
	return firstNonEmpty(
		EnvItemValue(envItems, "ANTHROPIC_MODEL"),
		EnvItemValue(envItems, "CLAUDE_MODEL"),
	)
}

func SessionEnvProviderID(sessionID, providerFamily string) string {
	sessionID = strings.TrimSpace(sessionID)
	providerFamily = NormalizeOptionalProviderType(providerFamily)
	if sessionID == "" || providerFamily == "" {
		return ""
	}
	return "session-env:" + sessionID + ":" + providerFamily
}

func IsSessionEnvProviderID(providerID string) bool {
	return strings.HasPrefix(strings.TrimSpace(providerID), "session-env:")
}

func ChooseSessionEnvProviderID(current, next, nextFamily, preferredFamily string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return current
	}
	preferredFamily = NormalizeOptionalProviderType(preferredFamily)
	if preferredFamily != "" {
		if NormalizeProviderType(nextFamily) == preferredFamily {
			return next
		}
		return current
	}
	if strings.TrimSpace(current) == "" {
		return next
	}
	return current
}
