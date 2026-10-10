// Package credentials owns the generic credential-handle model: a
// short-lived, narrowly scoped, revocable reference to a credential whose
// truth never enters a sandbox.
//
// A handle is the daemon-side record of an authorization, not the credential
// itself. It carries an identity (Handle), the hash of the bearer value that
// selects it (never the value), the endpoint and owners it is scoped to, and an
// explicit expiry. The LLM facade token is the specialization this model
// generalizes from: pkg/llms owns the boundary mapping and does not duplicate
// the behavior here. That mapping is not the daemon's live path today — the
// facade tokens NewFacadeToken mints carry no expiry, and an unbounded
// credential is deliberately not representable as a handle — so read the
// specialization as the shape the model was extracted from, not as a claim that
// facade tokens are stored here.
//
// Two invariants are enforced by this package and by its tests:
//
//   - the right to forward traffic to a target is not the right to a
//     credential for a different target (an egress allow cannot widen Scope);
//   - a use without owner metadata is unattributable and therefore denied.
package credentials

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Sentinel errors are classification points for callers and tests. They are
// deliberately distinct so an authorization failure can be reported as a
// capability fact rather than a generic error.
var (
	// ErrInvalidHandle reports a handle that cannot describe an authorization
	// (missing identity, malformed scope, non-positive lifetime, ...).
	ErrInvalidHandle = errors.New("invalid credential handle")
	// ErrHandleLifetimeUnsupported reports a credential whose lifetime the
	// handle model cannot express. A handle is a bounded, non-renewable lease,
	// so a credential that never expires is not representable as one: it keeps
	// its own revocation model instead of being forced into a handle.
	ErrHandleLifetimeUnsupported = errors.New("credential lifetime is not representable as a handle")
	// ErrUnattributable reports a use with no owner. Empty owner means the call
	// cannot be attributed, which is always a denial.
	ErrUnattributable = errors.New("credential use has no owner")
	// ErrExpired reports a handle used at or after its explicit expiry.
	ErrExpired = errors.New("credential handle expired")
	// ErrRevoked reports a handle whose revocation took effect.
	ErrRevoked = errors.New("credential handle revoked")
	// ErrEndpointOutOfScope reports a use against an endpoint the handle was
	// not scoped to. It is the enforcement point for "right to forward is not
	// right to a credential".
	ErrEndpointOutOfScope = errors.New("credential handle is not scoped to the requested endpoint")
	// ErrOwnerNotGranted reports an owner that is not in the handle's scope.
	ErrOwnerNotGranted = errors.New("credential handle is not granted to the requesting owner")
	// ErrHandleNotInSnapshot reports an injection request for a handle the
	// fixed snapshot does not hold. The injection path never resolves policy,
	// so a handle it cannot find is a denial rather than a lookup miss to fix.
	ErrHandleNotInSnapshot = errors.New("credential handle is not in the injection snapshot")
	// ErrInvalidSecretSpec reports a secret description the sandbox mechanism
	// could not enforce safely (empty name, no allowed host, TLS not required).
	ErrInvalidSecretSpec = errors.New("invalid credential secret spec")
)

// Kind identifies the credential form a handle represents. It is descriptive
// rather than behavioral: every kind obeys the same expiry, scope, and
// revocation rules.
type Kind string

const (
	KindLLMFacade Kind = "llm_facade"
	KindGit       Kind = "git"
	KindMCP       Kind = "mcp"
	KindRegistry  Kind = "registry"
	KindGeneric   Kind = "generic"
)

// Valid reports whether the kind is a recognized credential form.
func (k Kind) Valid() bool {
	switch k {
	case KindLLMFacade, KindGit, KindMCP, KindRegistry, KindGeneric:
		return true
	default:
		return false
	}
}

// Owner names the subject a credential use is attributed to, for example a
// sandbox, a run, or a declared agent. Both fields are required: an owner that
// names only a kind cannot be attributed either.
type Owner struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// NormalizeOwner trims an owner's fields.
func NormalizeOwner(owner Owner) Owner {
	return Owner{Kind: strings.TrimSpace(owner.Kind), ID: strings.TrimSpace(owner.ID)}
}

// IsZero reports whether the owner carries no attribution at all.
func (o Owner) IsZero() bool {
	normalized := NormalizeOwner(o)
	return normalized.Kind == "" || normalized.ID == ""
}

// NormalizeOwners trims, drops empty owners, deduplicates, and sorts. Sorting
// makes the persistent JSON encoding and the batch-owner intersection
// deterministic and comparable.
func NormalizeOwners(owners []Owner) []Owner {
	if len(owners) == 0 {
		return nil
	}
	seen := make(map[Owner]struct{}, len(owners))
	normalized := make([]Owner, 0, len(owners))
	for _, owner := range owners {
		item := NormalizeOwner(owner)
		if item.IsZero() {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		normalized = append(normalized, item)
	}
	if len(normalized) == 0 {
		return nil
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Kind != normalized[j].Kind {
			return normalized[i].Kind < normalized[j].Kind
		}
		return normalized[i].ID < normalized[j].ID
	})
	return normalized
}

// Scope is the narrow authorization a handle carries: the single endpoint it
// may be presented to and the owners it is bound to. A handle with no endpoint
// or no owner cannot be constructed.
type Scope struct {
	Endpoint string  `json:"endpoint"`
	Owners   []Owner `json:"owners"`
}

// Normalized returns a copy of the scope with trimmed fields and canonical
// owner ordering.
func (s Scope) Normalized() Scope {
	return Scope{Endpoint: strings.TrimSpace(s.Endpoint), Owners: NormalizeOwners(s.Owners)}
}

// EndpointMatches reports whether the scope authorizes the requested endpoint.
// Endpoints compare case-insensitively after trimming because host names are
// case-insensitive and callers build them from different sources.
func (s Scope) EndpointMatches(endpoint string) bool {
	return strings.EqualFold(strings.TrimSpace(s.Endpoint), strings.TrimSpace(endpoint))
}

// Handle is the generic, persisted credential handle.
//
// TokenHash is the only representation of the bearer value that is ever
// stored; the raw value exists only between minting and the sandbox secret
// mechanism. EnvName is the environment variable the sandbox sees as the
// placeholder for the credential.
type Handle struct {
	ID               string
	Kind             Kind
	TokenHash        string
	TokenFingerprint string
	EnvName          string
	SandboxID        string
	RunID            string
	Scope            Scope
	IssuedAt         time.Time
	ExpiresAt        time.Time
	RevokedAt        time.Time
}

// Normalized returns a copy of the handle with trimmed identity fields and a
// canonical scope.
func (h Handle) Normalized() Handle {
	h.ID = strings.TrimSpace(h.ID)
	h.TokenHash = strings.TrimSpace(h.TokenHash)
	h.TokenFingerprint = strings.TrimSpace(h.TokenFingerprint)
	h.EnvName = strings.TrimSpace(h.EnvName)
	h.SandboxID = strings.TrimSpace(h.SandboxID)
	h.RunID = strings.TrimSpace(h.RunID)
	h.Scope = h.Scope.Normalized()
	return h
}

// Validate reports whether the handle describes a usable authorization. It
// refuses a handle with no owner so an unattributable handle can never be
// persisted in the first place.
//
// The lifetime cap is enforced here rather than only at minting, so it holds for
// every handle that reaches an authorize or persist call, including one
// reconstructed from the store: "a handle is a bounded lease" is a property of
// the model, not only of NewHandle.
func (h Handle) Validate() error {
	normalized := h.Normalized()
	switch {
	case normalized.ID == "":
		return fmt.Errorf("%w: handle id is required", ErrInvalidHandle)
	case !normalized.Kind.Valid():
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidHandle, h.Kind)
	case normalized.TokenHash == "":
		return fmt.Errorf("%w: token hash is required", ErrInvalidHandle)
	case normalized.EnvName == "":
		return fmt.Errorf("%w: environment variable name is required", ErrInvalidHandle)
	case normalized.Scope.Endpoint == "":
		return fmt.Errorf("%w: scope endpoint is required", ErrInvalidHandle)
	case len(normalized.Scope.Owners) == 0:
		// An empty owner list is unattributable and must never become a grant.
		return fmt.Errorf("%w: %w", ErrInvalidHandle, ErrUnattributable)
	case normalized.IssuedAt.IsZero():
		return fmt.Errorf("%w: issuance time is required", ErrInvalidHandle)
	case !normalized.ExpiresAt.After(normalized.IssuedAt):
		return fmt.Errorf("%w: expiry must be after issuance", ErrInvalidHandle)
	case normalized.ExpiresAt.Sub(normalized.IssuedAt) > MaxHandleTTL:
		return fmt.Errorf("%w: lifetime %s exceeds maximum %s", ErrInvalidHandle, normalized.ExpiresAt.Sub(normalized.IssuedAt), MaxHandleTTL)
	}
	return nil
}

// Active reports whether the handle may authorize a use at now: it is neither
// revoked nor expired. Expiry is exclusive at ExpiresAt, so a handle stops
// authorizing exactly at its deadline.
func (h Handle) Active(now time.Time) bool {
	normalized := h.Normalized()
	if !normalized.RevokedAt.IsZero() {
		return false
	}
	if normalized.ExpiresAt.IsZero() {
		return false
	}
	return now.UTC().Before(normalized.ExpiresAt.UTC())
}
