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

func EnvHasProviderKeyForFamily(envItems []domain.SandboxEnvVar, providerFamily string) bool {
	switch NormalizeProviderType(providerFamily) {
	case ProviderFamilyAnthropic:
		return strings.TrimSpace(firstNonEmpty(
			EnvItemValue(envItems, "ANTHROPIC_API_KEY"),
			EnvItemValue(envItems, "ANTHROPIC_AUTH_TOKEN"),
			EnvItemValue(envItems, "LLM_API_KEY"),
		)) != ""
	case ProviderFamilyOpenAI:
		return strings.TrimSpace(firstNonEmpty(
			EnvItemValue(envItems, "LLM_API_KEY"),
			EnvItemValue(envItems, "OPENAI_API_KEY"),
		)) != ""
	default:
		return false
	}
}

func HasOpenAIEnvProviderInput(envItems []domain.SandboxEnvVar) bool {
	return hasOpenAIEnvProviderInput(envItems) ||
		hasGenericLLMEnvProviderInput(envItems) && genericLLMEnvProviderFamily(envItems) == ProviderFamilyOpenAI
}

func HasAnthropicEnvProviderInput(envItems []domain.SandboxEnvVar) bool {
	return hasAnthropicEnvProviderInput(envItems) ||
		hasGenericLLMEnvProviderInput(envItems) && genericLLMEnvProviderFamily(envItems) == ProviderFamilyAnthropic
}

func hasOpenAIEnvProviderInput(envItems []domain.SandboxEnvVar) bool {
	return strings.TrimSpace(EnvItemValue(envItems, "OPENAI_API_KEY")) != ""
}

func hasAnthropicEnvProviderInput(envItems []domain.SandboxEnvVar) bool {
	return strings.TrimSpace(firstNonEmpty(
		EnvItemValue(envItems, "ANTHROPIC_BASE_URL"),
		EnvItemValue(envItems, "ANTHROPIC_API_ENDPOINT"),
		EnvItemValue(envItems, "ANTHROPIC_API_KEY"),
		EnvItemValue(envItems, "ANTHROPIC_AUTH_TOKEN"),
	)) != ""
}

func hasGenericLLMEnvProviderInput(envItems []domain.SandboxEnvVar) bool {
	return strings.TrimSpace(firstNonEmpty(
		EnvItemValue(envItems, "LLM_API_ENDPOINT"),
		EnvItemValue(envItems, "LLM_API_KEY"),
	)) != ""
}

func genericLLMEnvProviderFamily(envItems []domain.SandboxEnvVar) string {
	if !hasGenericLLMEnvProviderInput(envItems) {
		return ""
	}
	hasOpenAI := hasOpenAIEnvProviderInput(envItems)
	hasAnthropic := hasAnthropicEnvProviderInput(envItems)
	switch {
	case hasAnthropic && !hasOpenAI:
		return ProviderFamilyAnthropic
	case hasOpenAI && !hasAnthropic:
		return ProviderFamilyOpenAI
	case NormalizeWireAPI(EnvItemValue(envItems, "LLM_API_PROTOCOL")) == APIProtocolMessages:
		return ProviderFamilyAnthropic
	default:
		return ProviderFamilyOpenAI
	}
}

func HasSessionEnvProviderInput(envItems []domain.SandboxEnvVar) bool {
	return HasOpenAIEnvProviderInput(envItems) || HasAnthropicEnvProviderInput(envItems)
}

func SessionAnthropicEnvModel(envItems []domain.SandboxEnvVar) string {
	genericModel := EnvItemValue(envItems, "LLM_MODEL")
	return firstNonEmpty(
		EnvItemValue(envItems, "ANTHROPIC_MODEL"),
		EnvItemValue(envItems, "CLAUDE_MODEL"),
		genericModel,
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
	if strings.TrimSpace(current) == "" {
		return next
	}
	preferredFamily = NormalizeOptionalProviderType(preferredFamily)
	if preferredFamily != "" && NormalizeProviderType(nextFamily) == preferredFamily {
		return next
	}
	return current
}
