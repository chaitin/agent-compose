package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/egress"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

const sandboxEgressPolicyTestID = "11111111-1111-4111-8111-111111111111"

type sandboxEgressPolicyTestStore struct {
	session  *domain.Sandbox
	vmState  domain.VMState
	revision egress.PolicyRevision
}

func (s *sandboxEgressPolicyTestStore) GetSandbox(context.Context, string) (*domain.Sandbox, error) {
	return s.session, nil
}

func (s *sandboxEgressPolicyTestStore) RemoveSandbox(context.Context, string) error {
	return nil
}

func (s *sandboxEgressPolicyTestStore) GetVMState(string) (domain.VMState, error) {
	return s.vmState, nil
}

func (s *sandboxEgressPolicyTestStore) GetEgressPolicyRevision(context.Context, string) (egress.PolicyRevision, error) {
	return s.revision, nil
}

type sandboxEgressPolicyTestObserver struct {
	revision egress.PolicyRevision
	err      error
	calls    int
}

func (o *sandboxEgressPolicyTestObserver) ObserveSandboxEgressPolicy(context.Context, *domain.Sandbox) (egress.PolicyRevision, error) {
	o.calls++
	return o.revision, o.err
}

func newSandboxEgressPolicyTestHandler(t *testing.T, store *sandboxEgressPolicyTestStore, observer SandboxEgressPolicyObserver) *SandboxHandler {
	t.Helper()
	handler := NewSandboxHandler(SandboxHandlerDeps{Store: store})
	if observer != nil {
		handler.WithEgressPolicyObserver(observer)
	}
	return handler
}

func TestGetSandboxReportsThePersistedEgressPolicyRevision(t *testing.T) {
	appliedAt := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	ackAt := appliedAt.Add(time.Minute)
	store := &sandboxEgressPolicyTestStore{
		session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxEgressPolicyTestID, VMStatus: domain.VMStatusRunning}},
		vmState: domain.VMState{EgressPolicy: domain.SandboxEgressPolicy{Generation: 42, Hash: "sha256:runtime", Applied: string(egress.ApplicationMediated)}},
	}
	observer := &sandboxEgressPolicyTestObserver{revision: egress.PolicyRevision{
		Revision: 3, Hash: "sha256:active", AppliedAt: appliedAt, AckAt: ackAt,
	}}
	handler := newSandboxEgressPolicyTestHandler(t, store, observer)

	response, err := handler.GetSandbox(context.Background(), connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: sandboxEgressPolicyTestID}))
	if err != nil {
		t.Fatalf("GetSandbox() error = %v", err)
	}
	policy := response.Msg.GetSandbox().GetEgressPolicy()
	if policy == nil {
		t.Fatal("GetSandbox() returned no egress policy")
	}
	if policy.GetRevision() != 3 || policy.GetHash() != "sha256:active" {
		t.Fatalf("reported revision = %+v, want revision 3 with the active hash", policy)
	}
	if !policy.GetAcknowledged() {
		t.Fatal("acknowledged = false, want the proxy-read acknowledgement")
	}
	if !policy.GetAppliedAt().AsTime().Equal(appliedAt) || !policy.GetAcknowledgedAt().AsTime().Equal(ackAt) {
		t.Fatalf("reported timestamps = %+v, %+v; want %v, %v", policy.GetAppliedAt().AsTime(), policy.GetAcknowledgedAt().AsTime(), appliedAt, ackAt)
	}
	if policy.GetRuntimeGeneration() != 42 || policy.GetRuntimeApplication() != string(egress.ApplicationMediated) {
		t.Fatalf("runtime record = generation %d application %q, want the persisted mediated record", policy.GetRuntimeGeneration(), policy.GetRuntimeApplication())
	}
	if observer.calls != 1 {
		t.Fatalf("observer calls = %d, want one", observer.calls)
	}
}

func TestGetSandboxFallsBackToThePersistedRevisionWhenObservationFails(t *testing.T) {
	appliedAt := time.Date(2026, 11, 3, 12, 30, 0, 0, time.UTC)
	store := &sandboxEgressPolicyTestStore{
		session:  &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxEgressPolicyTestID, VMStatus: domain.VMStatusStopped}},
		revision: egress.PolicyRevision{Revision: 2, Hash: "sha256:persisted", AppliedAt: appliedAt},
	}
	handler := newSandboxEgressPolicyTestHandler(t, store, &sandboxEgressPolicyTestObserver{err: errors.New("policy unavailable")})

	response, err := handler.GetSandbox(context.Background(), connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: sandboxEgressPolicyTestID}))
	if err != nil {
		t.Fatalf("GetSandbox() error = %v", err)
	}
	policy := response.Msg.GetSandbox().GetEgressPolicy()
	if policy.GetRevision() != 2 || policy.GetHash() != "sha256:persisted" || policy.GetAcknowledged() {
		t.Fatalf("fallback revision = %+v, want the persisted unacknowledged revision", policy)
	}
	if policy.GetAppliedAt().AsTime().Equal(appliedAt) == false {
		t.Fatalf("applied_at = %v, want %v", policy.GetAppliedAt().AsTime(), appliedAt)
	}
}

func TestGetSandboxOmitsAnAbsentEgressPolicy(t *testing.T) {
	store := &sandboxEgressPolicyTestStore{
		session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxEgressPolicyTestID, VMStatus: domain.VMStatusPending}},
	}
	handler := newSandboxEgressPolicyTestHandler(t, store, &sandboxEgressPolicyTestObserver{})

	response, err := handler.GetSandbox(context.Background(), connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: sandboxEgressPolicyTestID}))
	if err != nil {
		t.Fatalf("GetSandbox() error = %v", err)
	}
	if got := response.Msg.GetSandbox().GetEgressPolicy(); got != nil {
		t.Fatalf("egress policy = %+v, want none for a sandbox with no record", got)
	}
}
