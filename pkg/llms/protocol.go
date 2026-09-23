package llms

// Protocol is one wire protocol on one side of the LLM call chain.
//
// The same three values describe the protocol an upstream connection serves
// and the protocol an agent CLI can speak, but they are never interchangeable
// at the type level: Provider.DefaultWireAPI always names the upstream
// protocol, while Dialect.Supported always names the inbound protocols an
// agent accepts. Neither is derived from the other by inspecting a string.
type Protocol string

const (
	// ProtocolResponses is the OpenAI Responses API (/v1/responses).
	ProtocolResponses Protocol = APIProtocolResponses
	// ProtocolChatCompletions is the OpenAI Chat Completions API
	// (/v1/chat/completions).
	ProtocolChatCompletions Protocol = APIProtocolChatCompletions
	// ProtocolMessages is the Anthropic Messages API (/v1/messages).
	ProtocolMessages Protocol = APIProtocolMessages
)

// NormalizeProtocol maps a configuration spelling onto a canonical Protocol.
// The empty string means the response-API default, matching NormalizeWireAPI.
func NormalizeProtocol(value string) Protocol {
	return Protocol(NormalizeWireAPI(value))
}

// Valid reports whether p is one of the three supported protocols.
func (p Protocol) Valid() bool {
	switch p {
	case ProtocolResponses, ProtocolChatCompletions, ProtocolMessages:
		return true
	default:
		return false
	}
}

// Family returns the upstream provider family that serves p.
func (p Protocol) Family() string {
	if p == ProtocolMessages {
		return ProviderFamilyAnthropic
	}
	return ProviderFamilyOpenAI
}

// ProtocolPreference orders upstream protocols from best to worst for one
// caller.
//
// It exists so that a model several connections serve resolves to a connection
// the caller can speak natively whenever one exists: an upstream protocol equal
// to the inbound protocol needs no bridge, which makes passthrough the better
// answer than a conversion that merely happens to be available.
type ProtocolPreference []Protocol

// DefaultProtocolPreference is the order used when no agent CLI applies, as for
// a daemon-owned call: the response API first, then chat completions, then
// messages.
func DefaultProtocolPreference() ProtocolPreference {
	return ProtocolPreference{ProtocolResponses, ProtocolChatCompletions, ProtocolMessages}
}

// rank returns the position of protocol in the preference. A protocol the
// preference does not list ranks after every protocol it does, together with
// the other unlisted ones; an empty preference ranks all protocols equally.
func (p ProtocolPreference) rank(protocol Protocol) int {
	for index, candidate := range p {
		if candidate == protocol {
			return index
		}
	}
	return len(p)
}
