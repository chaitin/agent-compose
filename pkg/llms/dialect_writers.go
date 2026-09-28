package llms

import (
	"fmt"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// writeDialectGuestConfig writes the guest-side LLM configuration for one
// resolved run and returns the environment the agent process inherits.
//
// Each agent owns its own file format and environment names, so the writers
// stay separate; what they share is that none of them resolves anything. They
// receive an already-chosen model, connection, and inbound protocol.
func writeDialectGuestConfig(config *appconfig.Config, sandbox *domain.Sandbox, prepared *AgentLLM) (map[string]string, error) {
	switch prepared.Dialect.Kind {
	case "codex":
		return writeCodexGuestConfig(config, sandbox, prepared)
	case "claude":
		return writeClaudeGuestConfig(config, sandbox, prepared)
	case "opencode":
		return writeOpenCodeGuestConfig(config, sandbox, prepared)
	case "pi":
		return writePiGuestConfig(config, sandbox, prepared)
	case "dsh":
		return writeDshGuestConfig(config, sandbox, prepared)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedAgentDialect, prepared.Dialect.Kind)
	}
}

// guestFacadeTokenEnvName is the environment variable a guest presents its
// run-scoped facade token through. Every run goes through the facade, so this is
// the only credential a guest ever receives.
const guestFacadeTokenEnvName = "AGENT_COMPOSE_SANDBOX_TOKEN"

// guestCredentialEnv is the generic LLM environment every agent receives: the
// endpoint to send calls to, the credential to present, and the protocol in use.
//
// The credential is always the run-scoped facade token. An upstream credential
// an operator published in project or agent environment is imported into the
// daemon's connection configuration, so no path puts a real key in a guest.
func guestCredentialEnv(prepared *AgentLLM) map[string]string {
	return map[string]string{
		"LLM_API_ENDPOINT": prepared.Endpoint,
		"LLM_API_KEY":      prepared.Credential,
		// The protocol in use, for operators and for restart inspection. No guest
		// runner reads it: the agent's own protocol is pinned by the route it is
		// configured with.
		"LLM_API_PROTOCOL":      string(prepared.Upstream),
		guestFacadeTokenEnvName: prepared.Credential,
	}
}

// guestCredentialEnvName is the environment variable the guest CLI reads its
// credential from. Every run presents the facade token, so its generated CLI
// configuration always points at guestFacadeTokenEnvName.
func guestCredentialEnvName(prepared *AgentLLM) string {
	return guestFacadeTokenEnvName
}

// facadeEndpoint returns the facade route that serves inbound. The route path
// is what selects the protocol at the proxy, so it is derived from the inbound
// protocol and never from the upstream.
func facadeEndpoint(baseURL, sandboxID string, inbound Protocol) string {
	if inbound == ProtocolMessages {
		return baseURL + "/api/runtime/sandboxes/" + sandboxID + "/llm/anthropic"
	}
	return baseURL + "/api/runtime/sandboxes/" + sandboxID + "/llm/openai/v1"
}

// agentProtocolSpelling is the protocol name the pi-ai adapter family uses in
// guest configuration.
func agentProtocolSpelling(protocol Protocol) string {
	switch protocol {
	case ProtocolResponses:
		return "openai-responses"
	case ProtocolChatCompletions:
		return "openai-completions"
	case ProtocolMessages:
		return "anthropic-messages"
	default:
		return ""
	}
}

func writeCodexGuestConfig(config *appconfig.Config, sandbox *domain.Sandbox, prepared *AgentLLM) (map[string]string, error) {
	if err := WriteCodexRuntimeConfig(sandbox, CodexRuntimeConfig{
		Model: prepared.Model, BaseURL: prepared.Endpoint, WireAPI: string(prepared.Inbound),
		CredentialEnv: guestCredentialEnvName(prepared),
		Policy:        CodexRuntimePolicyFromConfig(config),
	}); err != nil {
		return nil, err
	}
	env := guestCredentialEnv(prepared)
	env["LLM_MODEL"] = prepared.Model
	env["CODEX_MODEL"] = prepared.Model
	env[GuestModelEnvName] = prepared.Model
	env["OPENAI_API_KEY"] = prepared.Credential
	env["OPENAI_BASE_URL"] = prepared.Endpoint
	return env, nil
}

func writeClaudeGuestConfig(_ *appconfig.Config, sandbox *domain.Sandbox, prepared *AgentLLM) (map[string]string, error) {
	env := guestCredentialEnv(prepared)
	// The claude runner maps the generic LLM_* variables onto Anthropic's own
	// names, and the CLI reads the latter.
	env["ANTHROPIC_API_KEY"] = prepared.Credential
	env["ANTHROPIC_AUTH_TOKEN"] = prepared.Credential
	env["ANTHROPIC_BASE_URL"] = prepared.Endpoint
	env["ANTHROPIC_MODEL"] = prepared.Model
	env["CLAUDE_MODEL"] = prepared.Model
	env[GuestModelEnvName] = prepared.Model
	return env, nil
}

func writeOpenCodeGuestConfig(config *appconfig.Config, sandbox *domain.Sandbox, prepared *AgentLLM) (map[string]string, error) {
	endpoint := prepared.Endpoint
	// The Anthropic SDK appends the version segment itself, so its provider
	// base carries /v1 explicitly while the raw facade route does not.
	configBaseURL := endpoint
	if prepared.Inbound == ProtocolMessages {
		configBaseURL = endpoint + "/v1"
	}
	if err := WriteOpenCodeRuntimeConfig(sandbox, prepared.Inbound, prepared.Model, configBaseURL, guestCredentialEnvName(prepared)); err != nil {
		return nil, err
	}
	env := guestCredentialEnv(prepared)
	env["OPENCODE_CONFIG"] = GuestOpenCodeConfigPath(config)
	env["LLM_MODEL"] = prepared.GuestModel
	env["OPENCODE_MODEL"] = prepared.GuestModel
	env[GuestModelEnvName] = prepared.GuestModel
	if prepared.Inbound == ProtocolMessages {
		env["ANTHROPIC_API_KEY"] = prepared.Credential
		env["ANTHROPIC_AUTH_TOKEN"] = prepared.Credential
		env["ANTHROPIC_BASE_URL"] = endpoint
	} else {
		env["OPENAI_API_KEY"] = prepared.Credential
		env["OPENAI_BASE_URL"] = endpoint
	}
	return env, nil
}

func writePiGuestConfig(config *appconfig.Config, sandbox *domain.Sandbox, prepared *AgentLLM) (map[string]string, error) {
	if err := WritePiRuntimeConfig(sandbox, prepared.Model, prepared.Endpoint, agentProtocolSpelling(prepared.Inbound), guestCredentialEnvName(prepared)); err != nil {
		return nil, err
	}
	env := guestCredentialEnv(prepared)
	env["PI_CODING_AGENT_DIR"] = GuestPiAgentDir(config)
	env[GuestModelEnvName] = prepared.GuestModel
	if prepared.Inbound == ProtocolMessages {
		env["ANTHROPIC_API_KEY"] = prepared.Credential
	} else {
		env["OPENAI_API_KEY"] = prepared.Credential
	}
	return env, nil
}

func writeDshGuestConfig(_ *appconfig.Config, sandbox *domain.Sandbox, prepared *AgentLLM) (map[string]string, error) {
	env := guestCredentialEnv(prepared)
	env["DSH_WIRE_API"] = agentProtocolSpelling(prepared.Inbound)
	env["DSH_MODEL"] = prepared.Model
	env[GuestModelEnvName] = prepared.Model
	env["DSH_PERMISSION_MODE"] = "danger-full-access"
	return env, nil
}
