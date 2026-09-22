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

// ResolveUpstreamModel maps the model a guest asked for to the model the
// upstream knows, reporting ok=false when the token does not authorize it.
//
// A connection-bound token already records both names, so the daemon resolves
// this by exact string comparison and never parses a model reference. The guest
// addresses the model in whatever namespace its own configuration uses (pi and
// opencode use <connection>/<model>), and a model id that itself contains a
// slash survives because nothing is split.
//
// A request that matches neither name is forwarded verbatim: the token is bound
// to a connection, and that upstream decides which models it serves. Only a
// token with no connection keeps the legacy behaviour of pinning one model.
func (t FacadeToken) ResolveUpstreamModel(requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	if guestModel := strings.TrimSpace(t.GuestModel); guestModel != "" && requested == guestModel {
		return strings.TrimSpace(t.Model), true
	}
	pinned := strings.TrimSpace(t.Model)
	if t.ProviderID == "" && pinned != "" {
		return pinned, pinned == requested
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
