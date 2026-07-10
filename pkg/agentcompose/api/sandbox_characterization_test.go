package api

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	"agent-compose/pkg/identity"
	domain "agent-compose/pkg/model"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestV2SandboxRemoveUsesSandboxIDAndSessionCompatibilityDelegate(t *testing.T) {
	sandboxID := identity.NewID(identity.ResourceSandbox, "characterization", "remove")
	delegate := &characterizationSessionDelegate{}
	store := &characterizationSandboxStore{
		session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID, VMStatus: domain.VMStatusRunning}},
	}
	remover := &characterizationSandboxRemover{}
	dashboard := &characterizationDashboard{}
	handler := NewSandboxHandler(delegate, store, remover, dashboard)

	resp, err := handler.RemoveSandbox(context.Background(), connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{
		SandboxId: " " + sandboxID + " ",
		Force:     true,
	}))
	if err != nil {
		t.Fatalf("RemoveSandbox returned error: %v", err)
	}
	if resp.Msg.GetSandboxId() != sandboxID || !resp.Msg.GetStopped() || !resp.Msg.GetRemoved() {
		t.Fatalf("RemoveSandbox response = %#v", resp.Msg)
	}
	if store.getID != sandboxID || store.removeID != sandboxID {
		t.Fatalf("sandbox store ids get=%q remove=%q, want %q", store.getID, store.removeID, sandboxID)
	}
	if len(delegate.stopSessionIDs) != 1 || delegate.stopSessionIDs[0] != sandboxID {
		t.Fatalf("compatibility StopSession calls = %#v, want [%q]", delegate.stopSessionIDs, sandboxID)
	}
	if len(remover.sandboxIDs) != 1 || remover.sandboxIDs[0] != sandboxID {
		t.Fatalf("runtime remove calls = %#v, want [%q]", remover.sandboxIDs, sandboxID)
	}
	if len(dashboard.events) != 1 || dashboard.events[0] != "sandbox_removed" {
		t.Fatalf("dashboard events = %#v", dashboard.events)
	}
}

func TestV2SandboxRemoveRejectsRunningWithoutForce(t *testing.T) {
	sandboxID := identity.NewID(identity.ResourceSandbox, "characterization", "running")
	store := &characterizationSandboxStore{
		session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID, VMStatus: domain.VMStatusRunning}},
	}
	handler := NewSandboxHandler(&characterizationSessionDelegate{}, store, &characterizationSandboxRemover{}, nil)

	_, err := handler.RemoveSandbox(context.Background(), connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{SandboxId: sandboxID}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("RemoveSandbox running code = %v, err=%v", connect.CodeOf(err), err)
	}
	if store.removeID != "" {
		t.Fatalf("RemoveSandbox removed running sandbox without force: %q", store.removeID)
	}
}

func TestV2SandboxRemoveKeepsMetadataWhenRuntimeRemovalFails(t *testing.T) {
	sandboxID := identity.NewID(identity.ResourceSandbox, "characterization", "runtime-remove-error")
	store := &characterizationSandboxStore{
		session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID, VMStatus: domain.VMStatusStopped}},
	}
	removeErr := errors.New("runtime remove failed")
	remover := &characterizationSandboxRemover{err: removeErr}
	handler := NewSandboxHandler(&characterizationSessionDelegate{}, store, remover, nil)

	_, err := handler.RemoveSandbox(context.Background(), connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{SandboxId: sandboxID}))
	if connect.CodeOf(err) != connect.CodeInternal || !errors.Is(err, removeErr) {
		t.Fatalf("RemoveSandbox runtime error = %v, code=%v", err, connect.CodeOf(err))
	}
	if len(remover.sandboxIDs) != 1 || remover.sandboxIDs[0] != sandboxID {
		t.Fatalf("runtime remove calls = %#v, want [%q]", remover.sandboxIDs, sandboxID)
	}
	if store.removeID != "" {
		t.Fatalf("metadata removed after runtime removal failed: %q", store.removeID)
	}
}

func TestV2SandboxRemoveValidationAndStoreErrors(t *testing.T) {
	handler := NewSandboxHandler(&characterizationSessionDelegate{}, &characterizationSandboxStore{}, &characterizationSandboxRemover{}, nil)
	for _, sandboxID := range []string{"", "not an id", "../bad"} {
		_, err := handler.RemoveSandbox(context.Background(), connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{SandboxId: sandboxID}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("RemoveSandbox(%q) code = %v, want invalid argument (err=%v)", sandboxID, connect.CodeOf(err), err)
		}
	}

	missingID := identity.NewID(identity.ResourceSandbox, "characterization", "missing")
	missing := NewSandboxHandler(&characterizationSessionDelegate{}, &characterizationSandboxStore{getErr: errors.New("missing")}, &characterizationSandboxRemover{}, nil)
	if _, err := missing.RemoveSandbox(context.Background(), connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{SandboxId: missingID})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("RemoveSandbox missing code = %v, want not found (err=%v)", connect.CodeOf(err), err)
	}

	removeErr := errors.New("remove failed")
	removeID := identity.NewID(identity.ResourceSandbox, "characterization", "remove-error")
	failing := NewSandboxHandler(&characterizationSessionDelegate{}, &characterizationSandboxStore{
		session:   &domain.Sandbox{Summary: domain.SandboxSummary{ID: removeID, VMStatus: domain.VMStatusStopped}},
		removeErr: removeErr,
	}, &characterizationSandboxRemover{}, nil)
	if _, err := failing.RemoveSandbox(context.Background(), connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{SandboxId: removeID})); connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("RemoveSandbox remove error code = %v, want internal (err=%v)", connect.CodeOf(err), err)
	}
}

type characterizationSessionDelegate struct {
	stopSessionIDs []string
}

func (d *characterizationSessionDelegate) CreateSession(context.Context, *connect.Request[agentcomposev1.CreateSessionRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return connect.NewResponse(&agentcomposev1.SessionResponse{}), nil
}

func (d *characterizationSessionDelegate) ResumeSession(context.Context, *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	return connect.NewResponse(&agentcomposev1.SessionResponse{}), nil
}

func (d *characterizationSessionDelegate) StopSession(_ context.Context, req *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionResponse], error) {
	d.stopSessionIDs = append(d.stopSessionIDs, req.Msg.GetSessionId())
	return connect.NewResponse(&agentcomposev1.SessionResponse{}), nil
}

func (d *characterizationSessionDelegate) GetSessionProxy(context.Context, *connect.Request[agentcomposev1.SessionIDRequest]) (*connect.Response[agentcomposev1.SessionProxyResponse], error) {
	return connect.NewResponse(&agentcomposev1.SessionProxyResponse{}), nil
}

type characterizationSandboxStore struct {
	session   *domain.Sandbox
	getID     string
	removeID  string
	getErr    error
	removeErr error
}

func (s *characterizationSandboxStore) GetSandbox(_ context.Context, id string) (*domain.Sandbox, error) {
	s.getID = id
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.session, nil
}

func (s *characterizationSandboxStore) RemoveSandbox(_ context.Context, id string) error {
	s.removeID = id
	return s.removeErr
}

type characterizationDashboard struct {
	events []string
}

type characterizationSandboxRemover struct {
	sandboxIDs []string
	err        error
}

func (r *characterizationSandboxRemover) RemoveSandboxVM(_ context.Context, sandbox *domain.Sandbox) error {
	r.sandboxIDs = append(r.sandboxIDs, sandbox.Summary.ID)
	return r.err
}

func (d *characterizationDashboard) Notify(event string) {
	d.events = append(d.events, event)
}
