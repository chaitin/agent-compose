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

// ProviderFamilyEnv returns the provider-specific variables that address one
// provider family through the runtime facade: the credential, the endpoint the
// credential is presented at, and the model names.
//
// They are compatibility names: an older guest image that predates the generic
// LLM_* contract reads them instead. Two writers publish them — the startup
// compatibility facade, which serves an image whose selected agent the daemon
// does not configure, and the dialect writer of the agent itself. Both call
// this function so the two sets cannot drift.
//
// A dialect writer must publish the whole set for a family or none of it. The
// managed environment is applied after the startup facade, so a family name only
// the startup facade writes keeps the startup facade's value: a writer that
// replaces the family credential while leaving its model behind hands the guest
// a token and a model that disagree. The guest then asks that connection for a
// model this run never resolved, and the facade forwards it rather than
// refusing, so the mistake reaches the provider instead of being caught here.
// dsh publishes none of these names, which leaves the startup facade's token,
// endpoint and model intact as one consistent set.
func ProviderFamilyEnv(family, credential, endpoint, model string) map[string]string {
	family = NormalizeProviderType(family)
	env := make(map[string]string, 5)
	if family == ProviderFamilyAnthropic {
		env["ANTHROPIC_API_KEY"] = credential
		env["ANTHROPIC_AUTH_TOKEN"] = credential
		env["ANTHROPIC_BASE_URL"] = endpoint
	} else {
		env["OPENAI_API_KEY"] = credential
		env["OPENAI_BASE_URL"] = endpoint
	}
	for name, value := range providerModelEnvAliases(family, model) {
		env[name] = value
	}
	return env
}

func providerModelEnvAliases(family, model string) map[string]string {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	if NormalizeProviderType(family) == ProviderFamilyAnthropic {
		return map[string]string{"ANTHROPIC_MODEL": model, "CLAUDE_MODEL": model}
	}
	return map[string]string{"CODEX_MODEL": model, "OPENAI_MODEL": model}
}

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

// FilterPersistedRuntimeEnv removes the LLM provider configuration an operator
// declared from the environment a sandbox persists and shows. The declaration
// itself is kept in ProviderEnvItems, which is what the facade resolves against;
// the guest only ever receives the facade address and a run-scoped token.
func FilterPersistedRuntimeEnv(items []domain.SandboxEnvVar) []domain.SandboxEnvVar {
	result := make([]domain.SandboxEnvVar, 0, len(items))
	for _, item := range domain.NormalizeEnvItems(items) {
		if driverpkg.LLMProviderEnvName(item.Name) || strings.EqualFold(strings.TrimSpace(item.Name), RuntimeBaseURLEnvName) {
			continue
		}
		result = append(result, item)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
