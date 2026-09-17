package llms

import "strings"

// ProviderAuth selects how an upstream credential is presented on the wire.
//
// A connection's protocol describes the request body it speaks, not the header
// that carries its credential: a gateway can serve the Anthropic Messages
// protocol while authenticating with a bearer token. Operators therefore need
// to name the presentation, and the protocol only supplies the default.
type ProviderAuth string

const (
	// ProviderAuthXAPIKey sends the key as x-api-key without a scheme. It is the
	// Anthropic convention and the default for anthropic_messages.
	ProviderAuthXAPIKey ProviderAuth = "x-api-key"
	// ProviderAuthBearer sends the key as "Authorization: Bearer <key>". It is
	// the default for the OpenAI protocols.
	ProviderAuthBearer ProviderAuth = "bearer"
)

// ProviderProtocolAuth returns the credential header and scheme a protocol uses
// when no explicit presentation is configured.
func ProviderProtocolAuth(protocol string) (header, scheme string) {
	if protocol == APIProtocolMessages {
		return "x-api-key", ""
	}
	return "Authorization", "Bearer"
}

// ProviderAuthWire returns the header and scheme for an explicit presentation.
// The empty presentation reports no header so callers keep the protocol default.
func ProviderAuthWire(auth ProviderAuth) (header, scheme string) {
	switch auth {
	case ProviderAuthXAPIKey:
		return "x-api-key", ""
	case ProviderAuthBearer:
		return "Authorization", "Bearer"
	default:
		return "", ""
	}
}

// ResolveProviderAuth returns the effective credential header and scheme for a
// connection, preferring an explicit presentation over the protocol convention.
func ResolveProviderAuth(protocol string, auth ProviderAuth) (header, scheme string) {
	if header, scheme := ProviderAuthWire(auth); header != "" {
		return header, scheme
	}
	return ProviderProtocolAuth(protocol)
}

// ProviderAuthFromWire reports the presentation a stored header and scheme
// describe, or the empty presentation when they match no known convention.
// Scanned providers always carry a scheme for an Authorization header.
func ProviderAuthFromWire(header, scheme string) ProviderAuth {
	switch {
	case strings.EqualFold(strings.TrimSpace(header), "x-api-key"):
		return ProviderAuthXAPIKey
	case strings.EqualFold(strings.TrimSpace(header), "Authorization") && strings.EqualFold(strings.TrimSpace(scheme), "Bearer"):
		return ProviderAuthBearer
	default:
		return ""
	}
}
