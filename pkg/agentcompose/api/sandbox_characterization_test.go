package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	"agent-compose/pkg/identity"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestV2SandboxLifecycleActions(t *testing.T) {
	sandboxID := identity.NewID(identity.ResourceSandbox, "characterization", "lifecycle")
	now := time.Now().UTC().Truncate(time.Second)
	delegate := &characterizationSessionDelegate{}
	store := &characterizationSandboxStore{session: &domain.Sandbox{Summary: domain.SandboxSummary{
		ID: sandboxID, Driver: "docker", VMStatus: domain.VMStatusRunning, CreatedAt: now, UpdatedAt: now, ProxyPath: "/agent-compose/session/" + sandboxID,
	}}}
	handler := NewSandboxHandler(delegate, store, &characterizationSandboxRemover{}, nil)

	got, err := handler.GetSandbox(context.Background(), connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: sandboxID}))
	if err != nil || got.Msg.GetSandbox().GetStatus() != domain.VMStatusRunning || got.Msg.GetSandbox().GetProxyPath() == "" || delegate.proxyCalls != 0 {
		t.Fatalf("GetSandbox() = %#v, proxy calls=%d, err=%v", got, delegate.proxyCalls, err)
	}
	stopped, err := handler.StopSandbox(context.Background(), connect.NewRequest(&agentcomposev2.StopSandboxRequest{SandboxId: sandboxID}))
	if err != nil || stopped.Msg.GetSandbox().GetStatus() != domain.VMStatusStopped || len(delegate.stopSessionIDs) != 1 {
		t.Fatalf("StopSandbox() = %#v, calls=%v, err=%v", stopped, delegate.stopSessionIDs, err)
	}
	store.session.Summary.VMStatus = domain.VMStatusStopped
	resumed, err := handler.ResumeSandbox(context.Background(), connect.NewRequest(&agentcomposev2.ResumeSandboxRequest{SandboxId: sandboxID}))
	if err != nil || resumed.Msg.GetSandbox().GetStatus() != domain.VMStatusRunning || len(delegate.resumeSessionIDs) != 1 {
		t.Fatalf("ResumeSandbox() = %#v, calls=%v, err=%v", resumed, delegate.resumeSessionIDs, err)
	}
}

func TestV2GetSandboxAcceptsLegacyUUID(t *testing.T) {
	const sandboxID = "28fed243-4d9d-4e56-96cf-8b2baa8643c8"
	store := &characterizationSandboxStore{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID}}}
	handler := NewSandboxHandler(&characterizationSessionDelegate{}, store, &characterizationSandboxRemover{}, nil)

	response, err := handler.GetSandbox(context.Background(), connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: sandboxID}))
	if err != nil {
		t.Fatalf("GetSandbox legacy UUID returned error: %v", err)
	}
	if got := response.Msg.GetSandbox().GetSandboxId(); got != sandboxID || store.getID != sandboxID {
		t.Fatalf("GetSandbox legacy UUID = %q, store ID = %q", got, store.getID)
	}
	for _, invalid := range []string{"../sandbox", "legacy-session"} {
		if _, err := handler.GetSandbox(context.Background(), connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: invalid})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("GetSandbox(%q) code = %v, want invalid_argument", invalid, connect.CodeOf(err))
		}
	}
}

func TestV2GetSandboxIncludesSavedExposedNotebookURL(t *testing.T) {
	const sandboxID = "28fed243-4d9d-4e56-96cf-8b2baa8643c8"
	delegate := &characterizationSessionDelegate{}
	store := &characterizationSandboxStore{
		session:    &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID, VMStatus: domain.VMStatusRunning, ProxyPath: "/jupyter/" + sandboxID + "/lab"}},
		proxyState: domain.ProxyState{Enabled: true, Exposed: true, ProxyPath: "/jupyter/" + sandboxID + "/lab", Token: "token value"},
	}
	handler := NewSandboxHandler(delegate, store, &characterizationSandboxRemover{}, nil)

	response, err := handler.GetSandbox(context.Background(), connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: sandboxID}))
	if err != nil {
		t.Fatalf("GetSandbox returned error: %v", err)
	}
	if got := response.Msg.GetSandbox().GetNotebookUrl(); got != "/jupyter/"+sandboxID+"/lab?token=token+value" {
		t.Fatalf("notebook URL = %q", got)
	}
	if delegate.proxyCalls != 0 {
		t.Fatalf("GetSandbox prepared proxy %d times", delegate.proxyCalls)
	}
}

func TestV2GetSandboxIncludesNetworkEndpoints(t *testing.T) {
	const sandboxID = "28fed243-4d9d-4e56-96cf-8b2baa8643c8"
	store := &characterizationSandboxStore{session: &domain.Sandbox{
		Summary: domain.SandboxSummary{ID: sandboxID},
		NetworkState: &domain.SandboxNetworkState{
			Attachments: []domain.SandboxNetworkEndpoint{{Name: "frontend"}},
			Bindings:    []domain.SandboxPortBinding{{Networks: []string{"frontend"}, HostIP: "10.254.1.1", HostPort: 32000, GuestPort: 8080, Protocol: "tcp", Visibility: "internal", Publisher: "docker"}},
		},
	}}
	handler := NewSandboxHandler(&characterizationSessionDelegate{}, store, &characterizationSandboxRemover{}, nil)
	response, err := handler.GetSandbox(context.Background(), connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: sandboxID}))
	if err != nil {
		t.Fatalf("GetSandbox() error = %v", err)
	}
	network := response.Msg.GetSandbox().GetNetwork()
	if network == nil || len(network.GetAttachments()) != 1 || len(network.GetBindings()) != 1 {
		t.Fatalf("sandbox network = %#v", network)
	}
	if got := network.GetBindings()[0]; got.GetHostIp() != "10.254.1.1" || got.GetHostPort() != 32000 || got.GetGuestPort() != 8080 {
		t.Fatalf("sandbox binding = %#v", got)
	}
}

func TestGetSandboxDoesNotPrepareProxyForStoppedSandbox(t *testing.T) {
	const sandboxID = "28fed243-4d9d-4e56-96cf-8b2baa8643c8"
	delegate := &characterizationSessionDelegate{}
	store := &characterizationSandboxStore{
		session: &domain.Sandbox{Summary: domain.SandboxSummary{
			ID: sandboxID, VMStatus: domain.VMStatusStopped, ProxyPath: "/agent-compose/session/" + sandboxID,
		}},
		proxyState: domain.ProxyState{Enabled: true, Exposed: true, ProxyPath: "/agent-compose/session/" + sandboxID, Token: "token"},
	}
	handler := NewSandboxHandler(delegate, store, &characterizationSandboxRemover{}, nil)

	response, err := handler.GetSandbox(context.Background(), connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: sandboxID}))
	if err != nil {
		t.Fatalf("GetSandbox returned error: %v", err)
	}
	if response.Msg.GetSandbox().GetStatus() != domain.VMStatusStopped || response.Msg.GetSandbox().GetProxyPath() == "" {
		t.Fatalf("GetSandbox response = %#v", response.Msg.GetSandbox())
	}
	if response.Msg.GetSandbox().GetNotebookUrl() != "" {
		t.Fatalf("stopped sandbox notebook URL = %q", response.Msg.GetSandbox().GetNotebookUrl())
	}
	if delegate.proxyCalls != 0 || len(delegate.resumeSessionIDs) != 0 {
		t.Fatalf("GetSandbox proxy calls=%d resume calls=%v, want none", delegate.proxyCalls, delegate.resumeSessionIDs)
	}
}

func TestV2ListSandboxHistoryReturnsLegacyCellsAndEvents(t *testing.T) {
	const sandboxID = "28fed243-4d9d-4e56-96cf-8b2baa8643c8"
	createdAt := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	store := &characterizationSandboxStore{
		session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID}},
		cells:   []domain.NotebookCell{{ID: "cell-1", Type: "agent", Source: "hello", Output: "world", Agent: "codex", CreatedAt: createdAt}},
		events:  []domain.SandboxEvent{{ID: "event-1", Type: "agent.completed", Level: "info", Message: "done", CreatedAt: createdAt}},
	}
	handler := NewSandboxHandler(&characterizationSessionDelegate{}, store, &characterizationSandboxRemover{}, nil)

	response, err := handler.ListSandboxHistory(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxHistoryRequest{SandboxId: sandboxID}))
	if err != nil {
		t.Fatalf("ListSandboxHistory returned error: %v", err)
	}
	if !response.Msg.GetLegacyHistory() || len(response.Msg.GetCells()) != 1 || response.Msg.GetCells()[0].GetOutput() != "world" {
		t.Fatalf("ListSandboxHistory cells = %#v", response.Msg)
	}
	if len(response.Msg.GetEvents()) != 1 || response.Msg.GetEvents()[0].GetMessage() != "done" {
		t.Fatalf("ListSandboxHistory events = %#v", response.Msg.GetEvents())
	}
}

func TestV2SandboxWatchEventProjection(t *testing.T) {
	now := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	cell := domain.NotebookCell{ID: "cell-1", Output: "done", CreatedAt: now}
	event := domain.SandboxEvent{ID: "event-1", Type: "agent.completed", CreatedAt: now}
	tests := []struct {
		input sessions.WatchEvent
		want  agentcomposev2.SandboxWatchEventType
	}{
		{input: sessions.WatchEvent{EventType: sessions.WatchEventTypeSandboxUpdated, Sandbox: &domain.SandboxSummary{ID: "sandbox-1"}}, want: agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_SANDBOX_UPDATED},
		{input: sessions.WatchEvent{EventType: sessions.WatchEventTypeCellStarted, Cell: &cell}, want: agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_CELL_STARTED},
		{input: sessions.WatchEvent{EventType: sessions.WatchEventTypeCellOutput, CellID: "cell-1", Chunk: "part"}, want: agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_CELL_OUTPUT},
		{input: sessions.WatchEvent{EventType: sessions.WatchEventTypeCellCompleted, Cell: &cell}, want: agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_CELL_COMPLETED},
		{input: sessions.WatchEvent{EventType: sessions.WatchEventTypeEventAdded, Event: &event}, want: agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_EVENT_ADDED},
	}
	for _, test := range tests {
		if got := sandboxWatchEventToV2(test.input); got.GetEventType() != test.want {
			t.Fatalf("sandboxWatchEventToV2(%v) = %v, want %v", test.input.EventType, got.GetEventType(), test.want)
		}
	}
}

func TestV2SandboxLifecycleIsIdempotentAndRejectsInvalidState(t *testing.T) {
	sandboxID := identity.NewID(identity.ResourceSandbox, "characterization", "idempotent")
	delegate := &characterizationSessionDelegate{}
	store := &characterizationSandboxStore{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID, VMStatus: domain.VMStatusStopped}}}
	handler := NewSandboxHandler(delegate, store, &characterizationSandboxRemover{}, nil)

	if _, err := handler.StopSandbox(context.Background(), connect.NewRequest(&agentcomposev2.StopSandboxRequest{SandboxId: sandboxID})); err != nil {
		t.Fatalf("idempotent StopSandbox: %v", err)
	}
	if len(delegate.stopSessionIDs) != 0 {
		t.Fatalf("idempotent stop called delegate: %v", delegate.stopSessionIDs)
	}
	store.session.Summary.VMStatus = domain.VMStatusFailed
	if _, err := handler.ResumeSandbox(context.Background(), connect.NewRequest(&agentcomposev2.ResumeSandboxRequest{SandboxId: sandboxID})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("ResumeSandbox failed state code=%v err=%v", connect.CodeOf(err), err)
	}
}

func TestV2ListSandboxesUsesOpaquePagination(t *testing.T) {
	firstID := identity.NewID(identity.ResourceSandbox, "characterization", "list-first")
	secondID := identity.NewID(identity.ResourceSandbox, "characterization", "list-second")
	firstUpdatedAt := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	store := &characterizationSandboxStore{listResults: []domain.SandboxListResult{
		{Sandboxes: []*domain.Sandbox{{Summary: domain.SandboxSummary{ID: firstID, UpdatedAt: firstUpdatedAt}}}, HasMore: true},
		{Sandboxes: []*domain.Sandbox{{Summary: domain.SandboxSummary{ID: secondID, UpdatedAt: firstUpdatedAt.Add(-time.Second)}}}},
	}}
	handler := NewSandboxHandler(&characterizationSessionDelegate{}, store, &characterizationSandboxRemover{}, nil)
	first, err := handler.ListSandboxes(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxesRequest{Limit: 1}))
	if err != nil || first.Msg.GetSandboxes()[0].GetSandboxId() != firstID || first.Msg.GetNextCursor() == "" {
		t.Fatalf("first ListSandboxes() = %#v, err=%v", first, err)
	}
	second, err := handler.ListSandboxes(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxesRequest{Limit: 1, Cursor: first.Msg.GetNextCursor()}))
	if err != nil || second.Msg.GetSandboxes()[0].GetSandboxId() != secondID || second.Msg.GetNextCursor() != "" {
		t.Fatalf("second ListSandboxes() = %#v, err=%v", second, err)
	}
	if len(store.listOptions) != 2 || store.listOptions[1].BeforeID != firstID || !store.listOptions[1].BeforeUpdatedAt.Equal(firstUpdatedAt) {
		t.Fatalf("list options = %#v", store.listOptions)
	}
	if _, err := handler.ListSandboxes(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxesRequest{Cursor: "invalid"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid token code=%v err=%v", connect.CodeOf(err), err)
	}
}

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
	stopSessionIDs   []string
	resumeSessionIDs []string
	proxyCalls       int
}

func (d *characterizationSessionDelegate) ResumeSandbox(_ context.Context, sandboxID string) (*domain.Sandbox, error) {
	d.resumeSessionIDs = append(d.resumeSessionIDs, sandboxID)
	return &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID, VMStatus: domain.VMStatusRunning}}, nil
}

func (d *characterizationSessionDelegate) StopSandbox(_ context.Context, sandboxID string) (*domain.Sandbox, error) {
	d.stopSessionIDs = append(d.stopSessionIDs, sandboxID)
	return &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID, VMStatus: domain.VMStatusStopped}}, nil
}

func (d *characterizationSessionDelegate) GetSandboxProxy(context.Context, string) (SandboxProxy, error) {
	d.proxyCalls++
	return SandboxProxy{}, nil
}

type characterizationSandboxStore struct {
	session     *domain.Sandbox
	getID       string
	removeID    string
	getErr      error
	removeErr   error
	listResults []domain.SandboxListResult
	listOptions []domain.SandboxListOptions
	cells       []domain.NotebookCell
	events      []domain.SandboxEvent
	proxyState  domain.ProxyState
}

func (s *characterizationSandboxStore) GetProxyState(string) (domain.ProxyState, error) {
	return s.proxyState, nil
}

func (s *characterizationSandboxStore) ListCells(context.Context, string) ([]domain.NotebookCell, error) {
	return s.cells, nil
}

func (s *characterizationSandboxStore) ListEvents(context.Context, string) ([]domain.SandboxEvent, error) {
	return s.events, nil
}

func (s *characterizationSandboxStore) ListSandboxes(_ context.Context, options domain.SandboxListOptions) (domain.SandboxListResult, error) {
	s.listOptions = append(s.listOptions, options)
	if len(s.listResults) == 0 {
		return domain.SandboxListResult{}, nil
	}
	result := s.listResults[0]
	s.listResults = s.listResults[1:]
	return result, nil
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
