package llms

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/credentials"
)

func TestFacadeTokenMapsToGenericCredentialHandle(t *testing.T) {
	issuedAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	token := FacadeToken{
		SandboxID:        "sbx-1",
		TokenHash:        "facade-hash",
		TokenFingerprint: "facade-fp",
		ProviderID:       "provider-a",
		Model:            "gpt-test",
		WireAPI:          APIProtocolResponses,
		RunID:            "run-1",
		IssuedAt:         issuedAt,
		ExpiresAt:        issuedAt.Add(10 * time.Minute),
	}
	handle, err := token.CredentialHandle()
	if err != nil {
		t.Fatalf("CredentialHandle() error = %v", err)
	}
	if handle.Kind != credentials.KindLLMFacade {
		t.Fatalf("Kind = %q, want %q", handle.Kind, credentials.KindLLMFacade)
	}
	if handle.TokenHash != token.TokenHash || handle.TokenFingerprint != token.TokenFingerprint {
		t.Fatalf("handle identity = %q/%q, want the token's hash and fingerprint", handle.TokenHash, handle.TokenFingerprint)
	}
	if handle.EnvName != FacadeCredentialEnvName {
		t.Fatalf("EnvName = %q, want %q", handle.EnvName, FacadeCredentialEnvName)
	}
	if handle.Scope.Endpoint != "provider-a" {
		t.Fatalf("Scope.Endpoint = %q, want the provider id", handle.Scope.Endpoint)
	}
	if !handle.IssuedAt.Equal(issuedAt) || !handle.ExpiresAt.Equal(issuedAt.Add(10*time.Minute)) {
		t.Fatalf("handle times = %s/%s, want the token's times", handle.IssuedAt, handle.ExpiresAt)
	}
	if len(handle.Scope.Owners) != 2 {
		t.Fatalf("Scope.Owners = %#v, want the sandbox and run owners", handle.Scope.Owners)
	}
	// Owners are canonicalized (sorted by kind) by the handle model, so assert
	// the exact attribution rather than only the count.
	wantOwners := []credentials.Owner{
		{Kind: "run", ID: "run-1"},
		{Kind: "sandbox", ID: "sbx-1"},
	}
	if !slices.Equal(handle.Scope.Owners, wantOwners) {
		t.Fatalf("Scope.Owners = %#v, want %#v", handle.Scope.Owners, wantOwners)
	}
	if err := handle.Validate(); err != nil {
		t.Fatalf("mapped handle must validate: %v", err)
	}
}

func TestFacadeTokenAtMaxHandleLifetimeMaps(t *testing.T) {
	issuedAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	token := FacadeToken{
		SandboxID:  "sbx-1",
		TokenHash:  "facade-hash",
		ProviderID: "provider-a",
		IssuedAt:   issuedAt,
		// Exactly the cap is representable; only a lifetime beyond it is not.
		ExpiresAt: issuedAt.Add(credentials.MaxHandleTTL),
	}
	handle, err := token.CredentialHandle()
	if err != nil {
		t.Fatalf("CredentialHandle() error = %v, want a token at the cap to map", err)
	}
	if handle.Kind != credentials.KindLLMFacade {
		t.Fatalf("Kind = %q, want %q", handle.Kind, credentials.KindLLMFacade)
	}
	if handle.EnvName != FacadeCredentialEnvName {
		t.Fatalf("EnvName = %q, want %q", handle.EnvName, FacadeCredentialEnvName)
	}
	if handle.Scope.Endpoint != "provider-a" {
		t.Fatalf("Scope.Endpoint = %q, want the provider id", handle.Scope.Endpoint)
	}
	wantOwners := []credentials.Owner{{Kind: "sandbox", ID: "sbx-1"}}
	if !slices.Equal(handle.Scope.Owners, wantOwners) {
		t.Fatalf("Scope.Owners = %#v, want %#v", handle.Scope.Owners, wantOwners)
	}
}

func TestFacadeTokenWithoutExpiryIsRefusedByDesign(t *testing.T) {
	// NewFacadeToken never sets ExpiresAt, so this is the production shape: a
	// token that only ever ends by explicit revocation. It must be refused
	// because a handle is a bounded, non-renewable lease, not merely because
	// its expiry ordering failed generic handle validation.
	token := FacadeToken{
		SandboxID:  "sbx-1",
		TokenHash:  "facade-hash",
		ProviderID: "provider-a",
		IssuedAt:   time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
	}
	handle, err := token.CredentialHandle()
	if err == nil {
		t.Fatalf("a never-expiring facade token must not map, got %#v", handle)
	}
	if !errors.Is(err, credentials.ErrHandleLifetimeUnsupported) {
		t.Fatalf("error = %v, want errors.Is(..., ErrHandleLifetimeUnsupported)", err)
	}
	if errors.Is(err, credentials.ErrInvalidHandle) {
		t.Fatalf("error = %v, want the lifetime rule rather than generic handle validation", err)
	}
	if !strings.Contains(err.Error(), "no expiry") {
		t.Fatalf("error = %v, want it to name the missing expiry", err)
	}
	if handle.ID != "" || handle.Kind != "" {
		t.Fatalf("refused mapping returned %#v, want the zero handle", handle)
	}
}

func TestFacadeTokenLifetimeBeyondHandleMaximumIsRefused(t *testing.T) {
	issuedAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	token := FacadeToken{
		SandboxID:  "sbx-1",
		TokenHash:  "facade-hash",
		ProviderID: "provider-a",
		IssuedAt:   issuedAt,
		ExpiresAt:  issuedAt.Add(credentials.MaxHandleTTL + time.Second),
	}
	_, err := token.CredentialHandle()
	if err == nil {
		t.Fatal("a facade token beyond the handle maximum must not map")
	}
	if !errors.Is(err, credentials.ErrInvalidHandle) {
		t.Fatalf("error = %v, want errors.Is(..., ErrInvalidHandle)", err)
	}
	if !strings.Contains(err.Error(), credentials.MaxHandleTTL.String()) {
		t.Fatalf("error = %v, want it to report the maximum %s", err, credentials.MaxHandleTTL)
	}
}

func TestFacadeTokenWithoutScopeOrOwnerDoesNotMap(t *testing.T) {
	issuedAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	base := FacadeToken{
		SandboxID:  "sbx-1",
		TokenHash:  "facade-hash",
		ProviderID: "provider-a",
		IssuedAt:   issuedAt,
		ExpiresAt:  issuedAt.Add(time.Minute),
	}

	noProvider := base
	noProvider.ProviderID = ""
	if _, err := noProvider.CredentialHandle(); err == nil {
		t.Fatal("a token with no provider cannot be scoped and must not map")
	}

	noOwner := base
	noOwner.SandboxID = ""
	noOwner.RunID = ""
	if _, err := noOwner.CredentialHandle(); err == nil {
		t.Fatal("a token with no owner cannot be attributed and must not map")
	}
}
