package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestV2SandboxLifecycleActions(t *testing.T) {
	sandboxID := identity.NewID(identity.ResourceSandbox, "characterization", "lifecycle")
	now := time.Now().UTC().Truncate(time.Second)
	delegate := &characterizationSessionDelegate{}
	store := &characterizationSandboxStore{session: &domain.Sandbox{Summary: domain.SandboxSummary{
		ID: sandboxID, Driver: "docker", VMStatus: domain.VMStatusRunning, CreatedAt: now, UpdatedAt: now, ProxyPath: "/agent-compose/session/" + sandboxID,
	}}}
	handler := NewSandboxHandler(SandboxHandlerDeps{
		Delegate:  delegate,
		Store:     store,
		Remover:   &characterizationSandboxRemover{},
		Dashboard: nil,
	})

	got, err := handler.GetSandbox(context.Background(), connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: sandboxID}))
	if err != nil || got.Msg.GetSandbox().GetStatus() != agentcomposev2.SandboxStatus_SANDBOX_STATUS_RUNNING || got.Msg.GetSandbox().GetProxyPath() == "" || delegate.proxyCalls != 0 {
		t.Fatalf("GetSandbox() = %#v, proxy calls=%d, err=%v", got, delegate.proxyCalls, err)
	}
	stopped, err := handler.StopSandbox(context.Background(), connect.NewRequest(&agentcomposev2.StopSandboxRequest{SandboxId: sandboxID}))
	if err != nil || stopped.Msg.GetSandbox().GetStatus() != agentcomposev2.SandboxStatus_SANDBOX_STATUS_STOPPED || len(delegate.stopSessionIDs) != 1 {
		t.Fatalf("StopSandbox() = %#v, calls=%v, err=%v", stopped, delegate.stopSessionIDs, err)
	}
	store.session.Summary.VMStatus = domain.VMStatusStopped
	resumed, err := handler.ResumeSandbox(context.Background(), connect.NewRequest(&agentcomposev2.ResumeSandboxRequest{SandboxId: sandboxID}))
	if err != nil || resumed.Msg.GetSandbox().GetStatus() != agentcomposev2.SandboxStatus_SANDBOX_STATUS_RUNNING || len(delegate.resumeSessionIDs) != 1 {
		t.Fatalf("ResumeSandbox() = %#v, calls=%v, err=%v", resumed, delegate.resumeSessionIDs, err)
	}
}

func TestV2GetSandboxAcceptsLegacyUUID(t *testing.T) {
	const sandboxID = "28fed243-4d9d-4e56-96cf-8b2baa8643c8"
	store := &characterizationSandboxStore{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID}}}
	handler := NewSandboxHandler(SandboxHandlerDeps{
		Delegate:  &characterizationSessionDelegate{},
		Store:     store,
		Remover:   &characterizationSandboxRemover{},
		Dashboard: nil,
	})

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
	handler := NewSandboxHandler(SandboxHandlerDeps{
		Delegate:  delegate,
		Store:     store,
		Remover:   &characterizationSandboxRemover{},
		Dashboard: nil,
	})

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

func TestGetSandboxDoesNotPrepareProxyForStoppedSandbox(t *testing.T) {
	const sandboxID = "28fed243-4d9d-4e56-96cf-8b2baa8643c8"
	delegate := &characterizationSessionDelegate{}
	store := &characterizationSandboxStore{
		session: &domain.Sandbox{Summary: domain.SandboxSummary{
			ID: sandboxID, VMStatus: domain.VMStatusStopped, ProxyPath: "/agent-compose/session/" + sandboxID,
		}},
		proxyState: domain.ProxyState{Enabled: true, Exposed: true, ProxyPath: "/agent-compose/session/" + sandboxID, Token: "token"},
	}
	handler := NewSandboxHandler(SandboxHandlerDeps{
		Delegate:  delegate,
		Store:     store,
		Remover:   &characterizationSandboxRemover{},
		Dashboard: nil,
	})

	response, err := handler.GetSandbox(context.Background(), connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: sandboxID}))
	if err != nil {
		t.Fatalf("GetSandbox returned error: %v", err)
	}
	if response.Msg.GetSandbox().GetStatus() != agentcomposev2.SandboxStatus_SANDBOX_STATUS_STOPPED || response.Msg.GetSandbox().GetProxyPath() == "" {
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
	handler := NewSandboxHandler(SandboxHandlerDeps{
		Delegate:  &characterizationSessionDelegate{},
		Store:     store,
		Remover:   &characterizationSandboxRemover{},
		Dashboard: nil,
	})

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
		input sandboxes.WatchEvent
		want  agentcomposev2.SandboxWatchEventType
	}{
		{input: sandboxes.WatchEvent{EventType: sandboxes.WatchEventTypeSandboxUpdated, Sandbox: &domain.SandboxSummary{ID: "sandbox-1"}}, want: agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_SANDBOX_UPDATED},
		{input: sandboxes.WatchEvent{EventType: sandboxes.WatchEventTypeCellStarted, Cell: &cell}, want: agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_CELL_STARTED},
		{input: sandboxes.WatchEvent{EventType: sandboxes.WatchEventTypeCellOutput, CellID: "cell-1", Chunk: "part"}, want: agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_CELL_OUTPUT},
		{input: sandboxes.WatchEvent{EventType: sandboxes.WatchEventTypeCellCompleted, Cell: &cell}, want: agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_CELL_COMPLETED},
		{input: sandboxes.WatchEvent{EventType: sandboxes.WatchEventTypeEventAdded, Event: &event}, want: agentcomposev2.SandboxWatchEventType_SANDBOX_WATCH_EVENT_TYPE_EVENT_ADDED},
	}
	for _, test := range tests {
		if got := sandboxWatchEventToV2(test.input); got.GetEventType() != test.want {
			t.Fatalf("sandboxWatchEventToV2(%v) = %v, want %v", test.input.EventType, got.GetEventType(), test.want)
		}
	}
}

func TestV2SandboxWatchCellCarriesTheSameCappedOutputAsAListing(t *testing.T) {
	// A completed cell is published with everything its run accumulated, so the
	// watch stream pushes the same tens of megabytes a listing used to return.
	// Both go through sandboxHistoryCellToV2 and both are capped; a subscriber
	// that needs the head of the log reads it from RunService.FollowRunLogs.
	buildLog := strings.Repeat("a", maxSandboxHistoryCellOutputBytes+4096)
	cell := domain.NotebookCell{ID: "cell-build", Stdout: buildLog, Output: buildLog}

	for _, eventType := range []sandboxes.WatchEventType{sandboxes.WatchEventTypeCellStarted, sandboxes.WatchEventTypeCellCompleted} {
		got := sandboxWatchEventToV2(sandboxes.WatchEvent{EventType: eventType, Cell: &cell}).GetCell()
		if length := len(got.GetOutput()); length != maxSandboxHistoryCellOutputBytes {
			t.Fatalf("%v output length = %d, want %d", eventType, length, maxSandboxHistoryCellOutputBytes)
		}
		if truncated := got.GetOutputTruncatedBytes(); truncated != 4096 {
			t.Fatalf("%v output_truncated_bytes = %d, want 4096", eventType, truncated)
		}
		if got.GetStdout() != "" || got.GetStderr() != "" {
			t.Fatalf("%v still carries a second copy of the stream", eventType)
		}
	}
}

func TestV2SandboxLifecycleIsIdempotentAndRejectsInvalidState(t *testing.T) {
	sandboxID := identity.NewID(identity.ResourceSandbox, "characterization", "idempotent")
	delegate := &characterizationSessionDelegate{}
	store := &characterizationSandboxStore{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID, VMStatus: domain.VMStatusStopped}}}
	handler := NewSandboxHandler(SandboxHandlerDeps{
		Delegate:  delegate,
		Store:     store,
		Remover:   &characterizationSandboxRemover{},
		Dashboard: nil,
	})

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

func TestV2ListSandboxesEmptyPageReturnsTotal(t *testing.T) {
	// A page whose indexed rows were all ghosts comes back empty with HasMore set.
	// The cursor must not be built from the (empty) page, which would panic.
	store := &characterizationSandboxStore{listResults: []domain.SandboxListResult{
		{Sandboxes: nil, TotalCount: 5, HasMore: true},
	}}
	handler := NewSandboxHandler(SandboxHandlerDeps{
		Delegate:  &characterizationSessionDelegate{},
		Store:     store,
		Remover:   &characterizationSandboxRemover{},
		Dashboard: nil,
	})

	resp, err := handler.ListSandboxes(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxesRequest{Limit: 1}))
	if err != nil {
		t.Fatalf("ListSandboxes() error = %v", err)
	}
	if len(resp.Msg.GetSandboxes()) != 0 {
		t.Fatalf("expected empty page, got %d sandboxes", len(resp.Msg.GetSandboxes()))
	}
	if resp.Msg.GetTotal() != 5 {
		t.Fatalf("total = %d, want 5", resp.Msg.GetTotal())
	}
}

func TestV2ListSandboxesUsesOffsetPagination(t *testing.T) {
	firstID := identity.NewID(identity.ResourceSandbox, "characterization", "list-first")
	secondID := identity.NewID(identity.ResourceSandbox, "characterization", "list-second")
	firstUpdatedAt := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	store := &characterizationSandboxStore{listResults: []domain.SandboxListResult{
		{Sandboxes: []*domain.Sandbox{{Summary: domain.SandboxSummary{ID: firstID, UpdatedAt: firstUpdatedAt}}}, TotalCount: 2},
		{Sandboxes: []*domain.Sandbox{{Summary: domain.SandboxSummary{ID: secondID, UpdatedAt: firstUpdatedAt.Add(-time.Second)}}}, TotalCount: 2},
	}}
	handler := NewSandboxHandler(SandboxHandlerDeps{
		Delegate:  &characterizationSessionDelegate{},
		Store:     store,
		Remover:   &characterizationSandboxRemover{},
		Dashboard: nil,
	})
	first, err := handler.ListSandboxes(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxesRequest{Limit: 1}))
	if err != nil || first.Msg.GetSandboxes()[0].GetSandboxId() != firstID || first.Msg.GetTotal() != 2 {
		t.Fatalf("first ListSandboxes() = %#v, err=%v", first, err)
	}
	second, err := handler.ListSandboxes(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxesRequest{Limit: 1, Offset: 1}))
	if err != nil || second.Msg.GetSandboxes()[0].GetSandboxId() != secondID || second.Msg.GetTotal() != 2 {
		t.Fatalf("second ListSandboxes() = %#v, err=%v", second, err)
	}
	if len(store.listOptions) != 2 || store.listOptions[1].Offset != 1 {
		t.Fatalf("list options = %#v", store.listOptions)
	}
}

func TestV2ListSandboxesPassesProjectAndStatusFiltersToStore(t *testing.T) {
	store := &characterizationSandboxStore{}
	handler := NewSandboxHandler(SandboxHandlerDeps{
		Delegate:  &characterizationSessionDelegate{},
		Store:     store,
		Remover:   &characterizationSandboxRemover{},
		Dashboard: nil,
	})

	_, err := handler.ListSandboxes(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxesRequest{
		ProjectId: " project-a ", Status: []agentcomposev2.SandboxStatus{agentcomposev2.SandboxStatus_SANDBOX_STATUS_RUNNING, agentcomposev2.SandboxStatus_SANDBOX_STATUS_STOPPED, agentcomposev2.SandboxStatus_SANDBOX_STATUS_RUNNING},
	}))
	if err != nil {
		t.Fatalf("ListSandboxes() error = %v", err)
	}
	if len(store.listOptions) != 1 {
		t.Fatalf("list options count = %d, want 1", len(store.listOptions))
	}
	options := store.listOptions[0]
	if options.ProjectID != "project-a" || !slices.Equal(options.VMStatuses, []string{domain.VMStatusRunning, domain.VMStatusStopped}) {
		t.Fatalf("list options = %#v", options)
	}
}

func TestV2ListSandboxesRejectsUnknownStatusBeforeStore(t *testing.T) {
	for _, statuses := range [][]agentcomposev2.SandboxStatus{{agentcomposev2.SandboxStatus_SANDBOX_STATUS_UNSPECIFIED}, {agentcomposev2.SandboxStatus_SANDBOX_STATUS_RUNNING, agentcomposev2.SandboxStatus(99)}} {
		t.Run(fmt.Sprint(statuses), func(t *testing.T) {
			store := &characterizationSandboxStore{}
			handler := NewSandboxHandler(SandboxHandlerDeps{
				Delegate:  &characterizationSessionDelegate{},
				Store:     store,
				Remover:   &characterizationSandboxRemover{},
				Dashboard: nil,
			})

			_, err := handler.ListSandboxes(context.Background(), connect.NewRequest(&agentcomposev2.ListSandboxesRequest{Status: statuses}))
			if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "unsupported sandbox status") {
				t.Fatalf("ListSandboxes() code/error = %v / %v", connect.CodeOf(err), err)
			}
			if len(store.listOptions) != 0 {
				t.Fatalf("invalid status reached store with options %#v", store.listOptions)
			}
		})
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
	handler := NewSandboxHandler(SandboxHandlerDeps{
		Delegate:  delegate,
		Store:     store,
		Remover:   remover,
		Dashboard: dashboard,
	})

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
	handler := NewSandboxHandler(SandboxHandlerDeps{
		Delegate:  &characterizationSessionDelegate{},
		Store:     store,
		Remover:   &characterizationSandboxRemover{},
		Dashboard: nil,
	})

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
	handler := NewSandboxHandler(SandboxHandlerDeps{
		Delegate:  &characterizationSessionDelegate{},
		Store:     store,
		Remover:   remover,
		Dashboard: nil,
	})

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
	handler := NewSandboxHandler(SandboxHandlerDeps{
		Delegate:  &characterizationSessionDelegate{},
		Store:     &characterizationSandboxStore{},
		Remover:   &characterizationSandboxRemover{},
		Dashboard: nil,
	})
	for _, sandboxID := range []string{"", "not an id", "../bad"} {
		_, err := handler.RemoveSandbox(context.Background(), connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{SandboxId: sandboxID}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("RemoveSandbox(%q) code = %v, want invalid argument (err=%v)", sandboxID, connect.CodeOf(err), err)
		}
	}

	missingID := identity.NewID(identity.ResourceSandbox, "characterization", "missing")
	missing := NewSandboxHandler(SandboxHandlerDeps{
		Delegate:  &characterizationSessionDelegate{},
		Store:     &characterizationSandboxStore{getErr: errors.New("missing")},
		Remover:   &characterizationSandboxRemover{},
		Dashboard: nil,
	})
	if _, err := missing.RemoveSandbox(context.Background(), connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{SandboxId: missingID})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("RemoveSandbox missing code = %v, want not found (err=%v)", connect.CodeOf(err), err)
	}

	removeErr := errors.New("remove failed")
	removeID := identity.NewID(identity.ResourceSandbox, "characterization", "remove-error")
	failing := NewSandboxHandler(SandboxHandlerDeps{
		Delegate: &characterizationSessionDelegate{},
		Store: &characterizationSandboxStore{
			session:   &domain.Sandbox{Summary: domain.SandboxSummary{ID: removeID, VMStatus: domain.VMStatusStopped}},
			removeErr: removeErr,
		},
		Remover:   &characterizationSandboxRemover{},
		Dashboard: nil,
	})
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
