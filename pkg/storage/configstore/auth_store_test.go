package configstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	controlauth "agent-compose/pkg/auth"
)

func newAuthTestStore(t *testing.T) *ConfigStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store := FromDB(db)
	if err := store.ensureAuthSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestAuthStoreEnvironmentTokenRotationAndNoPlaintext(t *testing.T) {
	ctx := context.Background()
	store := newAuthTestStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	item, changed, err := store.ReconcileEnvironmentToken(ctx, "bootstrap-secret", now)
	if err != nil || !changed || item.Name != controlauth.DefaultAdminName || item.Role != controlauth.RoleAdmin {
		t.Fatalf("initial reconcile = %#v, %v, %v", item, changed, err)
	}
	if _, _, err := store.ReconcileEnvironmentToken(ctx, "rotated-secret", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AuthenticateTokenHash(ctx, controlauth.TokenHash("bootstrap-secret")); !errors.Is(err, controlauth.ErrInvalidToken) {
		t.Fatalf("old token auth error = %v", err)
	}
	if _, err := store.AuthenticateTokenHash(ctx, controlauth.TokenHash("rotated-secret")); err != nil {
		t.Fatalf("rotated token auth error = %v", err)
	}
	var storedHash string
	if err := store.DB().QueryRow(`SELECT token_hash FROM auth_token WHERE id = ?`, item.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(storedHash, "secret") || storedHash == "rotated-secret" {
		t.Fatalf("database stored plaintext-like value %q", storedHash)
	}
}

func TestAuthStoreCreateIdempotencyAndRevocationRules(t *testing.T) {
	ctx := context.Background()
	store := newAuthTestStore(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	environment, _, err := store.ReconcileEnvironmentToken(ctx, "bootstrap", now)
	if err != nil {
		t.Fatal(err)
	}
	actor := controlauth.Identity{TokenID: environment.ID, TokenName: environment.Name, Role: controlauth.RoleAdmin, Origin: controlauth.OriginEnvironment}
	input := controlauth.CreateTokenInput{Name: "reader", Role: controlauth.RoleReadOnlyAdmin, Plaintext: "ac_0123456789012345678901234567890123456789012", ClientRequestID: "create-1"}
	created, ok, replay, err := store.CreateToken(ctx, actor, input, "request-1", `{}`, now)
	if err != nil || !ok || replay {
		t.Fatalf("create = %#v, %v, %v, %v", created, ok, replay, err)
	}
	again, ok, replay, err := store.CreateToken(ctx, actor, input, "request-2", `{}`, now)
	if err != nil || ok || !replay || again.ID != created.ID {
		t.Fatalf("replay = %#v, %v, %v, %v", again, ok, replay, err)
	}
	conflict := input
	conflict.Name = "different"
	if _, _, _, err := store.CreateToken(ctx, actor, conflict, "request-3", `{}`, now); !errors.Is(err, controlauth.ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict error = %v", err)
	}
	if _, _, _, err := store.RevokeToken(ctx, actor, environment.ID, "request-4", `{}`, now); !errors.Is(err, controlauth.ErrTokenSelfRevoke) {
		t.Fatalf("self revoke error = %v", err)
	}
	local := controlauth.Identity{TokenName: "local-admin", Role: controlauth.RoleAdmin, Origin: controlauth.OriginLocal}
	if _, revoked, _, err := store.RevokeToken(ctx, local, created.ID, "request-5", `{}`, now); err != nil || !revoked {
		t.Fatalf("local revoke = %v, %v", revoked, err)
	}
	audits, err := store.ListAudits(ctx, controlauth.AuditFilter{Action: "auth.token.create", Limit: 10})
	if err != nil || len(audits.Audits) != 1 {
		t.Fatalf("create audits = %#v, %v", audits, err)
	}
}
