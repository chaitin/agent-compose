package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type serviceStoreFake struct {
	initialized bool
	created     CreateTokenInput
}

func (s *serviceStoreFake) ReconcileEnvironmentToken(context.Context, string, time.Time) (Token, bool, error) {
	return Token{}, false, nil
}
func (s *serviceStoreFake) AuthInitialized(context.Context) (bool, error) { return s.initialized, nil }
func (s *serviceStoreFake) AuthenticateTokenHash(context.Context, string) (Token, error) {
	return Token{}, ErrInvalidToken
}
func (s *serviceStoreFake) ListTokens(context.Context, TokenListOptions) (TokenListResult, error) {
	return TokenListResult{}, nil
}
func (s *serviceStoreFake) CreateToken(_ context.Context, actor Identity, input CreateTokenInput, _, _ string, now time.Time) (Token, bool, bool, error) {
	s.created = input
	return Token{ID: "token-1", Name: input.Name, Role: input.Role, CreatedByTokenID: actor.TokenID, CreatedAt: now}, true, false, nil
}
func (s *serviceStoreFake) RevokeToken(context.Context, Identity, string, string, string, time.Time) (Token, bool, bool, error) {
	return Token{}, false, false, nil
}
func (s *serviceStoreFake) BeginAudit(context.Context, Audit) error  { return nil }
func (s *serviceStoreFake) FinishAudit(context.Context, Audit) error { return nil }
func (s *serviceStoreFake) ListAudits(context.Context, AuditFilter) (AuditListResult, error) {
	return AuditListResult{}, nil
}

func TestCreateTokenRequiresAdminAndClientGeneratedSecret(t *testing.T) {
	store := &serviceStoreFake{}
	service, err := NewService(context.Background(), store, "")
	if err != nil {
		t.Fatal(err)
	}
	valid := CreateTokenInput{Name: "reader", Role: RoleReadOnlyAdmin, Plaintext: "ac_0123456789012345678901234567890123456789012", ClientRequestID: "request-1"}
	if _, _, _, err := service.CreateToken(context.Background(), Identity{Role: RoleReadOnlyAdmin}, valid, ""); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("read-only create error = %v, want permission denied", err)
	}
	invalid := valid
	invalid.Plaintext = "server-please-generate"
	if _, _, _, err := service.CreateToken(context.Background(), Identity{Role: RoleAdmin}, invalid, ""); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("invalid secret error = %v, want invalid token", err)
	}
	item, created, replay, err := service.CreateToken(context.Background(), Identity{TokenID: "admin-1", Role: RoleAdmin}, valid, "request-id")
	if err != nil || !created || replay || item.Name != valid.Name || store.created.Plaintext != valid.Plaintext {
		t.Fatalf("create result = %#v, %v, %v, %v", item, created, replay, err)
	}
}

func TestAllowedRoleMatrix(t *testing.T) {
	if !Allowed(RoleAdmin, AccessRead) || !Allowed(RoleAdmin, AccessOperation) || !Allowed(RoleReadOnlyAdmin, AccessRead) || Allowed(RoleReadOnlyAdmin, AccessOperation) {
		t.Fatal("role access matrix is incorrect")
	}
}

func TestRequestRegistryCancelsOnlyMatchingToken(t *testing.T) {
	var registry requestRegistry
	first, second := false, false
	unregister := registry.register("token-1", func() { first = true })
	registry.register("token-2", func() { second = true })
	registry.cancelToken("token-1")
	if !first || second {
		t.Fatalf("cancellation state = %v/%v", first, second)
	}
	unregister()
}

func TestSanitizeErrorRedactsFromEarliestCredentialMarker(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		secrets []string
	}{
		{name: "repeated bearer", input: "failed: Bearer first-secret, retry with bearer second-secret", secrets: []string{"first-secret", "second-secret"}},
		{name: "authorization before bearer", input: "proxy Authorization: Bearer auth-secret; upstream Bearer upstream-secret", secrets: []string{"auth-secret", "upstream-secret"}},
		{name: "mixed case", input: "failed: bEaReR mixed-secret", secrets: []string{"mixed-secret"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := SanitizeError(test.input)
			if !strings.Contains(got, "[REDACTED]") {
				t.Fatalf("SanitizeError(%q) = %q, want redaction marker", test.input, got)
			}
			for _, secret := range test.secrets {
				if strings.Contains(got, secret) {
					t.Fatalf("SanitizeError(%q) leaked %q in %q", test.input, secret, got)
				}
			}
		})
	}
}
