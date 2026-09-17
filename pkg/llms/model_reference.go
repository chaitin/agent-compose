package llms

import "strings"

// SplitModelReference parses the agent-facing model selection shared by the
// facade agents (pi, opencode, dsh).
//
// The <connection>/<model> prefix is optional. When it is present it keeps its
// established meaning and is dispatched by the caller: a configured connection
// id, a family alias (openai/anthropic), or an env-backed custom provider.
// When it is absent the whole value is a literal model name and the caller
// resolves it against the daemon's default connection, so agent configuration
// never has to repeat routing the daemon already knows.
func SplitModelReference(value string) (providerID, model string) {
	value = strings.TrimSpace(value)
	if prefix, rest, ok := SplitProviderModelReference(value); ok {
		return prefix, rest
	}
	return "", value
}
