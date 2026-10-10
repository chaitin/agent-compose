package credentials

import (
	"fmt"
	"strings"
	"time"
)

// PlaceholderPrefix marks the value a sandbox sees in place of a credential.
// It is derived from the handle fingerprint so it is stable across an
// injection and carries no credential material.
const PlaceholderPrefix = "ac_ph_"

// InjectionRequest selects one handle from a Snapshot and states the use it is
// authorized for. The injection path never re-resolves policy, so a request
// that names a handle the snapshot does not hold is denied.
type InjectionRequest struct {
	HandleID string
	Request  Request
}

// SecretSpec is the driver-agnostic description of one credential for a sandbox
// secret mechanism: the placeholder reaches the guest, the mechanism substitutes
// Value only for AllowHosts over verified TLS.
type SecretSpec struct {
	// Name is the environment variable the guest sees.
	Name string
	// Value is the credential truth. It never crosses into the guest and must
	// never be logged, persisted, or serialized.
	Value string
	// Placeholder replaces Value inside the guest.
	Placeholder string
	// AllowHosts is the exact endpoint set where substitution may happen.
	AllowHosts []string
	// RequireTLS is always true: substitution without a verified TLS peer would
	// leak the credential to whoever holds the network path.
	RequireTLS bool
}

// Validate reports whether the sandbox mechanism could enforce the spec safely.
func (s SecretSpec) Validate() error {
	switch {
	case strings.TrimSpace(s.Name) == "":
		return fmt.Errorf("%w: environment variable name is required", ErrInvalidSecretSpec)
	case s.Value == "":
		return fmt.Errorf("%w: credential value is required", ErrInvalidSecretSpec)
	case strings.TrimSpace(s.Placeholder) == "":
		return fmt.Errorf("%w: placeholder is required", ErrInvalidSecretSpec)
	case len(s.AllowHosts) == 0:
		return fmt.Errorf("%w: at least one allowed host is required", ErrInvalidSecretSpec)
	case !s.RequireTLS:
		return fmt.Errorf("%w: TLS verification must be required", ErrInvalidSecretSpec)
	}
	for _, host := range s.AllowHosts {
		if strings.TrimSpace(host) == "" {
			return fmt.Errorf("%w: allowed host must not be blank", ErrInvalidSecretSpec)
		}
	}
	if strings.ContainsAny(s.Placeholder, "\x00\r\n") {
		return fmt.Errorf("%w: placeholder must not contain NUL, CR, or LF", ErrInvalidSecretSpec)
	}
	return nil
}

// PlanInjection authorizes every request against the fixed snapshot and only
// then produces secret specs. It is all-or-nothing: any denial returns no specs
// at all, so a caller can never write a header or environment variable for a
// partially verified set of credentials.
//
// Requests that share a handle are evaluated together with AuthorizeBatch, so
// their effective owners are the intersection rather than the union.
//
// Two different handles may not claim the same guest environment variable. The
// guest holds one value per name, so the second claim would silently shadow the
// first; that is refused here, at the planning boundary, rather than being left
// for a driver to discover after this function has already reported success.
func PlanInjection(snapshot Snapshot, reqs []InjectionRequest, now time.Time) ([]SecretSpec, error) {
	if len(reqs) == 0 {
		return nil, nil
	}
	order, grouped := groupInjectionRequests(reqs)

	// Phase 1: verify every authorization before emitting anything.
	authorizations := make([]Authorization, 0, len(order))
	materials := make([]Material, 0, len(order))
	claimed := make(map[string]string, len(order))
	for _, handleID := range order {
		material, ok := snapshot.Lookup(handleID)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrHandleNotInSnapshot, handleID)
		}
		name := material.Handle.Normalized().EnvName
		if owner, duplicate := claimed[name]; duplicate {
			return nil, fmt.Errorf("%w: environment variable %q is claimed by handles %s and %s", ErrInvalidSecretSpec, name, owner, handleID)
		}
		claimed[name] = handleID
		group := grouped[handleID]
		var (
			authorization Authorization
			err           error
		)
		if len(group) == 1 {
			authorization, err = material.Handle.Authorize(group[0], now)
		} else {
			authorization, err = material.Handle.AuthorizeBatch(group, now)
		}
		if err != nil {
			return nil, err
		}
		authorizations = append(authorizations, authorization)
		materials = append(materials, material)
	}

	// Phase 2: describe the substitutions. Nothing here can re-decide policy.
	specs := make([]SecretSpec, 0, len(order))
	for i, authorization := range authorizations {
		spec, err := specFor(materials[i], authorization)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// groupInjectionRequests preserves first-seen order while collecting the
// requests that share a handle.
func groupInjectionRequests(reqs []InjectionRequest) ([]string, map[string][]Request) {
	order := make([]string, 0, len(reqs))
	grouped := make(map[string][]Request, len(reqs))
	for _, req := range reqs {
		handleID := strings.TrimSpace(req.HandleID)
		if _, seen := grouped[handleID]; !seen {
			order = append(order, handleID)
		}
		grouped[handleID] = append(grouped[handleID], req.Request)
	}
	return order, grouped
}

func specFor(material Material, authorization Authorization) (SecretSpec, error) {
	handle := material.Handle.Normalized()
	// The placeholder is derived from the fingerprint, so a handle without one
	// would produce the bare prefix for every such handle and make two secrets
	// indistinguishable inside the guest. Refuse instead of planning a
	// substitution the guest cannot disambiguate.
	if handle.TokenFingerprint == "" {
		return SecretSpec{}, fmt.Errorf("%w: handle %s has no token fingerprint to derive a placeholder", ErrInvalidHandle, handle.ID)
	}
	spec := SecretSpec{
		Name:        handle.EnvName,
		Value:       material.Value,
		Placeholder: PlaceholderPrefix + handle.TokenFingerprint,
		AllowHosts:  []string{authorization.Endpoint},
		RequireTLS:  true,
	}
	if err := spec.Validate(); err != nil {
		return SecretSpec{}, err
	}
	return spec, nil
}
