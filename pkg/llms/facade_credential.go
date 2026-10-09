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
	if err := handle.Validate(); err != nil {
		return credentials.Handle{}, fmt.Errorf("map facade token to credential handle: %w", err)
	}
	return handle.Normalized(), nil
}
