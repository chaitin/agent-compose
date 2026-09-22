package llms

import (
	"fmt"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// directEndpointDefaults are the public endpoints a declared credential is used
// against when the agent names no endpoint. They are the same endpoints the
// previous environment-backed provider bootstrap assumed, and they are a
// property of each vendor rather than a routing choice by the daemon.
const (
	directOpenAIEndpoint    = "https://api.openai.com"
	directAnthropicEndpoint = "https://api.anthropic.com"
)

// directAgentUpstream is an upstream the agent declared itself, in its own
// environment.
type directAgentUpstream struct {
	Endpoint string
	APIKey   string
	// Protocol is the wire protocol the declared upstream speaks. It is the
	// agent's own protocol, because in direct mode the daemon neither proxies
	// nor converts.
	Protocol Protocol
	// Model is the model the declaration names, or "" when it names none.
	Model string
}

// directUpstreamFromAgentEnv reports the upstream an agent declared in its own
// environment.
//
// This is the whole of the direct/managed rule: an agent that publishes its own
// LLM connection is served by that connection, and one that publishes none is
// served by the daemon's catalog. The two paths are mutually exclusive, so an
// agent either owns its upstream or the daemon does — never a mixture.
//
// A credential alone is a complete declaration, because the vendor's public
// endpoint is part of the vendor rather than a daemon choice. An endpoint alone
// is not: the daemon has no credential to present and must not invent one.
func directUpstreamFromAgentEnv(env []domain.SandboxEnvVar, dialect Dialect) (directAgentUpstream, bool) {
	if len(env) == 0 {
		return directAgentUpstream{}, false
	}
	if key := envItemFirst(env, "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"); key != "" {
		return directAgentUpstream{
			Endpoint: envItemOr(env, directAnthropicEndpoint, "ANTHROPIC_BASE_URL", "LLM_API_ENDPOINT"),
			APIKey:   key,
			Protocol: ProtocolMessages,
			Model:    envItemFirst(env, "ANTHROPIC_MODEL", "CLAUDE_MODEL", "LLM_MODEL"),
		}, true
	}
	genericKey := envItemFirst(env, "OPENAI_API_KEY", "LLM_API_KEY")
	if genericKey == "" {
		return directAgentUpstream{}, false
	}
	protocol := directProtocolFromEnv(env, dialect)
	return directAgentUpstream{
		Endpoint: envItemOr(env, directEndpointFor(protocol), "OPENAI_BASE_URL", "LLM_API_ENDPOINT"),
		APIKey:   genericKey,
		Protocol: protocol,
		Model:    envItemFirst(env, "LLM_MODEL", "OPENAI_MODEL"),
	}, true
}

// directProtocolFromEnv reads the declared wire protocol of an agent that
// publishes a generic credential. An agent that declares none is assumed to
// speak its own canonical protocol: that is the protocol its CLI would pick for
// an OpenAI-compatible endpoint, and nothing is converting on this path.
func directProtocolFromEnv(env []domain.SandboxEnvVar, dialect Dialect) Protocol {
	// NormalizeProtocol maps an empty string onto responses, so an undeclared
	// protocol must be distinguished before normalizing: the agent's canonical
	// protocol is the right default, not always responses.
	if raw := envItemFirst(env, "LLM_API_PROTOCOL"); raw != "" {
		if declared := NormalizeProtocol(raw); declared.Valid() {
			return declared
		}
	}
	return dialect.Canonical
}

func directEndpointFor(protocol Protocol) string {
	if protocol == ProtocolMessages {
		return directAnthropicEndpoint
	}
	return directOpenAIEndpoint
}

// prepareDirectAgentLLM configures a run whose upstream the agent declared
// itself.
//
// The daemon mints no token and the proxy is not involved, so the declared
// credential reaches the guest unchanged. That is the point of the mode: an
// operator who publishes a key in an agent's environment is choosing to give the
// agent that key, and the way to hide a key or to have the daemon convert
// protocols is to configure a connection in the catalog instead.
//
// The declared protocol must be one the agent can speak. Nothing converts on
// this path, so a mismatch is reported rather than silently rewritten — the
// operator chose the protocol when they published the upstream.
func prepareDirectAgentLLM(req AgentLLMRequest, dialect Dialect, upstream directAgentUpstream) (*AgentLLM, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = upstream.Model
	}
	if model == "" {
		return nil, ErrNoModel
	}
	if !dialect.Supports(upstream.Protocol) {
		return nil, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf(
			"agent %q cannot speak the %s protocol its own environment declares; configure the upstream as a daemon connection to have it converted",
			dialect.Kind, upstream.Protocol), nil)
	}
	prepared := &AgentLLM{
		Dialect:    dialect,
		Model:      model,
		GuestModel: dialect.GuestModel(model),
		Upstream:   upstream.Protocol,
		Inbound:    upstream.Protocol,
		Direct:     true,
		Endpoint:   strings.TrimRight(strings.TrimSpace(upstream.Endpoint), "/"),
		Credential: upstream.APIKey,
	}
	env, err := writeDialectGuestConfig(req.Config, req.Sandbox, prepared)
	if err != nil {
		return nil, err
	}
	prepared.Env = env
	return prepared, nil
}

// envItemFirst returns the first non-empty value among the named environment
// items, in the order given, or "" when none is set.
func envItemFirst(items []domain.SandboxEnvVar, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(EnvItemValue(items, name)); value != "" {
			return value
		}
	}
	return ""
}

// envItemOr is envItemFirst with a default for an environment that names
// nothing.
func envItemOr(items []domain.SandboxEnvVar, fallback string, names ...string) string {
	if value := envItemFirst(items, names...); value != "" {
		return value
	}
	return fallback
}
