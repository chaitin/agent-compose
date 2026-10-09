package credentials

import (
	"errors"
	"time"
)

// Request is one credential use: the endpoint the caller wants the credential
// presented to, and the owners the call is attributed to.
type Request struct {
	Endpoint string
	Owners   []Owner
}

// Authorization is the positive outcome of evaluating a Request against a
// Handle. It records the intersection of owners that were verified, which is
// what an audit trail needs: the fact that a specific set of subjects was
// authorized to use a specific handle at a specific endpoint.
type Authorization struct {
	HandleID string
	Endpoint string
	Owners   []Owner
}

// Authorize evaluates one use of the handle at now.
//
// The check order is the security order: attribution first (an unattributable
// use is denied even for a valid handle), then handle validity, then lifetime,
// then the endpoint scope, then the owner scope. A denial is total: it never
// returns a partial Authorization.
//
// The endpoint check is the enforcement of "right to forward is not right to a
// credential": an egress decision that allowed some other target carries no
// authority here, because only the handle's declared endpoint can match.
func (h Handle) Authorize(req Request, now time.Time) (Authorization, error) {
	normalized := h.Normalized()
	if len(NormalizeOwners(req.Owners)) == 0 {
		return Authorization{}, ErrUnattributable
	}
	if err := normalized.Validate(); err != nil {
		return Authorization{}, err
	}
	if err := normalized.checkActive(now); err != nil {
		return Authorization{}, err
	}
	if !normalized.Scope.EndpointMatches(req.Endpoint) {
		return Authorization{}, &ScopeError{
			HandleID:  normalized.ID,
			Requested: trimEndpoint(req.Endpoint),
			Granted:   normalized.Scope.Endpoint,
			Err:       ErrEndpointOutOfScope,
		}
	}
	owners := NormalizeOwners(req.Owners)
	for _, owner := range owners {
		if !normalized.grantsOwner(owner) {
			return Authorization{}, &ScopeError{
				HandleID: normalized.ID,
				Owner:    owner,
				Err:      ErrOwnerNotGranted,
			}
		}
	}
	return Authorization{
		HandleID: normalized.ID,
		Endpoint: normalized.Scope.Endpoint,
		Owners:   owners,
	}, nil
}

// AuthorizeBatch evaluates several uses of the same handle as one
// authorization. Every sub-call must be valid on its own, and the owners
// attributed to the result are the intersection across sub-calls, so a
// permissive sub-call cannot lend its identity to a stricter one. An empty
// intersection is unattributable and is denied.
func (h Handle) AuthorizeBatch(reqs []Request, now time.Time) (Authorization, error) {
	if len(reqs) == 0 {
		return Authorization{}, ErrUnattributable
	}
	// Every sub-call must be valid on its own, so an out-of-scope endpoint or an
	// ungranted owner fails the whole batch rather than being masked by the
	// intersection, which only decides attribution.
	for _, req := range reqs {
		if _, err := h.Authorize(req, now); err != nil {
			return Authorization{}, err
		}
	}
	intersection := IntersectOwners(reqs)
	if len(intersection) == 0 {
		return Authorization{}, ErrUnattributable
	}
	normalized := h.Normalized()
	return Authorization{
		HandleID: normalized.ID,
		Endpoint: normalized.Scope.Endpoint,
		Owners:   intersection,
	}, nil
}

// IntersectOwners returns the owners present in every request, normalized and
// ordered. A missing owner list yields nothing, which callers must treat as a
// denial rather than as "no restriction".
func IntersectOwners(reqs []Request) []Owner {
	if len(reqs) == 0 {
		return nil
	}
	counts := make(map[Owner]int)
	for _, req := range reqs {
		owners := NormalizeOwners(req.Owners)
		if len(owners) == 0 {
			return nil
		}
		for _, owner := range owners {
			counts[owner]++
		}
	}
	intersection := make([]Owner, 0, len(counts))
	for owner, count := range counts {
		if count == len(reqs) {
			intersection = append(intersection, owner)
		}
	}
	return NormalizeOwners(intersection)
}

// checkActive classifies an inactive handle so callers can report why.
func (h Handle) checkActive(now time.Time) error {
	if !h.RevokedAt.IsZero() {
		return ErrRevoked
	}
	if !h.Active(now) {
		return ErrExpired
	}
	return nil
}

func (h Handle) grantsOwner(owner Owner) bool {
	for _, granted := range h.Scope.Owners {
		if granted == owner {
			return true
		}
	}
	return false
}

// ScopeError explains a scope denial without disclosing credential material.
type ScopeError struct {
	HandleID  string
	Owner     Owner
	Requested string
	Granted   string
	Err       error
}

func (e *ScopeError) Error() string {
	switch {
	case errors.Is(e.Err, ErrOwnerNotGranted):
		return "credential handle " + e.HandleID + " is not granted to owner " + e.Owner.Kind + "/" + e.Owner.ID
	case e.Requested != "":
		return "credential handle " + e.HandleID + " is scoped to endpoint " + e.Granted + ", not " + e.Requested
	default:
		return "credential handle " + e.HandleID + " scope violation"
	}
}

// Unwrap exposes the sentinel so errors.Is classifies the denial.
func (e *ScopeError) Unwrap() error { return e.Err }

func trimEndpoint(endpoint string) string {
	return NormalizeOwner(Owner{Kind: "endpoint", ID: endpoint}).ID
}
