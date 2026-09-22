package llms

import "strings"

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

// ProtocolForFamily returns the canonical protocol of an upstream family.
func ProtocolForFamily(family string) Protocol {
	if strings.TrimSpace(family) == ProviderFamilyAnthropic {
		return ProtocolMessages
	}
	return ProtocolResponses
}
