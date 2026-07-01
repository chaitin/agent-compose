package agentcompose

import (
	"encoding/json"
	"strings"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

const redactedValue = "********"
const runtimeContextRedactionErrorKey = "agentCompose.redactionError"

func redactRuntimeContextJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return "{}"
	}
	var context agentcomposev2.RuntimeContext
	if err := json.Unmarshal([]byte(raw), &context); err != nil {
		fallback := &agentcomposev2.RuntimeContext{
			Metadata: map[string]string{
				runtimeContextRedactionErrorKey: "invalid runtime context JSON",
			},
		}
		encoded, marshalErr := json.Marshal(fallback)
		if marshalErr != nil {
			return `{"metadata":{"agentCompose.redactionError":"invalid runtime context JSON"}}`
		}
		return string(encoded)
	}
	redactStringMap(context.Metadata)
	redactStringMap(context.Env)
	redactStringMap(context.IdentityContext)
	if context.CapabilityScope != nil {
		redactStringMap(context.CapabilityScope.Metadata)
	}
	redacted, err := json.Marshal(&context)
	if err != nil {
		return "{}"
	}
	return string(redacted)
}

func redactStringMap(values map[string]string) {
	for key := range values {
		if runtimeContextKeyIsSensitive(key) {
			values[key] = redactedValue
		}
	}
}

func runtimeContextKeyIsSensitive(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"), ".", "_"))
	if normalized == "" {
		return false
	}
	if runtimeContextKeyIsProviderCredential(normalized) {
		return true
	}
	for _, marker := range []string{"token", "secret", "password", "passwd", "api_key", "apikey", "auth", "credential"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func runtimeContextKeyIsProviderCredential(normalized string) bool {
	switch strings.ToUpper(normalized) {
	case "LLM_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENROUTER_API_KEY", "AZURE_OPENAI_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY":
		return true
	default:
		return false
	}
}
