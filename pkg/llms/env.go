package llms

import (
	"strings"

	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

func LoaderCommandFacadeAgentModel(env map[string]string) (string, string) {
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

func ProviderKeyName(name string) bool {
	return driverpkg.LLMProviderKeyName(name)
}

func FilterPersistedRuntimeEnv(items []domain.SessionEnvVar) []domain.SessionEnvVar {
	result := make([]domain.SessionEnvVar, 0, len(items))
	for _, item := range domain.NormalizeEnvItems(items) {
		if ProviderKeyName(item.Name) {
			continue
		}
		result = append(result, item)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func RuntimeEnvMap(items []domain.SessionEnvVar) map[string]string {
	env := make(map[string]string, len(items))
	for _, item := range domain.NormalizeEnvItems(items) {
		name := strings.TrimSpace(item.Name)
		if name == "" || ProviderKeyName(name) {
			continue
		}
		env[name] = item.Value
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func ManagedRuntimeEnvMap(items []domain.SessionEnvVar) map[string]string {
	env := make(map[string]string, len(items))
	for _, item := range domain.NormalizeEnvItems(items) {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		env[name] = item.Value
	}
	if len(env) == 0 {
		return nil
	}
	return env
}
