package llms

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
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
	// FacadeToken.ResolveUpstreamModel.
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

// ResolveUpstreamModel maps the model a guest asked for to the model the
// upstream knows, reporting ok=false when the token does not authorize it.
//
// A token that records a model is pinned to it. The daemon resolves this by
// exact string comparison and never parses a model reference: the guest
// addresses the model in whatever namespace its own configuration uses (pi and
// opencode use <connection>/<model>), and a model id that itself contains a
// slash survives because nothing is split.
//
// A token that records no model forwards the request verbatim instead, and the
// connection it names decides which models it serves. Only the startup
// compatibility facade mints such a token, and on purpose: it serves guest
// images the daemon does not configure, whose entrypoint may send a model name
// the daemon cannot predict, so no one model can be pinned for it. Every token
// the daemon mints for a run it configures itself records a model and stays
// pinned, which is what keeps the proxy a weak caller only where it must be —
// see the note on the Connections resolver in pkg/agentcompose/app/app.go.
func (t FacadeToken) ResolveUpstreamModel(requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	if guestModel := strings.TrimSpace(t.GuestModel); guestModel != "" && requested == guestModel {
		model := strings.TrimSpace(t.Model)
		return model, model != ""
	}
	pinned := strings.TrimSpace(t.Model)
	if pinned != "" {
		if requested == pinned {
			return pinned, true
		}
		return "", false
	}
	return requested, true
}

func HashFacadeToken(value string) (string, string) {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	hash := hex.EncodeToString(sum[:])
	if len(hash) < 12 {
		return hash, hash
	}
	return hash, hash[:12]
}
