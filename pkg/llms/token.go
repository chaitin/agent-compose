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
