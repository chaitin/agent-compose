package llms

import (
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
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
	return EnvHasProviderKeyForFamily(envItems, ProviderFamilyOpenAI) && (hasOpenAIEnvProviderInput(envItems) ||
		hasGenericLLMEnvProviderInput(envItems) && genericLLMEnvProviderFamily(envItems) == ProviderFamilyOpenAI)
}

func HasAnthropicEnvProviderInput(envItems []domain.SandboxEnvVar) bool {
	return EnvHasProviderKeyForFamily(envItems, ProviderFamilyAnthropic) && (hasAnthropicEnvProviderInput(envItems) ||
		hasGenericLLMEnvProviderInput(envItems) && genericLLMEnvProviderFamily(envItems) == ProviderFamilyAnthropic)
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

// SessionEnvModel returns the model the sandbox's own provider environment
// names. A model id may itself contain slashes — a gateway injecting
// LLM_MODEL=<provider>/<model> publishes a qualified logical name — so a caller
// that lets the environment own the model must use this value verbatim instead
// of the remainder of an agent's <connection>/<model> declaration.
func SessionEnvModel(envItems []domain.SandboxEnvVar) string {
	return firstNonEmptyTrimmed(SessionAnthropicEnvModel(envItems), EnvItemValue(envItems, "LLM_MODEL"))
}

// sessionEnvModelForDeclaration returns the model a facade must resolve for an
// agent declaration when the sandbox publishes its own provider environment.
//
// A facade splits <connection>/<model> at the first slash, but the model a
// sandbox environment publishes is a single id that may legitimately contain
// slashes: an orchestrator that materializes one qualified name into both the
// agent declaration and LLM_MODEL (a gateway publishing <provider>/<model>
// logical names, for example) would otherwise have its name truncated to the
// remainder and address a model that upstream does not serve. A declaration
// that names exactly the published model therefore resolves verbatim; any other
// declaration keeps its established precedence, including a family alias or a
// configured connection that deliberately selects a different model.
//
// declaredProviderID and declaredModel are the two halves the caller split; the
// caller keeps the returned value as its requested model.
func sessionEnvModelForDeclaration(declaredProviderID, declaredModel string, envItems []domain.SandboxEnvVar) string {
	requestedModel := strings.TrimSpace(declaredModel)
	declared := requestedModel
	if prefix := strings.TrimSpace(declaredProviderID); prefix != "" {
		declared = prefix + "/" + requestedModel
	}
	if envModel := SessionEnvModel(envItems); envModel != "" && envModel == declared {
		return envModel
	}
	return requestedModel
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
