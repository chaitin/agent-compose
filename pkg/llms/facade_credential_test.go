package llms

import (
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
	if err := handle.Validate(); err != nil {
		t.Fatalf("mapped handle must validate: %v", err)
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
