package llms

import (
	"errors"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/model"
)

// GuestProviderAgentCompose is the provider key the daemon writes into a guest
// CLI's own configuration when that CLI addresses models as
// "<provider>/<model>". The key belongs to the daemon, never to the operator,
// so renaming an upstream connection cannot change what the guest sees.
const GuestProviderAgentCompose = "agent-compose"

// ErrUnsupportedAgentDialect reports an agent kind that has no LLM dialect.
var ErrUnsupportedAgentDialect = errors.New("unsupported agent llm dialect")

// Dialect describes how one agent CLI talks to a model server.
//
// Supported lists the inbound protocols the CLI can speak natively. Canonical
// is the inbound protocol used when the upstream serves something the CLI
// cannot speak; that is the only situation that requires protocol conversion.
// GuestProvider is the provider key used when the CLI addresses models as
// "<provider>/<model>", and is empty when the CLI addresses the model directly.
type Dialect struct {
	Kind          string
	Supported     []Protocol
	Canonical     Protocol
	GuestProvider string
}

// DialectFor returns the LLM dialect of an agent kind. It returns
// ErrUnsupportedAgentDialect for agents that do not consume a managed LLM
// configuration, rather than silently falling back to another agent's rules.
func DialectFor(agentKind string) (Dialect, error) {
	switch kind := model.NormalizeAgentKind(agentKind); kind {
	case "codex":
		return Dialect{
			Kind:      kind,
			Supported: []Protocol{ProtocolResponses},
			Canonical: ProtocolResponses,
		}, nil
	case "claude":
		return Dialect{
			Kind:      kind,
			Supported: []Protocol{ProtocolMessages},
			Canonical: ProtocolMessages,
		}, nil
	case "opencode":
		// OpenCode reaches the facade through the AI SDK: its OpenAI-compatible
		// provider posts chat completions, and its Anthropic provider posts
		// messages. It cannot speak the Responses API, so that is the one
		// upstream it needs converted.
		return Dialect{
			Kind:          kind,
			Supported:     []Protocol{ProtocolChatCompletions, ProtocolMessages},
			Canonical:     ProtocolChatCompletions,
			GuestProvider: GuestProviderAgentCompose,
		}, nil
	case "pi":
		return Dialect{
			Kind:          kind,
			Supported:     []Protocol{ProtocolChatCompletions, ProtocolResponses, ProtocolMessages},
			Canonical:     ProtocolChatCompletions,
			GuestProvider: GuestProviderAgentCompose,
		}, nil
	case "dsh":
		// The DSH profile declares its provider separately from its model, so
		// the model string never carries a provider prefix.
		return Dialect{
			Kind:      kind,
			Supported: []Protocol{ProtocolChatCompletions, ProtocolResponses, ProtocolMessages},
			Canonical: ProtocolChatCompletions,
		}, nil
	default:
		return Dialect{}, fmt.Errorf("%w: %q", ErrUnsupportedAgentDialect, strings.TrimSpace(agentKind))
	}
}

// Supports reports whether the CLI can speak the upstream protocol directly.
func (d Dialect) Supports(upstream Protocol) bool {
	for _, supported := range d.Supported {
		if supported == upstream {
			return true
		}
	}
	return false
}

// InboundProtocol returns the protocol the daemon must serve to this CLI for
// an upstream that speaks upstream. It equals upstream exactly when the CLI
// can speak it, so passthrough is always preferred over conversion.
func (d Dialect) InboundProtocol(upstream Protocol) Protocol {
	if d.Supports(upstream) {
		return upstream
	}
	return d.Canonical
}

// NeedsConversion reports whether serving this CLI requires converting between
// the upstream protocol and the inbound protocol.
func (d Dialect) NeedsConversion(upstream Protocol) bool {
	return d.InboundProtocol(upstream) != upstream
}

// GuestModel returns the model string handed to the guest CLI. This is the
// only place in the call chain that composes a "<provider>/<model>" string,
// and the result is an output: the daemon never parses it back.
func (d Dialect) GuestModel(model string) string {
	model = strings.TrimSpace(model)
	if d.GuestProvider == "" || model == "" {
		return model
	}
	return d.GuestProvider + "/" + model
}
