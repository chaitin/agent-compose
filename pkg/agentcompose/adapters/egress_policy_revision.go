package adapters

import (
	"context"
	"fmt"
	"sync"

	"github.com/chaitin/agent-compose/pkg/egress"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// EgressPolicyRevisionStore is the persistence seam for per-sandbox policy
// revisions. It is implemented by the sandbox store; a composition that does not
// provide it keeps the policy decision and only loses the revision account.
type EgressPolicyRevisionStore interface {
	ObserveEgressPolicyRevision(ctx context.Context, sandboxID, hash string, acknowledge bool) (egress.PolicyRevision, error)
	GetEgressPolicyRevision(ctx context.Context, sandboxID string) (egress.PolicyRevision, error)
}

// ObserveSandboxEgressPolicy reports the revision of the egress policy in force
// for one sandbox, persisting it when it changed. It deliberately does not
// acknowledge the revision: an inspect-time reader must not be mistaken for the
// enforcement point of a running sandbox.
func (p SandboxEgressProxy) ObserveSandboxEgressPolicy(ctx context.Context, session *domain.Sandbox) (egress.PolicyRevision, error) {
	if session == nil {
		return egress.PolicyRevision{}, nil
	}
	policy, err := p.policyForSession(ctx, session)
	if err != nil {
		return egress.PolicyRevision{}, fmt.Errorf("resolve the egress policy revision for sandbox %s: %w", session.Summary.ID, err)
	}
	return p.observePolicyRevision(ctx, session, policy, false)
}

// observePolicyRevision records the effective policy for a sandbox and returns
// its revision. acknowledge is true only for the enforcement point that decides
// the running sandbox's traffic (the proxy for a mediated sandbox, runtime
// creation for a driver-applied one).
//
// The cache is keyed by sandbox and hash, so a per-request read of an unchanged
// policy costs no disk I/O. A changed hash always goes to the store, which is
// what keeps a policy edit visible on the next request instead of being hidden
// by a stale cache entry.
func (p SandboxEgressProxy) observePolicyRevision(ctx context.Context, session *domain.Sandbox, policy egress.Policy, acknowledge bool) (egress.PolicyRevision, error) {
	if session == nil {
		return egress.PolicyRevision{}, nil
	}
	store, ok := p.sandboxes.(EgressPolicyRevisionStore)
	if !ok {
		return egress.PolicyRevision{}, nil
	}
	hash := egress.PolicyHash(policy)
	if cached, ok := p.revisions.get(session.Summary.ID); ok && cached.Hash == hash && (!acknowledge || cached.Acknowledged()) {
		return cached, nil
	}
	revision, err := store.ObserveEgressPolicyRevision(ctx, session.Summary.ID, hash, acknowledge)
	if err != nil {
		return egress.PolicyRevision{}, err
	}
	p.revisions.set(session.Summary.ID, revision)
	return revision, nil
}

// egressPolicyRevisionCache is a concurrency-safe read-through cache shared by
// the value copies of one SandboxEgressProxy.
type egressPolicyRevisionCache struct {
	mu      sync.Mutex
	entries map[string]egress.PolicyRevision
}

func newEgressPolicyRevisionCache() *egressPolicyRevisionCache {
	return &egressPolicyRevisionCache{entries: make(map[string]egress.PolicyRevision)}
}

func (c *egressPolicyRevisionCache) get(sandboxID string) (egress.PolicyRevision, bool) {
	if c == nil {
		return egress.PolicyRevision{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	revision, ok := c.entries[sandboxID]
	return revision, ok
}

func (c *egressPolicyRevisionCache) set(sandboxID string, revision egress.PolicyRevision) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[sandboxID] = revision
}
