package llms

import (
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/credentials"
)

// FacadeCredentialEnvName is the environment variable a guest sees for the LLM
// credential. The value there is the injected placeholder, never the upstream
// key: the daemon holds the key and the facade substitutes it.
const FacadeCredentialEnvName = "LLM_API_KEY"

// CredentialHandle maps the LLM facade specialization onto the generic
// credential-handle model.
//
// FacadeToken keeps its LLM-specific behavior (model resolution, wire API
// normalization) and this method is the single boundary where it becomes a
// generic handle. The hash is already the SHA-256 the generic HashedToken uses,
// so identity is preserved without recomputation.
//
// A token with no provider, or with neither a sandbox nor a run, cannot be
// attributed or scoped, so it maps to an error instead of a handle with empty
// fields. That is the same denial the handle model applies to every credential:
// no owner metadata means no grant.
//
// Lifetime is the other hard boundary. A handle is a bounded, non-renewable
// lease (credentials.MaxHandleTTL, and deliberately no renew operation), whereas
// NewFacadeToken mints a token with no ExpiresAt at all: the production facade
// token never expires and is ended only by explicit revocation, so it keeps its
// own endpoint-scoped model and is not routed through the handle mechanism. That
// is a decision about the tokens this daemon mints, not about this method: a
// facade token that does carry a finite lifetime within the cap still maps to a
// handle here, and only a never-expiring token (refused with
// credentials.ErrHandleLifetimeUnsupported) or one whose finite lifetime exceeds
// the cap is refused, for the same reason a mint request would be.
func (t FacadeToken) CredentialHandle() (credentials.Handle, error) {
	providerID := strings.TrimSpace(t.ProviderID)
	if providerID == "" {
		return credentials.Handle{}, fmt.Errorf("map facade token to credential handle: provider id is required to scope the credential")
	}
	owners := make([]credentials.Owner, 0, 2)
	if sandboxID := strings.TrimSpace(t.SandboxID); sandboxID != "" {
		owners = append(owners, credentials.Owner{Kind: "sandbox", ID: sandboxID})
	}
	if runID := strings.TrimSpace(t.RunID); runID != "" {
		owners = append(owners, credentials.Owner{Kind: "run", ID: runID})
	}
	if len(owners) == 0 {
		return credentials.Handle{}, fmt.Errorf("map facade token to credential handle: sandbox or run owner is required")
	}
	handle := credentials.Handle{
		ID:               "llm_" + strings.TrimSpace(t.TokenHash),
		Kind:             credentials.KindLLMFacade,
		TokenHash:        strings.TrimSpace(t.TokenHash),
		TokenFingerprint: strings.TrimSpace(t.TokenFingerprint),
		EnvName:          FacadeCredentialEnvName,
		SandboxID:        strings.TrimSpace(t.SandboxID),
		RunID:            strings.TrimSpace(t.RunID),
		Scope: credentials.Scope{
			Endpoint: providerID,
			Owners:   owners,
		},
		IssuedAt:  t.IssuedAt,
		ExpiresAt: t.ExpiresAt,
		RevokedAt: t.RevokedAt,
	}
	if err := facadeHandleLifetimeError(t); err != nil {
		return credentials.Handle{}, fmt.Errorf("map facade token to credential handle: %w", err)
	}
	if err := handle.Validate(); err != nil {
		return credentials.Handle{}, fmt.Errorf("map facade token to credential handle: %w", err)
	}
	return handle.Normalized(), nil
}

// facadeHandleLifetimeError decides whether a facade token's lifetime fits the
// handle model. Handle.Validate would reject a never-expiring token too, but
// only incidentally, as an expiry-after-issuance violation, which hides the
// design rule; stating it here keeps the denial diagnosable and applies the
// same cap that minting enforces. A missing issuance time is left to Validate,
// which names the missing field instead of a misleading lifetime problem.
func facadeHandleLifetimeError(t FacadeToken) error {
	if t.IssuedAt.IsZero() {
		return nil
	}
	if t.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: a facade token with no expiry is intentionally not representable as a credential handle; handles are bounded, non-renewable leases, while the facade token keeps its own endpoint-scoped, explicitly-revocable lifetime", credentials.ErrHandleLifetimeUnsupported)
	}
	if lifetime := t.ExpiresAt.Sub(t.IssuedAt); lifetime > credentials.MaxHandleTTL {
		return fmt.Errorf("%w: token lifetime %s exceeds the credential handle maximum %s", credentials.ErrInvalidHandle, lifetime, credentials.MaxHandleTTL)
	}
	return nil
}
