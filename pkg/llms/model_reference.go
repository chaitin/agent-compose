package llms

import (
	"fmt"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// SplitModelReference parses the agent-facing model selection shared by the
// facade agents (pi, opencode, dsh).
//
// The <connection>/<model> prefix is optional. When it is present it keeps its
// established meaning and is dispatched by the caller: a configured connection
// id, a family alias (openai/anthropic), or an env-backed custom provider.
// When it is absent the whole value is a literal model name and the caller
// resolves it against the daemon's default connection, so agent configuration
// never has to repeat routing the daemon already knows.
//
// An absent value stays valid and resolves to the daemon default. A value that
// carries a slash but leaves a side empty ("/model" or "connection/") is a typo
// rather than a literal model name, so it is rejected here instead of being
// forwarded to an upstream as a model that cannot exist. The failure is an
// invalid argument rather than a missing configuration, so an agent that may
// fall back to credentials it carries itself still reports the typo.
func SplitModelReference(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !strings.Contains(value, "/") {
		return "", value, nil
	}
	providerID, model, ok := SplitProviderModelReference(value)
	if !ok {
		return "", "", domain.ClassifyError(domain.ErrInvalidArgument, fmt.Sprintf(
			"llm model reference %q must not leave either side of <llm-provider-id>/<model-name> empty", value), nil)
	}
	return providerID, model, nil
}
