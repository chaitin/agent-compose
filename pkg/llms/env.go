package llms

import (
	"strings"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

const RuntimeBaseURLEnvName = "AGENT_COMPOSE_RUNTIME_BASE_URL"

// GuestModelEnvName carries the model name the runtime facade resolved for a
// sandbox, in the namespace the guest agent addresses models by. The daemon
// tells the runner this name instead of the model the agent declared, because a
// declaration is a request that resolution may rewrite: a catalog default may
// supply the model, and pi and opencode address models as
// "<provider>/<model>", so a prefix the declaration never had is added for
// them. Passing the declaration through instead makes the agent CLI and the
// facade token disagree, which the agent reports as a hung or failed model call.
const GuestModelEnvName = "AGENT_COMPOSE_RESOLVED_MODEL"

// EnvItemValue returns the value of one environment item, matched
// case-insensitively and trimmed, or "" when the item is absent.
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

func SchedulerCommandFacadeAgentModel(env map[string]string) (string, string) {
	if env == nil {
		return domain.DefaultAgentProvider, ""
	}
	agent := domain.NormalizeAgentKind(firstNonEmpty(
		env["PROJECT_AGENT_LLM_PROVIDER"],
		env["AGENT_COMPOSE_LLM_PROVIDER"],
		env["LLM_AGENT_PROVIDER"],
		env["PROJECT_AGENT_PROVIDER"],
		env["AGENT_PROVIDER"],
		env["AGENT_COMPOSE_PROVIDER"],
		domain.DefaultAgentProvider,
	))
	switch agent {
	case "codex":
		return agent, firstNonEmpty(env["CODEX_MODEL"], env["LLM_MODEL"])
	case "claude":
		return agent, firstNonEmpty(env["ANTHROPIC_MODEL"], env["CLAUDE_MODEL"], env["LLM_MODEL"])
	case "opencode":
		model := firstNonEmpty(env["OPENCODE_MODEL"], env["LLM_MODEL"])
		if strings.TrimSpace(model) == "" {
			return "", ""
		}
		return agent, model
	default:
		return "", ""
	}
}

func FilterPersistedRuntimeEnv(items []domain.SandboxEnvVar) []domain.SandboxEnvVar {
	result := make([]domain.SandboxEnvVar, 0, len(items))
	for _, item := range domain.NormalizeEnvItems(items) {
		if driverpkg.LLMProviderKeyName(item.Name) || strings.EqualFold(strings.TrimSpace(item.Name), RuntimeBaseURLEnvName) {
			continue
		}
		result = append(result, item)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
