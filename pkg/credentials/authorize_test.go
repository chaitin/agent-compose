package credentials_test

import (
	"errors"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/credentials"
)

func TestAuthorizeGrantsInScopeUse(t *testing.T) {
	handle := baseHandle()
	authorization, err := handle.Authorize(credentials.Request{
		Endpoint: "git.example.com",
		Owners:   []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}},
	}, testNow)
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if authorization.HandleID != handle.ID || authorization.Endpoint != "git.example.com" {
		t.Fatalf("Authorization = %#v, want the handle and its endpoint", authorization)
	}
	if len(authorization.Owners) != 1 || authorization.Owners[0].ID != "sbx-1" {
		t.Fatalf("Authorization owners = %#v, want the sandbox owner", authorization.Owners)
	}
}

func TestAuthorizeExpiryIsExclusiveAtTheDeadline(t *testing.T) {
	handle := baseHandle()
	request := credentials.Request{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}}}

	if _, err := handle.Authorize(request, handle.ExpiresAt.Add(-time.Nanosecond)); err != nil {
		t.Fatalf("Authorize() just before expiry error = %v, want nil", err)
	}
	if _, err := handle.Authorize(request, handle.ExpiresAt); !errors.Is(err, credentials.ErrExpired) {
		t.Fatalf("Authorize() at the deadline error = %v, want ErrExpired", err)
	}
	if _, err := handle.Authorize(request, handle.ExpiresAt.Add(time.Hour)); !errors.Is(err, credentials.ErrExpired) {
		t.Fatalf("Authorize() after expiry error = %v, want ErrExpired", err)
	}
}

func TestAuthorizeRevocationTakesEffect(t *testing.T) {
	handle := baseHandle()
	handle.RevokedAt = testNow.Add(time.Minute)
	request := credentials.Request{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}}}
	if _, err := handle.Authorize(request, testNow); !errors.Is(err, credentials.ErrRevoked) {
		t.Fatalf("Authorize() revoked handle error = %v, want ErrRevoked", err)
	}
	if handle.Active(testNow) {
		t.Fatal("a revoked handle must not be active")
	}
}

func TestAuthorizeDeniesWithoutOwnerMetadata(t *testing.T) {
	handle := baseHandle()
	for _, owners := range [][]credentials.Owner{nil, {}, {{Kind: "sandbox"}}, {{ID: "sbx-1"}}} {
		if _, err := handle.Authorize(credentials.Request{Endpoint: "git.example.com", Owners: owners}, testNow); !errors.Is(err, credentials.ErrUnattributable) {
			t.Fatalf("Authorize() owners %#v error = %v, want ErrUnattributable", owners, err)
		}
	}
}

// TestAuthorizeForwardRightIsNotCredentialRight pins the invariant that an
// egress allow for one target never becomes credential grounds for another.
// The handle is scoped to git.example.com, so every other target — including a
// plausible sibling host — is out of scope even though an egress policy might
// have allowed it.
func TestAuthorizeForwardRightIsNotCredentialRight(t *testing.T) {
	handle := baseHandle()
	for _, endpoint := range []string{"registry.example.com", "git.example.com.evil.test", "example.com"} {
		_, err := handle.Authorize(credentials.Request{
			Endpoint: endpoint,
			Owners:   []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}},
		}, testNow)
		if !errors.Is(err, credentials.ErrEndpointOutOfScope) {
			t.Fatalf("Authorize() endpoint %q error = %v, want ErrEndpointOutOfScope", endpoint, err)
		}
	}
}

func TestAuthorizeDeniesOwnerNotGranted(t *testing.T) {
	handle := baseHandle()
	_, err := handle.Authorize(credentials.Request{
		Endpoint: "git.example.com",
		Owners:   []credentials.Owner{{Kind: "sandbox", ID: "sbx-other"}},
	}, testNow)
	if !errors.Is(err, credentials.ErrOwnerNotGranted) {
		t.Fatalf("Authorize() unlisted owner error = %v, want ErrOwnerNotGranted", err)
	}
	var scopeErr *credentials.ScopeError
	if !errors.As(err, &scopeErr) {
		t.Fatalf("Authorize() error = %v, want a *ScopeError", err)
	}
	if scopeErr.HandleID != handle.ID {
		t.Fatalf("ScopeError.HandleID = %q, want %q", scopeErr.HandleID, handle.ID)
	}
}

func TestAuthorizeBatchUsesOwnerIntersection(t *testing.T) {
	handle := baseHandle()
	authorization, err := handle.AuthorizeBatch([]credentials.Request{
		{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}, {Kind: "run", ID: "run-1"}}},
		{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "run", ID: "run-1"}}},
	}, testNow)
	if err != nil {
		t.Fatalf("AuthorizeBatch() error = %v", err)
	}
	if len(authorization.Owners) != 1 || authorization.Owners[0] != (credentials.Owner{Kind: "run", ID: "run-1"}) {
		t.Fatalf("AuthorizeBatch() owners = %#v, want only the intersected run owner", authorization.Owners)
	}
}

func TestAuthorizeBatchDeniesDisjointAndEmptyOwnerSets(t *testing.T) {
	handle := baseHandle()
	tests := []struct {
		name string
		reqs []credentials.Request
	}{
		{name: "no requests", reqs: nil},
		{
			name: "disjoint owners",
			reqs: []credentials.Request{
				{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}}},
				{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "run", ID: "run-1"}}},
			},
		},
		{
			name: "one sub-call has no owner",
			reqs: []credentials.Request{
				{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}}},
				{Endpoint: "git.example.com"},
			},
		},
		{
			name: "one sub-call leaves the endpoint scope",
			reqs: []credentials.Request{
				{Endpoint: "git.example.com", Owners: []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}}},
				{Endpoint: "registry.example.com", Owners: []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := handle.AuthorizeBatch(test.reqs, testNow); err == nil {
				t.Fatal("AuthorizeBatch() must deny an empty intersection or out-of-scope sub-call")
			}
		})
	}
}

func TestIntersectOwnersNormalizesAndRejectsMissingMetadata(t *testing.T) {
	got := credentials.IntersectOwners([]credentials.Request{
		{Owners: []credentials.Owner{{Kind: " run ", ID: "run-1"}, {Kind: "sandbox", ID: "sbx-1"}}},
		{Owners: []credentials.Owner{{Kind: "run", ID: "run-1"}}},
	})
	if len(got) != 1 || got[0] != (credentials.Owner{Kind: "run", ID: "run-1"}) {
		t.Fatalf("IntersectOwners() = %#v, want the normalized run owner", got)
	}
	if got := credentials.IntersectOwners([]credentials.Request{{Owners: []credentials.Owner{{Kind: "run", ID: "run-1"}}}, {}}); got != nil {
		t.Fatalf("IntersectOwners() with missing metadata = %#v, want nil", got)
	}
}
