package credentials

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultHandleTTL is the lifetime of a handle whose requester names no
	// preference.
	DefaultHandleTTL = 15 * time.Minute
	// MaxHandleTTL bounds every handle lifetime. A credential handle is a
	// lease, not a credential: a caller cannot mint a long-lived one, and there
	// is deliberately no renew operation, so expiry means the sandbox must ask
	// the daemon again rather than extend its own authority from inside.
	MaxHandleTTL = time.Hour

	// TokenPrefix distinguishes a generic credential bearer value from other
	// daemon-issued tokens.
	TokenPrefix = "ac_cred_"
)

// NewHandleRequest describes a handle to mint. It excludes computed fields so
// there is no ambiguity about what the caller controls; expiry is a duration
// rather than an absolute time so it cannot exceed MaxHandleTTL.
type NewHandleRequest struct {
	Kind      Kind
	EnvName   string
	SandboxID string
	RunID     string
	Scope     Scope
	TTL       time.Duration
}

// NewHandle mints a raw bearer value and the hashed handle that records it. The
// raw value is returned exactly once and must go directly to the sandbox secret
// mechanism; callers must never log it or persist it.
//
// now is the issuance instant and the anchor for the TTL. An invalid request or
// a handle that fails normalization is rejected before any value is generated.
func NewHandle(req NewHandleRequest, now time.Time) (string, Handle, error) {
	ttl := req.TTL
	if ttl <= 0 {
		ttl = DefaultHandleTTL
	}
	if ttl > MaxHandleTTL {
		return "", Handle{}, fmt.Errorf("%w: ttl %s exceeds maximum %s", ErrInvalidHandle, ttl, MaxHandleTTL)
	}
	issuedAt := now.UTC()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", Handle{}, fmt.Errorf("generate credential token: %w", err)
	}
	tokenValue := TokenPrefix + hex.EncodeToString(raw)
	hash, fingerprint := HashToken(tokenValue)
	handle := Handle{
		ID:               "cred_" + hash[:16],
		Kind:             req.Kind,
		TokenHash:        hash,
		TokenFingerprint: fingerprint,
		EnvName:          strings.TrimSpace(req.EnvName),
		SandboxID:        strings.TrimSpace(req.SandboxID),
		RunID:            strings.TrimSpace(req.RunID),
		Scope:            req.Scope.Normalized(),
		IssuedAt:         issuedAt,
		ExpiresAt:        issuedAt.Add(ttl),
	}
	if err := handle.Validate(); err != nil {
		return "", Handle{}, err
	}
	return tokenValue, handle.Normalized(), nil
}

// HashToken returns the hex SHA-256 of a raw bearer value and a short
// fingerprint suitable for logs and error messages. It is the same digest
// llms.HashFacadeToken computes, so a facade token and its generic handle agree
// on identity across the specialization boundary.
func HashToken(value string) (string, string) {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	hash := hex.EncodeToString(sum[:])
	if len(hash) < 12 {
		return hash, hash
	}
	return hash, hash[:12]
}
