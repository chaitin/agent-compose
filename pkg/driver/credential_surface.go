package driver

import (
	"strings"

	"github.com/chaitin/agent-compose/pkg/credentials"
)

// CredentialSurface classifies a declared environment variable or MCP header
// that carries a long-lived credential.
//
// The engine's credential surface used to be implicit: LLM provider keys were
// recognized by name because the facade absorbed them, and everything else was
// simply not modeled. This type makes the whole surface explicit so a
// recognized credential can be redacted from views and reported honestly,
// rather than appearing to be protected because nothing named it.
type CredentialSurface struct {
	// Kind is the credential form the name belongs to.
	Kind credentials.Kind
	// Absorbable reports whether the daemon can already replace the declared
	// plaintext value with a short-lived, scoped handle on the way into a
	// sandbox. Only the LLM facade is absorbable today: the daemon holds the
	// upstream key and gives the guest a run-scoped facade token. Git, MCP, and
	// registry credentials are recognized and hidden from views, but still
	// travel as plaintext environment until the credential broker has a
	// declared endpoint to scope them to, so they are reported as not yet
	// isolated instead of being described as protected.
	Absorbable bool
}

// CredentialEnvName reports whether name is a recognized long-lived credential
// and which handle kind it maps to.
//
// It generalizes LLMProviderCredentialEnvName rather than replacing it: the LLM
// predicate stays the single list of provider credential names that pkg/llms is
// checked against, and this function adds the non-LLM forms the engine
// recognizes. MCP servers carry headers as well as environment (Authorization,
// X-API-Key, ...), so the same table answers for both; the names are compared
// case-insensitively after trimming.
func CredentialEnvName(name string) (CredentialSurface, bool) {
	normalized := strings.ToUpper(strings.TrimSpace(name))
	if normalized == "" {
		return CredentialSurface{}, false
	}
	if LLMProviderCredentialEnvName(normalized) {
		return CredentialSurface{Kind: credentials.KindLLMFacade, Absorbable: true}, true
	}
	switch normalized {
	case "GIT_TOKEN", "GITHUB_TOKEN", "GH_TOKEN", "GITLAB_TOKEN", "BITBUCKET_TOKEN", "GITEA_TOKEN":
		return CredentialSurface{Kind: credentials.KindGit}, true
	case "MCP_TOKEN", "MCP_API_KEY", "MCP_AUTH_TOKEN", "MCP_ACCESS_TOKEN", "MCP_SERVER_TOKEN":
		return CredentialSurface{Kind: credentials.KindMCP}, true
	case "REGISTRY_TOKEN", "REGISTRY_PASSWORD", "DOCKER_PASSWORD", "DOCKER_AUTH_CONFIG",
		"NPM_TOKEN", "NODE_AUTH_TOKEN", "CARGO_REGISTRY_TOKEN":
		return CredentialSurface{Kind: credentials.KindRegistry}, true
	case "AUTHORIZATION", "PROXY_AUTHORIZATION", "PROXY-AUTHORIZATION",
		"X_API_KEY", "X-API-KEY", "API_KEY", "API-KEY",
		"X_AUTH_TOKEN", "X-AUTH-TOKEN", "PRIVATE_TOKEN", "PRIVATE-TOKEN":
		// Auth header names are shared by MCP servers and custom endpoints, so
		// they are recognized as credential material without claiming a form.
		// Both the underscore and hyphen spellings are accepted because an MCP
		// header keeps the operator's original spelling.
		return CredentialSurface{Kind: credentials.KindGeneric}, true
	case "CAP_TOKEN":
		// The capability gateway token is daemon-issued but is still handed to
		// the guest as a plaintext bearer value; moving the proxy to the handle
		// model is part of the deferred broker work.
		return CredentialSurface{Kind: credentials.KindGeneric}, true
	default:
		return CredentialSurface{}, false
	}
}

// IsHeldCredentialName reports whether the daemon holds name's value alone, so
// a user-facing view must not echo it.
//
// The distinction is deliberate and mirrors the existing LLM rule: the daemon
// redacts a name only when the value never reaches the guest. A recognized but
// not-yet-absorbed credential (git, MCP, registry) still travels as plaintext
// environment, and hiding it from a view would describe an exposed value as
// protected without changing the exposure. CredentialEnvName enumerates those
// forms so the broker can absorb them; until then the declaration stays the
// visible source of truth.
func IsHeldCredentialName(name string) bool {
	surface, ok := CredentialEnvName(name)
	return ok && surface.Absorbable
}
