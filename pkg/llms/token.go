package llms

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/egress"
)

// NewFacadeTokenRequest describes the token NewFacadeToken mints. It
// deliberately excludes FacadeToken's computed fields (TokenHash,
// TokenFingerprint, IssuedAt, ...) so there's no ambiguity about which
// fields a caller controls.
type NewFacadeTokenRequest struct {
	SandboxID  string
	Model      string
	ProviderID string
	WireAPI    string
	// GuestModel is the model reference the guest will address. It is what the
	// proxy matches a request against to recover Model; see
	// FacadeToken.UpstreamModel.
	GuestModel string
	Source     string
	RunID      string
}

func NewFacadeToken(req NewFacadeTokenRequest) (string, FacadeToken, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", FacadeToken{}, err
	}
	tokenValue := "ac_llm_" + hex.EncodeToString(raw)
	hash, fingerprint := HashFacadeToken(tokenValue)
	now := time.Now().UTC()
	normalizedWireAPI := strings.TrimSpace(req.WireAPI)
	if normalizedWireAPI != "" {
		normalizedWireAPI = NormalizeWireAPI(normalizedWireAPI)
	}
	return tokenValue, FacadeToken{
		SandboxID:        strings.TrimSpace(req.SandboxID),
		TokenHash:        hash,
		TokenFingerprint: fingerprint,
		Model:            strings.TrimSpace(req.Model),
		ProviderID:       strings.TrimSpace(req.ProviderID),
		WireAPI:          normalizedWireAPI,
		GuestModel:       strings.TrimSpace(req.GuestModel),
		Source:           strings.TrimSpace(req.Source),
		RunID:            strings.TrimSpace(req.RunID),
		IssuedAt:         now,
	}, nil
}

// HasConnection reports whether the token names a connection the request must be
// routed through. It is the single definition of "this token is bound": the
// egress policy and the runtime proxy both read it, so a blank provider ID
// cannot be interpreted in two different ways. A provider ID that is empty after
// trimming is not a connection, because NewFacadeToken and the resolver treat
// it as absent.
func (t FacadeToken) HasConnection() bool {
	return strings.TrimSpace(t.ProviderID) != ""
}

// ServesWireAPI reports whether the token may be presented on the facade route
// that serves route.
//
// A token is bound to a protocol family, not to one protocol in it. The wire
// API recorded at issue time is the one the selected agent speaks, but the
// variables the token is published under belong to the whole family, and any
// program in the image may read them: a codex sandbox publishes its token as
// OPENAI_API_KEY, and a second CLI the entrypoint starts can post chat
// completions with it. Both OpenAI routes decode their own protocol and reach
// the connection through the protocol bridge, so the family is what keeps a
// request on routes the connection can be converted to. An Anthropic token is
// still refused on the OpenAI routes, and the reverse.
//
// A token with no recorded wire API serves every route, as before.
func (t FacadeToken) ServesWireAPI(route string) bool {
	if strings.TrimSpace(t.WireAPI) == "" {
		return true
	}
	issued := catalogProtocolFamily(NormalizeWireAPI(t.WireAPI))
	return issued != "" && issued == catalogProtocolFamily(NormalizeWireAPI(route))
}

// ResolveUpstreamModel maps the model a guest asked for to the model the
// upstream knows, reporting ok=false only when the token names no upstream the
// request could belong to.
//
// A token that names a connection authorizes that connection rather than one
// model on it: a request that matches neither recorded name is forwarded
// verbatim, and the upstream decides which models it serves. The daemon is a
// weak caller by design — see the note on the Connections resolver in
// pkg/agentcompose/app/app.go — and it never parses a model reference. The guest
// addresses the model in whatever namespace its own configuration uses (pi and
// opencode use <connection>/<model>), and a model id that itself contains a
// slash survives because nothing is split.
//
// Only a token with no connection keeps the legacy behaviour of pinning one
// model, because there is no upstream for a request to belong to.
//
// The decision itself is evaluated by the shared egress entry point against
// FacadeEgressPolicy; this method reports the resolved model and whether the
// token authorized it.
func (t FacadeToken) ResolveUpstreamModel(requested string) (string, bool) {
	result := egress.Decide(FacadeEgressPolicy(t), FacadeEgressRequest(t, requested))
	return result.Target, result.Allowed()
}

func HashFacadeToken(value string) (string, string) {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	hash := hex.EncodeToString(sum[:])
	if len(hash) < 12 {
		return hash, hash
	}
	return hash, hash[:12]
}
