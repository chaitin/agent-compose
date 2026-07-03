package llms

import (
	"net/url"
	"strings"

	domain "agent-compose/pkg/model"
)

func EnvItemValue(items []domain.SessionEnvVar, key string) string {
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

func LooksLikeAnthropicMessagesEndpoint(endpoint string) bool {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return false
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return strings.HasSuffix(endpoint, "/messages")
	}
	return strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/messages")
}

func EnvHasProviderKeyForFamily(envItems []domain.SessionEnvVar, providerFamily string) bool {
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

func HasOpenAIEnvProviderInput(envItems []domain.SessionEnvVar) bool {
	endpoint := EnvItemValue(envItems, "LLM_API_ENDPOINT")
	if LooksLikeAnthropicMessagesEndpoint(endpoint) {
		return false
	}
	return strings.TrimSpace(firstNonEmpty(
		endpoint,
		EnvItemValue(envItems, "LLM_API_KEY"),
		EnvItemValue(envItems, "OPENAI_API_KEY"),
	)) != ""
}

func HasAnthropicEnvProviderInput(envItems []domain.SessionEnvVar) bool {
	return strings.TrimSpace(firstNonEmpty(
		EnvItemValue(envItems, "ANTHROPIC_BASE_URL"),
		EnvItemValue(envItems, "ANTHROPIC_API_ENDPOINT"),
		EnvItemValue(envItems, "ANTHROPIC_API_KEY"),
		EnvItemValue(envItems, "ANTHROPIC_AUTH_TOKEN"),
	)) != "" || LooksLikeAnthropicMessagesEndpoint(EnvItemValue(envItems, "LLM_API_ENDPOINT"))
}

func HasSessionEnvProviderInput(envItems []domain.SessionEnvVar) bool {
	return HasOpenAIEnvProviderInput(envItems) || HasAnthropicEnvProviderInput(envItems)
}

func SessionAnthropicEnvModel(envItems []domain.SessionEnvVar) string {
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
