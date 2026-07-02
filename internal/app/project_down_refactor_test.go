package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"

	driverpkg "agent-compose/pkg/driver"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestProjectDownDisablesSchedulersStopsSessionsPreservesHistory(t *testing.T) {
	testProjectDownDisablesSchedulersStopsSessionsPreservesHistory(t)
}

func TestIntegrationProjectDownDisablesSchedulersStopsSessionsPreservesHistory(t *testing.T) {
	testProjectDownDisablesSchedulersStopsSessionsPreservesHistory(t)
}

func TestE2EProjectDownDisablesSchedulersStopsSessionsPreservesHistory(t *testing.T) {
	testProjectDownDisablesSchedulersStopsSessionsPreservesHistory(t)
}

func testProjectDownDisablesSchedulersStopsSessionsPreservesHistory(t *testing.T) {
	t.Helper()
	store, service, projectID := setupRunCoordinatorProject(t)
	ctx := context.Background()

	schedulers, err := store.ListProjectSchedulers(ctx, projectID)
	if err != nil {
		t.Fatalf("ListProjectSchedulers before down returned error: %v", err)
	}
	if len(schedulers) != 1 || !schedulers[0].Enabled || strings.TrimSpace(schedulers[0].ManagedLoaderID) == "" {
		t.Fatalf("project schedulers before down = %#v", schedulers)
	}
	loader, err := store.GetLoader(ctx, schedulers[0].ManagedLoaderID)
	if err != nil {
		t.Fatalf("GetLoader before down returned error: %v", err)
	}
	if !loader.Summary.Enabled {
		t.Fatalf("managed loader before down is disabled: %#v", loader.Summary)
	}

	runResp, err := service.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "keep session running for down",
		Source:          agentcomposev2.RunSource_RUN_SOURCE_API,
		CleanupPolicy:   agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_KEEP_RUNNING,
		ClientRequestId: "down-service-run",
	}))
	if err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
	}
	runSummary := runResp.Msg.GetRun().GetSummary()
	if runSummary.GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED || strings.TrimSpace(runSummary.GetSessionId()) == "" {
		t.Fatalf("RunAgent summary = %#v", runSummary)
	}
	session, err := service.store.GetSession(ctx, runSummary.GetSessionId())
	if err != nil {
		t.Fatalf("GetSession before down returned error: %v", err)
	}
	if session.Summary.VMStatus != VMStatusRunning {
		t.Fatalf("session status before down = %q, want running", session.Summary.VMStatus)
	}

	downResp, err := service.RemoveProject(ctx, connect.NewRequest(&agentcomposev2.RemoveProjectRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: projectID},
	}))
	if err != nil {
		t.Fatalf("RemoveProject returned error: %v", err)
	}
	if downResp.Msg.GetProject().GetSummary().GetProjectId() != projectID {
		t.Fatalf("RemoveProject project = %#v", downResp.Msg.GetProject().GetSummary())
	}
	if !projectChangesContain(downResp.Msg.GetChanges(), "project_scheduler", schedulers[0].SchedulerID) ||
		!projectChangesContain(downResp.Msg.GetChanges(), "loader", schedulers[0].ManagedLoaderID) ||
		!projectChangesContain(downResp.Msg.GetChanges(), "session", runSummary.GetSessionId()) {
		t.Fatalf("RemoveProject changes = %#v", downResp.Msg.GetChanges())
	}

	disabledScheduler, err := store.GetProjectScheduler(ctx, projectID, schedulers[0].SchedulerID)
	if err != nil {
		t.Fatalf("GetProjectScheduler after down returned error: %v", err)
	}
	if disabledScheduler.Enabled {
		t.Fatalf("project scheduler after down is enabled: %#v", disabledScheduler)
	}
	disabledLoader, err := store.GetLoader(ctx, schedulers[0].ManagedLoaderID)
	if err != nil {
		t.Fatalf("GetLoader after down returned error: %v", err)
	}
	if disabledLoader.Summary.Enabled {
		t.Fatalf("managed loader after down is enabled: %#v", disabledLoader.Summary)
	}
	stoppedSession, err := service.store.GetSession(ctx, runSummary.GetSessionId())
	if err != nil {
		t.Fatalf("GetSession after down returned error: %v", err)
	}
	if stoppedSession.Summary.VMStatus != VMStatusStopped {
		t.Fatalf("session status after down = %q, want stopped", stoppedSession.Summary.VMStatus)
	}
	driver, ok := service.driver.(*fakeSessionDriver)
	if !ok {
		t.Fatalf("service driver = %T, want *fakeSessionDriver", service.driver)
	}
	if len(driver.stopCalls) != 1 || driver.stopCalls[0] != runSummary.GetSessionId() {
		t.Fatalf("StopSessionVM calls = %#v", driver.stopCalls)
	}

	loadedRun, err := service.GetRun(ctx, connect.NewRequest(&agentcomposev2.GetRunRequest{
		ProjectId: projectID,
		RunId:     runSummary.GetRunId(),
	}))
	if err != nil {
		t.Fatalf("GetRun after down returned error: %v", err)
	}
	if loadedRun.Msg.GetRun().GetSummary().GetRunId() != runSummary.GetRunId() {
		t.Fatalf("GetRun after down = %#v", loadedRun.Msg.GetRun().GetSummary())
	}
}

func TestProjectDownRemoveHistoryIsUnimplemented(t *testing.T) {
	testProjectDownRemoveHistoryIsUnimplemented(t)
}

func TestIntegrationProjectDownRemoveHistoryIsUnimplemented(t *testing.T) {
	testProjectDownRemoveHistoryIsUnimplemented(t)
}

func TestE2EProjectDownRemoveHistoryIsUnimplemented(t *testing.T) {
	testProjectDownRemoveHistoryIsUnimplemented(t)
}

func testProjectDownRemoveHistoryIsUnimplemented(t *testing.T) {
	t.Helper()
	_, service, projectID := setupRunCoordinatorProject(t)
	_, err := service.RemoveProject(context.Background(), connect.NewRequest(&agentcomposev2.RemoveProjectRequest{
		Project:       &agentcomposev2.ProjectRef{ProjectId: projectID},
		RemoveHistory: true,
	}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("RemoveProject remove_history error code = %v, want unimplemented; err=%v", connect.CodeOf(err), err)
	}
}

func TestProjectDownContinuesAfterPartialSessionStopFailure(t *testing.T) {
	testProjectDownContinuesAfterPartialSessionStopFailure(t)
}

func TestIntegrationProjectDownContinuesAfterPartialSessionStopFailure(t *testing.T) {
	testProjectDownContinuesAfterPartialSessionStopFailure(t)
}

func TestE2EProjectDownContinuesAfterPartialSessionStopFailure(t *testing.T) {
	testProjectDownContinuesAfterPartialSessionStopFailure(t)
}

func testProjectDownContinuesAfterPartialSessionStopFailure(t *testing.T) {
	t.Helper()
	_, service, projectID := setupRunCoordinatorProject(t)
	ctx := context.Background()
	failedID := runProjectDownTestSession(t, ctx, service, projectID, "down-partial-failed")
	stoppedID := runProjectDownTestSession(t, ctx, service, projectID, "down-partial-stopped")
	foreignSession, err := service.store.CreateSession(ctx, "Foreign project running", "", driverpkg.RuntimeDriverBoxlite, "guest:v1", "", SessionTypeManual, nil, nil,
		[]SessionTag{{Name: "project", Value: "foreign-project"}},
	)
	if err != nil {
		t.Fatalf("CreateSession(foreign) returned error: %v", err)
	}
	foreignSession.Summary.VMStatus = VMStatusRunning
	if err := service.store.UpdateSession(ctx, foreignSession); err != nil {
		t.Fatalf("UpdateSession(foreign) returned error: %v", err)
	}
	driver, ok := service.driver.(*fakeSessionDriver)
	if !ok {
		t.Fatalf("service driver = %T, want *fakeSessionDriver", service.driver)
	}
	driver.stopCalls = nil
	driver.stopHook = func(_ context.Context, session *Session) error {
		if session.Summary.ID == failedID {
			return fmt.Errorf("forced stop failure")
		}
		return nil
	}

	resp, err := service.RemoveProject(ctx, connect.NewRequest(&agentcomposev2.RemoveProjectRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: projectID},
	}))
	if err != nil {
		t.Fatalf("RemoveProject returned error: %v", err)
	}
	failedChange := projectChangeFor(resp.Msg.GetChanges(), "session", failedID)
	if failedChange == nil || failedChange.GetAction() != agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED || !strings.Contains(failedChange.GetMessage(), "forced stop failure") {
		t.Fatalf("failed session change = %#v", failedChange)
	}
	stoppedChange := projectChangeFor(resp.Msg.GetChanges(), "session", stoppedID)
	if stoppedChange == nil || stoppedChange.GetAction() != agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED {
		t.Fatalf("stopped session change = %#v", stoppedChange)
	}
	if projectChangeFor(resp.Msg.GetChanges(), "session", foreignSession.Summary.ID) != nil {
		t.Fatalf("foreign session included in changes: %#v", resp.Msg.GetChanges())
	}
	if !stringSliceContains(driver.stopCalls, failedID) || !stringSliceContains(driver.stopCalls, stoppedID) || stringSliceContains(driver.stopCalls, foreignSession.Summary.ID) {
		t.Fatalf("StopSessionVM calls = %#v", driver.stopCalls)
	}
	assertProjectDownTestSessionStatus(t, ctx, service, failedID, VMStatusRunning)
	assertProjectDownTestSessionStatus(t, ctx, service, stoppedID, VMStatusStopped)
	assertProjectDownTestSessionStatus(t, ctx, service, foreignSession.Summary.ID, VMStatusRunning)
}

func TestProjectDownIsIdempotent(t *testing.T) {
	testProjectDownIsIdempotent(t)
}

func TestIntegrationProjectDownIsIdempotent(t *testing.T) {
	testProjectDownIsIdempotent(t)
}

func TestE2EProjectDownIsIdempotent(t *testing.T) {
	testProjectDownIsIdempotent(t)
}

func testProjectDownIsIdempotent(t *testing.T) {
	t.Helper()
	store, service, projectID := setupRunCoordinatorProject(t)
	ctx := context.Background()
	sessionID := runProjectDownTestSession(t, ctx, service, projectID, "down-idempotent")

	first, err := service.RemoveProject(ctx, connect.NewRequest(&agentcomposev2.RemoveProjectRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: projectID},
	}))
	if err != nil {
		t.Fatalf("first RemoveProject returned error: %v", err)
	}
	if !projectChangesContain(first.Msg.GetChanges(), "session", sessionID) {
		t.Fatalf("first RemoveProject changes = %#v", first.Msg.GetChanges())
	}
	second, err := service.RemoveProject(ctx, connect.NewRequest(&agentcomposev2.RemoveProjectRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: projectID},
	}))
	if err != nil {
		t.Fatalf("second RemoveProject returned error: %v", err)
	}
	if len(second.Msg.GetChanges()) != 0 {
		t.Fatalf("second RemoveProject changes = %#v, want empty idempotent response", second.Msg.GetChanges())
	}
	schedulers, err := store.ListProjectSchedulers(ctx, projectID)
	if err != nil {
		t.Fatalf("ListProjectSchedulers after idempotent down returned error: %v", err)
	}
	for _, scheduler := range schedulers {
		if scheduler.Enabled {
			t.Fatalf("scheduler after repeated down is enabled: %#v", scheduler)
		}
	}
	assertProjectDownTestSessionStatus(t, ctx, service, sessionID, VMStatusStopped)
}

func runProjectDownTestSession(t *testing.T, ctx context.Context, service *Service, projectID, clientRequestID string) string {
	t.Helper()
	resp, err := service.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "keep session running for down",
		Source:          agentcomposev2.RunSource_RUN_SOURCE_API,
		CleanupPolicy:   agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_KEEP_RUNNING,
		ClientRequestId: clientRequestID,
	}))
	if err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
	}
	sessionID := resp.Msg.GetRun().GetSummary().GetSessionId()
	if strings.TrimSpace(sessionID) == "" {
		t.Fatalf("RunAgent session id is empty: %#v", resp.Msg.GetRun().GetSummary())
	}
	return sessionID
}

func assertProjectDownTestSessionStatus(t *testing.T, ctx context.Context, service *Service, sessionID, want string) {
	t.Helper()
	session, err := service.store.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSession(%s) returned error: %v", sessionID, err)
	}
	if session.Summary.VMStatus != want {
		t.Fatalf("session %s status = %q, want %q", sessionID, session.Summary.VMStatus, want)
	}
}

func projectChangesContain(changes []*agentcomposev2.ProjectChange, resourceType, resourceID string) bool {
	return projectChangeFor(changes, resourceType, resourceID) != nil
}

func projectChangeFor(changes []*agentcomposev2.ProjectChange, resourceType, resourceID string) *agentcomposev2.ProjectChange {
	for _, change := range changes {
		if change.GetResourceType() == resourceType && change.GetResourceId() == resourceID {
			return change
		}
	}
	return nil
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
