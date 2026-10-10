package configstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/credentials"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func newCredentialStore(t *testing.T) (*ConfigStore, context.Context) {
	t.Helper()
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	return store, ctx
}

func mintTestHandle(t *testing.T, now time.Time) (string, credentials.Handle) {
	t.Helper()
	token, handle, err := credentials.NewHandle(credentials.NewHandleRequest{
		Kind:      credentials.KindGit,
		EnvName:   "GIT_TOKEN",
		SandboxID: "sbx-1",
		RunID:     "run-1",
		Scope: credentials.Scope{
			Endpoint: "git.example.com",
			Owners: []credentials.Owner{
				{Kind: "sandbox", ID: "sbx-1"},
				{Kind: "run", ID: "run-1"},
			},
		},
	}, now)
	if err != nil {
		t.Fatalf("mint handle: %v", err)
	}
	return token, handle
}

func TestCredentialHandleStoreRoundTrip(t *testing.T) {
	store, ctx := newCredentialStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	token, handle := mintTestHandle(t, now)
	if err := store.SaveCredentialHandle(ctx, handle); err != nil {
		t.Fatalf("SaveCredentialHandle() error = %v", err)
	}

	byToken, err := store.GetCredentialHandle(ctx, token)
	if err != nil {
		t.Fatalf("GetCredentialHandle() error = %v", err)
	}
	if byToken.ID != handle.ID || byToken.TokenHash != handle.TokenHash {
		t.Fatalf("GetCredentialHandle() = %#v, want the saved handle", byToken)
	}
	if !byToken.IssuedAt.Equal(handle.IssuedAt) || !byToken.ExpiresAt.Equal(handle.ExpiresAt) {
		t.Fatalf("GetCredentialHandle() times = %s/%s, want %s/%s", byToken.IssuedAt, byToken.ExpiresAt, handle.IssuedAt, handle.ExpiresAt)
	}
	if len(byToken.Scope.Owners) != 2 || byToken.Scope.Endpoint != "git.example.com" {
		t.Fatalf("GetCredentialHandle() scope = %#v, want the persisted scope", byToken.Scope)
	}

	byID, err := store.GetCredentialHandleByID(ctx, handle.ID)
	if err != nil {
		t.Fatalf("GetCredentialHandleByID() error = %v", err)
	}
	if byID.TokenHash != handle.TokenHash {
		t.Fatalf("GetCredentialHandleByID() hash = %q, want %q", byID.TokenHash, handle.TokenHash)
	}
}

func TestCredentialHandleStoreNeverStoresTheRawToken(t *testing.T) {
	store, ctx := newCredentialStore(t)
	token, handle := mintTestHandle(t, time.Now().UTC())
	if err := store.SaveCredentialHandle(ctx, handle); err != nil {
		t.Fatalf("SaveCredentialHandle() error = %v", err)
	}

	rows, err := store.DB().QueryContext(ctx, `SELECT handle_id, kind, token_hash, token_fingerprint, env_name, sandbox_id, run_id, endpoint, owners_json FROM credential_handle`)
	if err != nil {
		t.Fatalf("query credential_handle: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		dest := make([]any, 9)
		values := make([]string, 9)
		for i := range dest {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatalf("scan credential_handle: %v", err)
		}
		for i, value := range values {
			if strings.Contains(value, token) {
				t.Fatalf("column %d stored the raw credential token", i)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate credential_handle: %v", err)
	}

	var matches int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(1) FROM credential_handle WHERE token_hash = ?`, token).Scan(&matches); err != nil {
		t.Fatalf("count raw-token rows: %v", err)
	}
	if matches != 0 {
		t.Fatalf("token_hash matched the raw token %d times; the store must persist only the hash", matches)
	}
}

func TestCredentialHandleStoreRevocationTakesEffect(t *testing.T) {
	store, ctx := newCredentialStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	token, handle := mintTestHandle(t, now)
	if err := store.SaveCredentialHandle(ctx, handle); err != nil {
		t.Fatalf("SaveCredentialHandle() error = %v", err)
	}

	if err := store.RevokeCredentialHandle(ctx, token); err != nil {
		t.Fatalf("RevokeCredentialHandle() error = %v", err)
	}
	revoked, err := store.GetCredentialHandle(ctx, token)
	if err != nil {
		t.Fatalf("GetCredentialHandle() after revoke error = %v", err)
	}
	if revoked.RevokedAt.IsZero() {
		t.Fatal("revocation must be recorded on the handle")
	}
	if _, err := revoked.Authorize(credentials.Request{
		Endpoint: "git.example.com",
		Owners:   []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}},
	}, now); !errors.Is(err, credentials.ErrRevoked) {
		t.Fatalf("Authorize() revoked persisted handle error = %v, want ErrRevoked", err)
	}

	// Revocation is idempotent and keeps the original instant.
	firstRevokedAt := revoked.RevokedAt
	if err := store.RevokeCredentialHandleByID(ctx, handle.ID); err != nil {
		t.Fatalf("RevokeCredentialHandleByID() error = %v", err)
	}
	again, err := store.GetCredentialHandleByID(ctx, handle.ID)
	if err != nil {
		t.Fatalf("GetCredentialHandleByID() error = %v", err)
	}
	if !again.RevokedAt.Equal(firstRevokedAt) {
		t.Fatalf("second revocation changed the instant from %s to %s", firstRevokedAt, again.RevokedAt)
	}
}

func TestCredentialHandleStoreSandboxSweepRevokesEveryHandle(t *testing.T) {
	store, ctx := newCredentialStore(t)
	now := time.Now().UTC()
	for _, envName := range []string{"GIT_TOKEN", "MCP_TOKEN"} {
		_, handle, err := credentials.NewHandle(credentials.NewHandleRequest{
			Kind:      credentials.KindGit,
			EnvName:   envName,
			SandboxID: "sbx-2",
			Scope: credentials.Scope{
				Endpoint: "git.example.com",
				Owners:   []credentials.Owner{{Kind: "sandbox", ID: "sbx-2"}},
			},
		}, now)
		if err != nil {
			t.Fatalf("mint handle: %v", err)
		}
		if err := store.SaveCredentialHandle(ctx, handle); err != nil {
			t.Fatalf("SaveCredentialHandle() error = %v", err)
		}
	}
	if err := store.RevokeCredentialHandlesForSandbox(ctx, "sbx-2"); err != nil {
		t.Fatalf("RevokeCredentialHandlesForSandbox() error = %v", err)
	}
	handles, err := store.ListCredentialHandlesForSandbox(ctx, "sbx-2")
	if err != nil {
		t.Fatalf("ListCredentialHandlesForSandbox() error = %v", err)
	}
	if len(handles) != 2 {
		t.Fatalf("ListCredentialHandlesForSandbox() = %d handles, want 2", len(handles))
	}
	for _, handle := range handles {
		if handle.RevokedAt.IsZero() {
			t.Fatalf("handle %s was not revoked by the sandbox sweep", handle.ID)
		}
	}
}

func TestCredentialHandleStoreReportsNotFound(t *testing.T) {
	store, ctx := newCredentialStore(t)
	if _, err := store.GetCredentialHandle(ctx, "ac_cred_missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetCredentialHandle() missing error = %v, want ErrNotFound", err)
	}
	if err := store.RevokeCredentialHandle(ctx, "ac_cred_missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("RevokeCredentialHandle() missing error = %v, want ErrNotFound", err)
	}
}

func TestCredentialHandleStoreRejectsUnattributableHandle(t *testing.T) {
	store, ctx := newCredentialStore(t)
	_, handle := mintTestHandle(t, time.Now().UTC())
	handle.Scope.Owners = nil
	if err := store.SaveCredentialHandle(ctx, handle); !errors.Is(err, credentials.ErrUnattributable) {
		t.Fatalf("SaveCredentialHandle() unattributable error = %v, want ErrUnattributable", err)
	}
}

// TestCredentialHandleStorePrunesDeadHandlesOnSandboxSweep pins the bounded
// retention and, specifically, that the grace window applies to every way a
// handle can die. The common case here is expiry, not revocation, because
// DefaultHandleTTL is 15 minutes: a handle that ran out 30 minutes ago must
// still be answerable, and only one that has been dead for longer than
// CredentialHandleRetention is removed.
func TestCredentialHandleStorePrunesDeadHandlesOnSandboxSweep(t *testing.T) {
	store, ctx := newCredentialStore(t)
	now := time.Now().UTC()
	mint := func(envName string, issuedAt time.Time) credentials.Handle {
		t.Helper()
		_, handle, err := credentials.NewHandle(credentials.NewHandleRequest{
			Kind:      credentials.KindGit,
			EnvName:   envName,
			SandboxID: "sbx-prune",
			Scope: credentials.Scope{
				Endpoint: "git.example.com",
				Owners:   []credentials.Owner{{Kind: "sandbox", ID: "sbx-prune"}},
			},
		}, issuedAt)
		if err != nil {
			t.Fatalf("mint handle: %v", err)
		}
		if err := store.SaveCredentialHandle(ctx, handle); err != nil {
			t.Fatalf("save handle: %v", err)
		}
		return handle
	}
	// Default 15m TTL throughout: dead 1h45m ago, 30m ago, and not at all.
	expiredBeyondRetention := mint("GIT_TOKEN_OLD", now.Add(-2*time.Hour))
	expiredWithinRetention := mint("GIT_TOKEN_RECENT", now.Add(-45*time.Minute))
	live := mint("GIT_TOKEN_LIVE", now)

	if err := store.RevokeCredentialHandlesForSandbox(ctx, "sbx-prune"); err != nil {
		t.Fatalf("RevokeCredentialHandlesForSandbox() error = %v", err)
	}

	if _, err := store.GetCredentialHandleByID(ctx, expiredBeyondRetention.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("long-dead handle lookup error = %v, want ErrNotFound after the sweep pruned it", err)
	}
	kept, err := store.GetCredentialHandleByID(ctx, expiredWithinRetention.ID)
	if err != nil {
		t.Fatalf("recently-expired handle lookup error = %v, want the grace window to keep it", err)
	}
	if kept.RevokedAt.IsZero() {
		t.Fatal("the sweep must mark a recently-expired handle revoked, not only retain it")
	}
	got, err := store.GetCredentialHandleByID(ctx, live.ID)
	if err != nil {
		t.Fatalf("live handle lookup error = %v, want the handle to survive the prune", err)
	}
	if got.RevokedAt.IsZero() {
		t.Fatal("the sweep must still revoke a live handle before the retention window passes")
	}
}
