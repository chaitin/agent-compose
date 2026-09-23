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
// Supported lists the inbound protocols the CLI can speak natively, most
// preferred first. Canonical is the inbound protocol used when the upstream
// serves something the CLI cannot speak; that is the only situation that
// requires protocol conversion. GuestProvider is the provider key used when the
// CLI addresses models as "<provider>/<model>", and is empty when the CLI
// addresses the model directly.
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
		// Pi speaks all three protocols natively. When one model is served over
		// several of them, the response API is the one it runs best against, so
		// the order below is an affinity rather than an enumeration.
		return Dialect{
			Kind:          kind,
			Supported:     []Protocol{ProtocolResponses, ProtocolChatCompletions, ProtocolMessages},
			Canonical:     ProtocolChatCompletions,
			GuestProvider: GuestProviderAgentCompose,
		}, nil
	case "dsh":
		// The DSH profile declares its provider separately from its model, so
		// the model string never carries a provider prefix.
		//
		// GuestProvider is deliberately left empty. An older DSH guest truncated
		// the model argument at its first slash, and the daemon used to shield it
		// by sending a disposable "agent-compose/" prefix. That direction was
		// dropped on purpose: support runs one way, so a new runtime tolerates an
		// older daemon but a new daemon does not accommodate an older guest.
		// docs/pages/guest-image-abi.md states the upgrade order this requires.
		// Do not reintroduce the prefix without changing that document.
		return Dialect{
			Kind:      kind,
			Supported: []Protocol{ProtocolResponses, ProtocolChatCompletions, ProtocolMessages},
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

// PreferredProtocols returns every upstream protocol ordered by this dialect's
// affinity: the protocols the CLI speaks natively first, so that a model
// several connections serve is resolved to a passthrough whenever one is
// available, then the protocols it cannot speak. A configuration that only
// offers conversions still resolves, because refusing to run would be worse
// than converting; among those, the order is stable and keeps the two OpenAI
// protocols ahead of the Anthropic one.
func (d Dialect) PreferredProtocols() ProtocolPreference {
	conversionOrder := DefaultProtocolPreference()
	preference := make(ProtocolPreference, 0, len(conversionOrder))
	preference = append(preference, d.Supported...)
	for _, protocol := range conversionOrder {
		if !d.Supports(protocol) {
			preference = append(preference, protocol)
		}
	}
	return preference
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
