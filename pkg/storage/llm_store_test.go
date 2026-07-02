package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestLLMFacadeTokenStoreRevokePrunesDeadRows(t *testing.T) {
	store := newTestConfigStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	activeVal := "raw-active"
	active := testLLMFacadeToken(activeVal, "sess-x")
	if err := store.SaveLLMFacadeToken(ctx, active); err != nil {
		t.Fatalf("save active: %v", err)
	}

	oldVal := "raw-old"
	old := testLLMFacadeToken(oldVal, "sess-y")
	old.RevokedAt = now.Add(-2 * LLMFacadeTokenRetention)
	if err := store.SaveLLMFacadeToken(ctx, old); err != nil {
		t.Fatalf("save old: %v", err)
	}

	expVal := "raw-expired"
	exp := testLLMFacadeToken(expVal, "sess-z")
	exp.ExpiresAt = now.Add(-time.Minute)
	if err := store.SaveLLMFacadeToken(ctx, exp); err != nil {
		t.Fatalf("save expired: %v", err)
	}

	if err := store.RevokeLLMFacadeTokensForSession(ctx, "sess-x"); err != nil {
		t.Fatalf("RevokeLLMFacadeTokensForSession: %v", err)
	}

	got, err := store.GetLLMFacadeToken(ctx, activeVal)
	if err != nil {
		t.Fatalf("active token should remain: %v", err)
	}
	if got.RevokedAt.IsZero() {
		t.Fatalf("active token should be marked revoked")
	}
	if _, err := store.GetLLMFacadeToken(ctx, oldVal); err == nil {
		t.Fatalf("long-revoked token should be pruned")
	}
	if _, err := store.GetLLMFacadeToken(ctx, expVal); err == nil {
		t.Fatalf("expired token should be pruned")
	}
}

func TestLLMFacadeTokenStoreDelete(t *testing.T) {
	store := newTestConfigStore(t)
	ctx := context.Background()
	raw := "raw-delete"
	if err := store.SaveLLMFacadeToken(ctx, testLLMFacadeToken(raw, "sess")); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := store.GetLLMFacadeToken(ctx, raw); err != nil {
		t.Fatalf("token should exist before delete: %v", err)
	}
	if err := store.DeleteLLMFacadeToken(ctx, raw); err != nil {
		t.Fatalf("DeleteLLMFacadeToken: %v", err)
	}
	if _, err := store.GetLLMFacadeToken(ctx, raw); err == nil {
		t.Fatalf("token should be gone after delete")
	}
	if err := store.DeleteLLMFacadeToken(ctx, ""); err != nil {
		t.Fatalf("deleting empty token should be a no-op: %v", err)
	}
}

func testLLMFacadeToken(raw, sessionID string) LLMFacadeToken {
	hash, fingerprint := testLLMFacadeTokenHash(raw)
	return LLMFacadeToken{
		SessionID:        sessionID,
		TokenHash:        hash,
		TokenFingerprint: fingerprint,
		Model:            "m",
		ProviderID:       "default",
		WireAPI:          llmAPIProtocolResponses,
		Source:           "agent",
		RunID:            "run-1",
		IssuedAt:         time.Now().UTC(),
	}
}

func testLLMFacadeTokenHash(value string) (string, string) {
	sum := sha256.Sum256([]byte(value))
	hash := hex.EncodeToString(sum[:])
	if len(hash) < 12 {
		return hash, hash
	}
	return hash, hash[:12]
}
