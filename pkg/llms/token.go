package llms

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

func NewFacadeToken(sessionID, model, providerID, wireAPI, source, runID string) (string, FacadeToken, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", FacadeToken{}, err
	}
	tokenValue := "ac_llm_" + hex.EncodeToString(raw)
	hash, fingerprint := HashFacadeToken(tokenValue)
	now := time.Now().UTC()
	return tokenValue, FacadeToken{
		SessionID:        strings.TrimSpace(sessionID),
		TokenHash:        hash,
		TokenFingerprint: fingerprint,
		Model:            strings.TrimSpace(model),
		ProviderID:       strings.TrimSpace(providerID),
		WireAPI:          NormalizeWireAPI(wireAPI),
		Source:           strings.TrimSpace(source),
		RunID:            strings.TrimSpace(runID),
		IssuedAt:         now,
	}, nil
}

func HashFacadeToken(value string) (string, string) {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	hash := hex.EncodeToString(sum[:])
	if len(hash) < 12 {
		return hash, hash
	}
	return hash, hash[:12]
}
