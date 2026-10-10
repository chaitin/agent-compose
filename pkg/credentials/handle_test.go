package credentials_test

import (
	"errors"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/credentials"
)

var testNow = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func baseHandle() credentials.Handle {
	return credentials.Handle{
		ID:               "cred_test",
		Kind:             credentials.KindGit,
		TokenHash:        "hash-value",
		TokenFingerprint: "fingerprint",
		EnvName:          "GIT_TOKEN",
		SandboxID:        "sbx-1",
		RunID:            "run-1",
		Scope: credentials.Scope{
			Endpoint: "git.example.com",
			Owners: []credentials.Owner{
				{Kind: "sandbox", ID: "sbx-1"},
				{Kind: "run", ID: "run-1"},
			},
		},
		IssuedAt:  testNow,
		ExpiresAt: testNow.Add(10 * time.Minute),
	}
}

func TestHandleValidateRejectsUnattributableAndIncompleteHandles(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*credentials.Handle)
		want   error
	}{
		{
			name:   "empty owner list is unattributable",
			mutate: func(h *credentials.Handle) { h.Scope.Owners = nil },
			want:   credentials.ErrUnattributable,
		},
		{
			name:   "owner with missing id is unattributable",
			mutate: func(h *credentials.Handle) { h.Scope.Owners = []credentials.Owner{{Kind: "sandbox"}} },
			want:   credentials.ErrUnattributable,
		},
		{
			name:   "missing endpoint",
			mutate: func(h *credentials.Handle) { h.Scope.Endpoint = "" },
			want:   credentials.ErrInvalidHandle,
		},
		{
			name:   "missing env name",
			mutate: func(h *credentials.Handle) { h.EnvName = "" },
			want:   credentials.ErrInvalidHandle,
		},
		{
			name:   "unknown kind",
			mutate: func(h *credentials.Handle) { h.Kind = credentials.Kind("not-a-kind") },
			want:   credentials.ErrInvalidHandle,
		},
		{
			name:   "expiry not after issuance",
			mutate: func(h *credentials.Handle) { h.ExpiresAt = h.IssuedAt },
			want:   credentials.ErrInvalidHandle,
		},
		{
			name:   "lifetime beyond the handle maximum",
			mutate: func(h *credentials.Handle) { h.ExpiresAt = h.IssuedAt.Add(credentials.MaxHandleTTL + time.Second) },
			want:   credentials.ErrInvalidHandle,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handle := baseHandle()
			test.mutate(&handle)
			err := handle.Validate()
			if !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
		})
	}
	if err := baseHandle().Validate(); err != nil {
		t.Fatalf("base handle must be valid: %v", err)
	}
	// The cap itself is representable; only a lifetime beyond it is refused.
	atCap := baseHandle()
	atCap.ExpiresAt = atCap.IssuedAt.Add(credentials.MaxHandleTTL)
	if err := atCap.Validate(); err != nil {
		t.Fatalf("a handle at the maximum lifetime must validate: %v", err)
	}
}

func TestHandleOwnersAreNormalizedDeterministically(t *testing.T) {
	handle := baseHandle()
	handle.Scope.Owners = []credentials.Owner{
		{Kind: " run ", ID: "run-1"},
		{Kind: "sandbox", ID: "sbx-1"},
		{Kind: "run", ID: "run-1"},
		{Kind: "", ID: "ignored"},
	}
	ownersJSON, err := handle.OwnersJSON()
	if err != nil {
		t.Fatalf("OwnersJSON() error = %v", err)
	}
	const want = `[{"kind":"run","id":"run-1"},{"kind":"sandbox","id":"sbx-1"}]`
	if ownersJSON != want {
		t.Fatalf("OwnersJSON() = %s, want %s", ownersJSON, want)
	}
	parsed, err := credentials.ParseOwners(ownersJSON)
	if err != nil {
		t.Fatalf("ParseOwners() error = %v", err)
	}
	if len(parsed) != 2 || parsed[0].Kind != "run" || parsed[1].Kind != "sandbox" {
		t.Fatalf("ParseOwners() = %#v, want run then sandbox", parsed)
	}
}

func TestParseOwnersRejectsMalformedJSON(t *testing.T) {
	if _, err := credentials.ParseOwners("{not json"); err == nil {
		t.Fatal("ParseOwners() must reject malformed owners JSON")
	}
}

func TestNewHandleMintsShortLivedHashedHandle(t *testing.T) {
	token, handle, err := credentials.NewHandle(credentials.NewHandleRequest{
		Kind:      credentials.KindMCP,
		EnvName:   "MCP_TOKEN",
		SandboxID: "sbx-1",
		Scope: credentials.Scope{
			Endpoint: "mcp.example.com",
			Owners:   []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}},
		},
	}, testNow)
	if err != nil {
		t.Fatalf("NewHandle() error = %v", err)
	}
	hash, fingerprint := credentials.HashToken(token)
	if handle.TokenHash != hash || handle.TokenFingerprint != fingerprint {
		t.Fatalf("handle hash = %q/%q, want %q/%q", handle.TokenHash, handle.TokenFingerprint, hash, fingerprint)
	}
	if handle.TokenHash == token {
		t.Fatal("handle must never store the raw token")
	}
	if !handle.ExpiresAt.Equal(testNow.Add(credentials.DefaultHandleTTL)) {
		t.Fatalf("ExpiresAt = %s, want %s", handle.ExpiresAt, testNow.Add(credentials.DefaultHandleTTL))
	}
	if handle.ExpiresAt.Sub(handle.IssuedAt) > credentials.MaxHandleTTL {
		t.Fatalf("minted handle lifetime %s exceeds the maximum", handle.ExpiresAt.Sub(handle.IssuedAt))
	}
	if len(handle.Scope.Owners) != 1 || handle.Scope.Owners[0].ID != "sbx-1" {
		t.Fatalf("minted handle owners = %#v, want the sandbox owner", handle.Scope.Owners)
	}
}

func TestNewHandleRefusesLongLivedAndUnattributableRequests(t *testing.T) {
	if _, _, err := credentials.NewHandle(credentials.NewHandleRequest{
		Kind:    credentials.KindGit,
		EnvName: "GIT_TOKEN",
		Scope:   credentials.Scope{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}}},
		TTL:     credentials.MaxHandleTTL + time.Second,
	}, testNow); !errors.Is(err, credentials.ErrInvalidHandle) {
		t.Fatalf("NewHandle() long TTL error = %v, want ErrInvalidHandle", err)
	}
	if _, _, err := credentials.NewHandle(credentials.NewHandleRequest{
		Kind:    credentials.KindGit,
		EnvName: "GIT_TOKEN",
		Scope:   credentials.Scope{Endpoint: "git.example.com"},
	}, testNow); !errors.Is(err, credentials.ErrUnattributable) {
		t.Fatalf("NewHandle() empty owner error = %v, want ErrUnattributable", err)
	}
}

func TestNewHandleIsNonRenewableThroughMinting(t *testing.T) {
	request := credentials.NewHandleRequest{
		Kind:    credentials.KindRegistry,
		EnvName: "REGISTRY_TOKEN",
		Scope:   credentials.Scope{Endpoint: "registry.example.com", Owners: []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}}},
		TTL:     time.Minute,
	}
	firstToken, firstHandle, err := credentials.NewHandle(request, testNow)
	if err != nil {
		t.Fatalf("first NewHandle() error = %v", err)
	}
	// Re-minting the same request produces a distinct handle; there is no way to
	// extend the first handle's expiry, which is what "non-renewable" means here.
	secondToken, secondHandle, err := credentials.NewHandle(request, testNow.Add(30*time.Second))
	if err != nil {
		t.Fatalf("second NewHandle() error = %v", err)
	}
	if firstToken == secondToken || firstHandle.TokenHash == secondHandle.TokenHash {
		t.Fatal("each mint must produce a distinct credential")
	}
	if !firstHandle.ExpiresAt.Before(secondHandle.ExpiresAt) {
		t.Fatal("a later mint is a new lease, not an extension of the first")
	}
}
