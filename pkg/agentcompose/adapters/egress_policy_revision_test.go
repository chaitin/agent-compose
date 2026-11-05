package adapters

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/egress"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// egressPolicyRevisionTestSandboxes is both the sandbox loader the mediation
// needs and the revision store it writes through, so a test observes the exact
// revision the production path would persist.
type egressPolicyRevisionTestSandboxes struct {
	session   *domain.Sandbox
	mu        sync.Mutex
	revisions map[string]egress.PolicyRevision
	observes  int
}

func (s *egressPolicyRevisionTestSandboxes) GetSandbox(context.Context, string) (*domain.Sandbox, error) {
	return s.session, nil
}

func (s *egressPolicyRevisionTestSandboxes) ObserveEgressPolicyRevision(_ context.Context, sandboxID, hash string, acknowledge bool) (egress.PolicyRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observes++
	now := time.Now().UTC()
	current := s.revisions[sandboxID]
	if current.Hash == hash {
		if acknowledge && !current.Acknowledged() {
			current.AckAt = now
		}
		s.revisions[sandboxID] = current
		return current, nil
	}
	next := egress.PolicyRevision{Revision: current.Revision + 1, Hash: hash, AppliedAt: now}
	if acknowledge {
		next.AckAt = now
	}
	s.revisions[sandboxID] = next
	return next, nil
}

func (s *egressPolicyRevisionTestSandboxes) GetEgressPolicyRevision(_ context.Context, sandboxID string) (egress.PolicyRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revisions[sandboxID], nil
}

func (s *egressPolicyRevisionTestSandboxes) grabbed() (int, egress.PolicyRevision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observes, s.revisions["sandbox-1"]
}

func newEgressPolicyRevisionTestSandboxes(session *domain.Sandbox) *egressPolicyRevisionTestSandboxes {
	return &egressPolicyRevisionTestSandboxes{session: session, revisions: map[string]egress.PolicyRevision{}}
}

func TestSandboxEgressProxyAcknowledgesThePolicyItReads(t *testing.T) {
	sandboxes := newEgressPolicyRevisionTestSandboxes(networkPolicyTestSession())
	mediation := egressProxyTestMediation(newEgressProxyTestStore(), sandboxes, denyDeclaration())

	if _, err := mediation.EgressProxyPolicy(context.Background(), "sandbox-1"); err != nil {
		t.Fatalf("EgressProxyPolicy() error = %v", err)
	}
	observes, revision := sandboxes.grabbed()
	if observes != 1 {
		t.Fatalf("revision store observations = %d, want one", observes)
	}
	if revision.Revision != 1 || !revision.Acknowledged() {
		t.Fatalf("revision = %+v, want one acknowledged revision", revision)
	}
	expected, err := mediation.policyForSession(context.Background(), networkPolicyTestSession())
	if err != nil {
		t.Fatalf("policyForSession() error = %v", err)
	}
	if revision.Hash != egress.PolicyHash(expected) {
		t.Fatalf("revision hash = %q, want the hash of the effective policy", revision.Hash)
	}

	// Repeating the same decision is served from the cache; the proxy still
	// reads the declaration, but it does not need to touch the revision file.
	if _, err := mediation.EgressProxyPolicy(context.Background(), "sandbox-1"); err != nil {
		t.Fatalf("second EgressProxyPolicy() error = %v", err)
	}
	if observesAfter, _ := sandboxes.grabbed(); observesAfter != 1 {
		t.Fatalf("revision store observations after an unchanged policy = %d, want the cache to answer", observesAfter)
	}
}

func TestSandboxEgressProxyRevisionChangesOnlyWhenThePolicyDoes(t *testing.T) {
	sandboxes := newEgressPolicyRevisionTestSandboxes(networkPolicyTestSession())
	first := egressProxyTestMediation(newEgressProxyTestStore(), sandboxes, denyDeclaration())
	if _, err := first.EgressProxyPolicy(context.Background(), "sandbox-1"); err != nil {
		t.Fatalf("first EgressProxyPolicy() error = %v", err)
	}

	// Inspect compiles the declaration in force now but must not acknowledge a
	// revision the enforcement point has not read.
	changed := &egress.NetworkDeclaration{
		Default: egress.Deny,
		Allow:   []egress.AllowEntry{{Host: "example.com", Port: 443, Protocol: egress.ProtocolHTTPS}},
	}
	second := egressProxyTestMediation(newEgressProxyTestStore(), sandboxes, changed)
	observed, err := second.ObserveSandboxEgressPolicy(context.Background(), networkPolicyTestSession())
	if err != nil {
		t.Fatalf("ObserveSandboxEgressPolicy() error = %v", err)
	}
	if observed.Revision != 2 || observed.Acknowledged() {
		t.Fatalf("inspect revision = %+v, want revision 2 without acknowledgement", observed)
	}

	// The proxy is the enforcement point, so its next read acknowledges it.
	if _, err := second.EgressProxyPolicy(context.Background(), "sandbox-1"); err != nil {
		t.Fatalf("second EgressProxyPolicy() error = %v", err)
	}
	_, acknowledged := sandboxes.grabbed()
	if acknowledged.Revision != 2 || !acknowledged.Acknowledged() {
		t.Fatalf("revision after the proxy read = %+v, want revision 2 acknowledged", acknowledged)
	}
}

func TestObserveSandboxEgressPolicyWithoutARevisionStoreIsInert(t *testing.T) {
	mediation := egressProxyTestMediation(newEgressProxyTestStore(), egressProxyTestSandboxes{session: networkPolicyTestSession()}, denyDeclaration())
	revision, err := mediation.ObserveSandboxEgressPolicy(context.Background(), networkPolicyTestSession())
	if err != nil {
		t.Fatalf("ObserveSandboxEgressPolicy() error = %v", err)
	}
	if revision.Recorded() {
		t.Fatalf("revision = %+v, want no revision without a store", revision)
	}
}
