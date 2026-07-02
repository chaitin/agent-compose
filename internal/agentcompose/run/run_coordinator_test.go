package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func TestRunCoordinatorStateMachineTransitions(t *testing.T) {
	store, service, projectID := setupRunCoordinatorProject(t)
	ctx := context.Background()
	_ = service
	coordinator := NewRunCoordinator(store)
	now := time.Date(2026, 6, 11, 9, 10, 0, 0, time.UTC)
	coordinator.now = func() time.Time {
		now = now.Add(time.Second)
		return now
	}

	run, err := coordinator.BeginRun(ctx, ProjectRunStartRequest{
		ProjectID:       projectID,
		AgentName:       "reviewer",
		Source:          ProjectRunSourceManual,
		Prompt:          "review now",
		ClientRequestID: "request-1",
	})
	if err != nil {
		t.Fatalf("BeginRun returned error: %v", err)
	}
	if run.Status != ProjectRunStatusPending || run.ProjectID != projectID || run.AgentName != "reviewer" || run.ManagedAgentID == "" {
		t.Fatalf("created run = %#v", run)
	}
	if run.Source != ProjectRunSourceManual || run.ProjectName != "demo" || run.ProjectRevision != 1 || run.Driver != "boxlite" || run.ImageRef != "guest:v1" {
		t.Fatalf("created run project/source/runtime fields = %#v", run)
	}

	running, err := coordinator.MarkRunning(ctx, run.RunID, "session-1")
	if err != nil {
		t.Fatalf("MarkRunning returned error: %v", err)
	}
	if running.Status != ProjectRunStatusRunning || running.SessionID != "session-1" || running.StartedAt.IsZero() {
		t.Fatalf("running run = %#v", running)
	}

	succeeded, err := coordinator.MarkSucceeded(ctx, ProjectRunTransitionRequest{
		RunID:      run.RunID,
		Output:     "done",
		ResultJSON: `{"ok":true}`,
	})
	if err != nil {
		t.Fatalf("MarkSucceeded returned error: %v", err)
	}
	if succeeded.Status != ProjectRunStatusSucceeded || succeeded.CompletedAt.IsZero() || succeeded.DurationMs <= 0 || succeeded.Output != "done" || succeeded.ResultJSON != `{"ok":true}` {
		t.Fatalf("succeeded run = %#v", succeeded)
	}
	if _, err := coordinator.MarkRunning(ctx, run.RunID, "session-2"); err == nil || !strings.Contains(err.Error(), "already terminal") {
		t.Fatalf("terminal run accepted transition back to running: %v", err)
	}
}

func TestRunCoordinatorRejectsInvalidTransitionsAndRecordsFailureTerminal(t *testing.T) {
	store, _, projectID := setupRunCoordinatorProject(t)
	ctx := context.Background()
	coordinator := NewRunCoordinator(store)

	run, err := coordinator.BeginRun(ctx, ProjectRunStartRequest{
		ProjectID:       projectID,
		AgentName:       "reviewer",
		Source:          ProjectRunSourceScheduler,
		SchedulerID:     "scheduler-1",
		TriggerID:       "trigger-1",
		ClientRequestID: "request-2",
	})
	if err != nil {
		t.Fatalf("BeginRun returned error: %v", err)
	}
	if _, err := coordinator.MarkSucceeded(ctx, ProjectRunTransitionRequest{RunID: run.RunID}); err == nil || !strings.Contains(err.Error(), "pending -> succeeded") {
		t.Fatalf("pending run accepted direct success: %v", err)
	}
	failed, err := coordinator.MarkFailed(ctx, ProjectRunTransitionRequest{
		RunID: run.RunID,
		Error: "workspace prepare failed",
	})
	if err != nil {
		t.Fatalf("MarkFailed returned error: %v", err)
	}
	if failed.Status != ProjectRunStatusFailed || failed.StartedAt.IsZero() || failed.CompletedAt.IsZero() || failed.Error != "workspace prepare failed" {
		t.Fatalf("failed terminal run = %#v", failed)
	}
	if failed.Source != ProjectRunSourceScheduler || failed.SchedulerID != "scheduler-1" || failed.TriggerID != "trigger-1" {
		t.Fatalf("failed terminal source fields = %#v", failed)
	}
	if _, err := coordinator.MarkCanceled(ctx, ProjectRunTransitionRequest{RunID: run.RunID}); err == nil || !strings.Contains(err.Error(), "already terminal") {
		t.Fatalf("failed terminal run accepted cancel: %v", err)
	}
}

func TestIntegrationRunServiceRunAgentCreatesTerminalSucceededRecord(t *testing.T) {
	testRunServiceRunAgentCreatesTerminalSucceededRecord(t)
}

func TestE2ERunServiceRunAgentCreatesTerminalSucceededRecord(t *testing.T) {
	testRunServiceRunAgentCreatesTerminalSucceededRecord(t)
}

func testRunServiceRunAgentCreatesTerminalSucceededRecord(t *testing.T) {
	store, service, projectID := setupRunCoordinatorProject(t)
	ctx := context.Background()
	resp, err := service.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "review via api",
		Source:          agentcomposev2.RunSource_RUN_SOURCE_API,
		Env:             []*agentcomposev2.EnvVarSpec{{Name: "RUN_SESSION_ENV", Value: "session-value", Secret: true}},
		ClientRequestId: "api-request-1",
	}))
	if err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
	}
	summary := resp.Msg.GetRun().GetSummary()
	if summary.GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED || summary.GetSource() != agentcomposev2.RunSource_RUN_SOURCE_API || summary.GetStartedAt() == "" || summary.GetCompletedAt() == "" {
		t.Fatalf("RunAgent summary = %#v", summary)
	}
	if summary.GetError() != "" {
		t.Fatalf("RunAgent error = %q", summary.GetError())
	}

	loaded, err := service.GetRun(ctx, connect.NewRequest(&agentcomposev2.GetRunRequest{
		RunId:     summary.GetRunId(),
		ProjectId: projectID,
	}))
	if err != nil {
		t.Fatalf("GetRun returned error: %v", err)
	}
	if loaded.Msg.GetRun().GetSummary().GetRunId() != summary.GetRunId() {
		t.Fatalf("GetRun response = %#v", loaded.Msg.GetRun())
	}
	listed, err := service.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{
		ProjectId: projectID,
		Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
		Source:    agentcomposev2.RunSource_RUN_SOURCE_API,
	}))
	if err != nil {
		t.Fatalf("ListRuns returned error: %v", err)
	}
	if len(listed.Msg.GetRuns()) != 1 || listed.Msg.GetRuns()[0].GetRunId() != summary.GetRunId() {
		t.Fatalf("ListRuns response = %#v", listed.Msg.GetRuns())
	}
	stopped, err := service.StopRun(ctx, connect.NewRequest(&agentcomposev2.StopRunRequest{
		RunId:  summary.GetRunId(),
		Reason: "no-op",
	}))
	if err != nil {
		t.Fatalf("StopRun terminal returned error: %v", err)
	}
	if stopped.Msg.GetStopRequested() || stopped.Msg.GetRun().GetSummary().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED {
		t.Fatalf("StopRun terminal response = %#v", stopped.Msg)
	}
	stored, err := store.GetProjectRun(ctx, summary.GetRunId())
	if err != nil {
		t.Fatalf("GetProjectRun returned error: %v", err)
	}
	if stored.Status != ProjectRunStatusSucceeded || stored.ProjectID != projectID || stored.AgentName != "reviewer" || stored.ManagedAgentID == "" {
		t.Fatalf("stored RunAgent run = %#v", stored)
	}
	if !strings.Contains(stored.Output, "loader agent transcript") || stored.ResultJSON == "{}" || stored.ArtifactsDir == "" || stored.LogsPath == "" {
		t.Fatalf("stored RunAgent output/result artifacts = %#v", stored)
	}
	if strings.TrimSpace(stored.SessionID) == "" || stored.SessionID != summary.GetSessionId() {
		t.Fatalf("stored RunAgent session id = %q, summary = %q", stored.SessionID, summary.GetSessionId())
	}
	session, err := service.store.GetSession(ctx, stored.SessionID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if session.Summary.VMStatus != VMStatusStopped {
		t.Fatalf("session status = %q, want stopped", session.Summary.VMStatus)
	}
	env := envItemsByName(session.EnvItems)
	if got := env["RUN_SESSION_ENV"]; got.Value != "session-value" || !got.Secret {
		t.Fatalf("session env RUN_SESSION_ENV = %#v", got)
	}
	for name, value := range map[string]string{
		"project": projectID,
		"agent":   "reviewer",
		"run_id":  stored.RunID,
		"source":  ProjectRunSourceAPI,
	} {
		if !sessionHasTag(session, name, value) {
			t.Fatalf("session tags missing %s=%s: %#v", name, value, session.Summary.Tags)
		}
	}
}

func TestRunServiceRunAgentInjectsProjectAgentCapabilities(t *testing.T) {
	testRunServiceRunAgentInjectsProjectAgentCapabilities(t)
}

func TestE2ERunServiceRunAgentInjectsProjectAgentCapabilities(t *testing.T) {
	testRunServiceRunAgentInjectsProjectAgentCapabilities(t)
}

func testRunServiceRunAgentInjectsProjectAgentCapabilities(t *testing.T) {
	spec := newProjectServiceTestSpec("capset-run", "gpt-test")
	spec.Agents[0].CapsetIds = []string{"xray-dev"}
	store, service, projectID := setupRunPreparationProject(t, spec, t.TempDir())
	catalog := "# Catalog: xray-dev\n\n## gRPC\n\n| Method | Metadata |\n| --- | --- |\n| `/pkg.Service/Call` | `x-octobus-capset=xray-dev, x-octobus-instance=inst` |\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/v1/catalog/xray-dev" && r.URL.Query().Get("format") == "md" {
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(catalog))
			return
		}
		t.Errorf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		http.Error(w, "unexpected", http.StatusBadRequest)
	}))
	defer server.Close()
	service.cap = newTestCapabilityProvider(server.URL, "agent-compose:9100")

	ctx := context.Background()
	resp, err := service.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "run with capabilities",
		ClientRequestId: "capset-run-request",
	}))
	if err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
	}
	summary := resp.Msg.GetRun().GetSummary()
	if summary.GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED || summary.GetSessionId() == "" {
		t.Fatalf("RunAgent summary = %#v", summary)
	}
	stored, err := store.GetProjectRun(ctx, summary.GetRunId())
	if err != nil {
		t.Fatalf("GetProjectRun returned error: %v", err)
	}
	session, err := service.store.GetSession(ctx, stored.SessionID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	env := envItemsByName(session.EnvItems)
	if got := env[capProxyTargetEnvName]; got.Value != "agent-compose:9100" || got.Secret {
		t.Fatalf("session %s = %#v, want visible proxy target", capProxyTargetEnvName, got)
	}
	if got := env[capabilitySessionTokenEnvName]; strings.TrimSpace(got.Value) == "" || !got.Secret {
		t.Fatalf("session %s = %#v, want secret token", capabilitySessionTokenEnvName, got)
	}
	assertStringSliceEqual(t, sessionCapabilityCapsets(session), []string{"xray-dev"}, "run session capset tags")
	guide, err := os.ReadFile(sessionCapabilityGuidePath(session))
	if err != nil {
		t.Fatalf("capability guide not written to MPI catalog: %v", err)
	}
	if !strings.Contains(string(guide), "agent-compose:9100") ||
		!strings.Contains(string(guide), "CAP_GRPC_TARGET") ||
		!strings.Contains(string(guide), "x-octobus-instance=inst") {
		t.Fatalf("capability guide missing proxy/routing info: %s", guide)
	}
}

func TestRunServiceRunAgentRefreshesCapabilitiesOnReusedSession(t *testing.T) {
	testRunServiceRunAgentRefreshesCapabilitiesOnReusedSession(t)
}

func TestE2ERunServiceRunAgentRefreshesCapabilitiesOnReusedSession(t *testing.T) {
	testRunServiceRunAgentRefreshesCapabilitiesOnReusedSession(t)
}

func testRunServiceRunAgentRefreshesCapabilitiesOnReusedSession(t *testing.T) {
	spec := newProjectServiceTestSpec("capset-reuse", "gpt-test")
	spec.Agents[0].CapsetIds = []string{"xray-dev"}
	store, service, projectID := setupRunPreparationProject(t, spec, t.TempDir())
	catalog := "# Catalog: xray-dev\n\n## gRPC\n\n| Method | Metadata |\n| --- | --- |\n| `/pkg.Service/Call` | `x-octobus-capset=xray-dev, x-octobus-instance=inst` |\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/v1/catalog/xray-dev" && r.URL.Query().Get("format") == "md" {
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(catalog))
			return
		}
		t.Errorf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		http.Error(w, "unexpected", http.StatusBadRequest)
	}))
	defer server.Close()
	service.cap = newTestCapabilityProvider(server.URL, "agent-compose:9100")

	ctx := context.Background()
	existing, err := service.store.CreateSession(ctx, "Reusable Capability Session", "", "boxlite", "guest:latest", "", SessionTypeManual, nil, nil,
		[]SessionTag{{Name: "legacy", Value: "true"}})
	if err != nil {
		t.Fatalf("CreateSession existing returned error: %v", err)
	}
	existing.Summary.VMStatus = VMStatusStopped
	if err := service.store.UpdateSession(ctx, existing); err != nil {
		t.Fatalf("UpdateSession existing returned error: %v", err)
	}
	resp, err := service.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		SessionId:       existing.Summary.ID,
		Prompt:          "reuse with capabilities",
		CleanupPolicy:   agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_KEEP_RUNNING,
		ClientRequestId: "capset-reuse-request",
	}))
	if err != nil {
		t.Fatalf("RunAgent reuse returned error: %v", err)
	}
	summary := resp.Msg.GetRun().GetSummary()
	if summary.GetSessionId() != existing.Summary.ID || summary.GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED {
		t.Fatalf("RunAgent reuse summary = %#v", summary)
	}
	loaded, err := service.store.GetSession(ctx, existing.Summary.ID)
	if err != nil {
		t.Fatalf("GetSession existing returned error: %v", err)
	}
	env := envItemsByName(loaded.EnvItems)
	if got := env[capProxyTargetEnvName]; got.Value != "agent-compose:9100" || got.Secret {
		t.Fatalf("reused session %s = %#v, want visible proxy target", capProxyTargetEnvName, got)
	}
	if got := env[capabilitySessionTokenEnvName]; strings.TrimSpace(got.Value) == "" || !got.Secret {
		t.Fatalf("reused session %s = %#v, want secret token", capabilitySessionTokenEnvName, got)
	}
	assertStringSliceEqual(t, sessionCapabilityCapsets(loaded), []string{"xray-dev"}, "reused run session capset tags")
	guide, err := os.ReadFile(sessionCapabilityGuidePath(loaded))
	if err != nil {
		t.Fatalf("capability guide not written for reused session: %v", err)
	}
	if !strings.Contains(string(guide), "agent-compose:9100") ||
		!strings.Contains(string(guide), "CAP_GRPC_TARGET") ||
		!strings.Contains(string(guide), "x-octobus-instance=inst") {
		t.Fatalf("reused session capability guide missing proxy/routing info: %s", guide)
	}
	runs, err := store.ListProjectRunsByOptions(ctx, ProjectRunListOptions{ProjectID: projectID, SessionID: existing.Summary.ID})
	if err != nil {
		t.Fatalf("ListProjectRunsByOptions returned error: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != summary.GetRunId() {
		t.Fatalf("runs for reused session = %#v", runs)
	}
}

func TestIntegrationRunAgentStreamReturnsRealtimeOutput(t *testing.T) {
	testRunAgentStreamReturnsRealtimeOutput(t)
}

func TestE2ERunAgentStreamReturnsRealtimeOutput(t *testing.T) {
	testRunAgentStreamReturnsRealtimeOutput(t)
}

func testRunAgentStreamReturnsRealtimeOutput(t *testing.T) {
	store, service, projectID := setupRunCoordinatorProject(t)
	client, closeServer := newRunServiceTestClient(t, service)
	defer closeServer()
	ctx := context.Background()

	events, err := collectRunAgentStreamEvents(ctx, client, &agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "stream review",
		Source:          agentcomposev2.RunSource_RUN_SOURCE_API,
		ClientRequestId: "stream-success-request",
	})
	if err != nil {
		t.Fatalf("RunAgentStream returned error: %v", err)
	}
	if len(events) < 3 {
		t.Fatalf("RunAgentStream events = %#v", events)
	}
	if events[0].GetEventType() != agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_STARTED {
		t.Fatalf("first stream event = %#v", events[0])
	}
	var outputSeen bool
	var completed *agentcomposev2.RunAgentStreamResponse
	for _, event := range events {
		switch event.GetEventType() {
		case agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT:
			if strings.Contains(event.GetChunk(), "loader agent transcript") && event.GetIsStderr() {
				outputSeen = true
			}
		case agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED:
			completed = event
		}
	}
	if !outputSeen || completed == nil {
		t.Fatalf("RunAgentStream outputSeen=%v completed=%#v events=%#v", outputSeen, completed, events)
	}
	if completed.GetRun().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED || completed.GetRunId() == "" || completed.GetCreatedAt() == "" {
		t.Fatalf("completed stream event = %#v", completed)
	}
	stored, err := store.GetProjectRun(ctx, completed.GetRunId())
	if err != nil {
		t.Fatalf("GetProjectRun stream returned error: %v", err)
	}
	if stored.Status != ProjectRunStatusSucceeded || stored.SessionID == "" || !strings.Contains(stored.Output, "loader agent transcript") {
		t.Fatalf("stored stream run = %#v", stored)
	}
}

func TestIntegrationRunAgentStreamAgentFailurePersistsRun(t *testing.T) {
	testRunAgentStreamAgentFailurePersistsRun(t)
}

func TestE2ERunAgentStreamAgentFailurePersistsRun(t *testing.T) {
	testRunAgentStreamAgentFailurePersistsRun(t)
}

func TestRunAgentStreamEmptyStdoutFailureIncludesProviderStderr(t *testing.T) {
	_, service, projectID := setupRunCoordinatorProject(t)
	runtime := runServiceFakeRuntime(t, service)
	runtime.agentExitCode = 1
	runtime.agentNoPayload = true
	runtime.agentStderr = `Codex provider config error: wire_api = "chat" is no longer supported`
	client, closeServer := newRunServiceTestClient(t, service)
	defer closeServer()
	ctx := context.Background()

	events, err := collectRunAgentStreamEvents(ctx, client, &agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "trigger provider config failure",
		ClientRequestId: "stream-agent-empty-stdout-request",
	})
	if err != nil {
		t.Fatalf("RunAgentStream empty stdout failure returned RPC error: %v", err)
	}
	completed := lastRunAgentStreamEvent(events, agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED)
	if completed == nil || completed.GetRun().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_FAILED {
		t.Fatalf("completed failure event = %#v events=%#v", completed, events)
	}
	if !strings.Contains(completed.GetRun().GetError(), "wire_api") || !strings.Contains(completed.GetRun().GetError(), "chat") {
		t.Fatalf("completed failure error = %q, want provider stderr", completed.GetRun().GetError())
	}
	session, err := service.store.GetSession(ctx, completed.GetRun().GetSessionId())
	if err != nil {
		t.Fatalf("GetSession failure returned error: %v", err)
	}
	cells, err := service.store.ListCells(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListCells failure returned error: %v", err)
	}
	if len(cells) == 0 || !strings.Contains(cells[len(cells)-1].Stderr, "wire_api") {
		t.Fatalf("failed cell stderr missing provider error: %#v", cells)
	}
}

func testRunAgentStreamAgentFailurePersistsRun(t *testing.T) {
	store, service, projectID := setupRunCoordinatorProject(t)
	runServiceFakeRuntime(t, service).agentExitCode = 7
	client, closeServer := newRunServiceTestClient(t, service)
	defer closeServer()
	ctx := context.Background()

	events, err := collectRunAgentStreamEvents(ctx, client, &agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "stream failure",
		ClientRequestId: "stream-agent-failure-request",
	})
	if err != nil {
		t.Fatalf("RunAgentStream failure returned RPC error: %v", err)
	}
	completed := lastRunAgentStreamEvent(events, agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED)
	if completed == nil || completed.GetRun().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_FAILED || completed.GetRun().GetExitCode() != 7 || completed.GetRun().GetSessionId() == "" {
		t.Fatalf("completed failure event = %#v events=%#v", completed, events)
	}
	if !strings.Contains(completed.GetRun().GetError(), "agent execution failed") {
		t.Fatalf("completed failure error = %q", completed.GetRun().GetError())
	}
	stored, err := store.GetProjectRun(ctx, completed.GetRunId())
	if err != nil {
		t.Fatalf("GetProjectRun failure returned error: %v", err)
	}
	if stored.Status != ProjectRunStatusFailed || stored.ExitCode != 7 || stored.SessionID != completed.GetRun().GetSessionId() || stored.ArtifactsDir == "" {
		t.Fatalf("stored failed stream run = %#v", stored)
	}
}

func TestRunAgentStreamSendFailurePersistsTerminalRun(t *testing.T) {
	store, service, projectID := setupRunCoordinatorProject(t)
	ctx := context.Background()
	var outputAttempts int
	sink := projectRunStreamSink{
		send: func(resp *agentcomposev2.RunAgentStreamResponse) error {
			if resp.GetEventType() == agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT {
				outputAttempts++
				return fmt.Errorf("%w: %w", errRunAgentStreamSend, fmt.Errorf("client stopped reading"))
			}
			return nil
		},
	}
	run, execErr, err := service.runProjectAgent(ctx, &agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "stream interruption",
		ClientRequestId: "stream-send-failure-request",
	}, &sink)
	if err != nil {
		t.Fatalf("runProjectAgent returned control-plane error: %v", err)
	}
	if !errors.Is(execErr, errRunAgentStreamSend) {
		t.Fatalf("runProjectAgent execErr = %v, want stream send failure", execErr)
	}
	if outputAttempts != 1 {
		t.Fatalf("output send attempts = %d, want 1", outputAttempts)
	}
	if run.Status != ProjectRunStatusFailed || run.SessionID == "" || run.ExitCode == 0 || !strings.Contains(run.Error, "client stopped reading") {
		t.Fatalf("stream send failure run = %#v", run)
	}
	stored, err := store.GetProjectRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetProjectRun send failure returned error: %v", err)
	}
	if stored.Status != ProjectRunStatusFailed || stored.CompletedAt.IsZero() || stored.SessionID != run.SessionID {
		t.Fatalf("stored stream send failure run = %#v", stored)
	}
}

func TestRunAgentContextCancelPersistsTerminalRun(t *testing.T) {
	store, service, projectID := setupRunCoordinatorProject(t)
	runtime := runServiceFakeRuntime(t, service)
	runtime.agentWaitForContext = true
	runtime.agentStdout = "partial agent output\n"
	runtime.agentStderr = "agent stderr before cancel\n"
	runtime.agentOutput = runtime.agentStdout + runtime.agentStderr

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var outputAttempts int
	sink := projectRunStreamSink{
		send: func(resp *agentcomposev2.RunAgentStreamResponse) error {
			if resp.GetEventType() == agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT {
				outputAttempts++
				cancel()
			}
			return nil
		},
	}
	run, execErr, err := service.runProjectAgent(ctx, &agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "cancel while agent is running",
		ClientRequestId: "agent-context-cancel-request",
	}, &sink)
	if err != nil {
		t.Fatalf("runProjectAgent returned control-plane error: %v", err)
	}
	if !errors.Is(execErr, context.Canceled) {
		t.Fatalf("runProjectAgent execErr = %v, want context canceled", execErr)
	}
	if outputAttempts != 1 {
		t.Fatalf("output send attempts = %d, want 1", outputAttempts)
	}
	if run.Status != ProjectRunStatusFailed || run.SessionID == "" || run.CompletedAt.IsZero() || !strings.Contains(run.Error, "context canceled") {
		t.Fatalf("canceled run = %#v", run)
	}
	stored, err := store.GetProjectRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("GetProjectRun canceled run returned error: %v", err)
	}
	if stored.Status != ProjectRunStatusFailed || stored.CompletedAt.IsZero() || stored.SessionID != run.SessionID || !strings.Contains(stored.Error, "context canceled") {
		t.Fatalf("stored canceled run = %#v", stored)
	}
}

func TestRunAgentSessionEnvProviderUsesAgentDefinitionModelAfterSessionReload(t *testing.T) {
	ctx := context.Background()
	t.Setenv("LLM_API_ENDPOINT", "")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("LLM_MODEL", "")
	spec := newProjectServiceTestSpec("session-provider-env", "session-agent-model")
	spec.Variables = []*agentcomposev2.EnvVarSpec{
		{Name: "LLM_API_ENDPOINT", Value: "https://session-openai.example.invalid/v1"},
		{Name: "LLM_API_KEY", Value: "session-provider-key", Secret: true},
	}
	store, service, projectID := setupRunPreparationProject(t, spec, t.TempDir())
	service.config.RuntimeBaseURL = "http://agent-compose.test"

	resp, err := service.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "use session provider env",
		ClientRequestId: "session-provider-env-request",
	}))
	if err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
	}
	summary := resp.Msg.GetRun().GetSummary()
	if summary.GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED || summary.GetSessionId() == "" {
		t.Fatalf("RunAgent summary = %#v", summary)
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMProviders returned error: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("provider count = %d, want 1: %#v", len(providers), providers)
	}
	if providers[0].APIKey != "session-provider-key" || providers[0].BaseURL != "https://session-openai.example.invalid/v1" || providers[0].Scope != llmProviderScopeSessionEnv {
		t.Fatalf("provider was not bootstrapped from session env: %#v", providers[0])
	}
	models, err := store.ListEnabledLLMModels(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMModels returned error: %v", err)
	}
	if len(models) != 1 || models[0].ID != "session-agent-model" {
		t.Fatalf("models = %#v, want session-agent-model", models)
	}
	session, err := service.store.GetSession(ctx, summary.GetSessionId())
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	for _, item := range session.EnvItems {
		if llmProviderKeyName(item.Name) || strings.Contains(item.Value, "session-provider-key") {
			t.Fatalf("provider key leaked into persisted session env: %#v", session.EnvItems)
		}
	}
}

func TestIntegrationRunAgentCleanupFailureRecordsRunCleanupError(t *testing.T) {
	testRunAgentCleanupFailureRecordsRunCleanupError(t)
}

func TestE2ERunAgentCleanupFailureRecordsRunCleanupError(t *testing.T) {
	testRunAgentCleanupFailureRecordsRunCleanupError(t)
}

func testRunAgentCleanupFailureRecordsRunCleanupError(t *testing.T) {
	store, service, projectID := setupRunCoordinatorProject(t)
	driver, ok := service.driver.(*fakeSessionDriver)
	if !ok {
		t.Fatalf("service driver = %T, want *fakeSessionDriver", service.driver)
	}
	driver.stopHook = func(context.Context, *Session) error {
		return fmt.Errorf("stop boom")
	}
	ctx := context.Background()
	resp, err := service.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "cleanup failure",
		ClientRequestId: "cleanup-failure-request",
	}))
	if err != nil {
		t.Fatalf("RunAgent cleanup failure returned error: %v", err)
	}
	run := resp.Msg.GetRun()
	if run.GetSummary().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED || !strings.Contains(run.GetCleanupError(), "stop boom") {
		t.Fatalf("RunAgent cleanup failure response = %#v", run)
	}
	stored, err := store.GetProjectRun(ctx, run.GetSummary().GetRunId())
	if err != nil {
		t.Fatalf("GetProjectRun cleanup failure returned error: %v", err)
	}
	if stored.Status != ProjectRunStatusSucceeded || !strings.Contains(stored.CleanupError, "stop boom") {
		t.Fatalf("stored cleanup failure run = %#v", stored)
	}
	session, err := service.store.GetSession(ctx, stored.SessionID)
	if err != nil {
		t.Fatalf("GetSession cleanup failure returned error: %v", err)
	}
	if session.Summary.VMStatus != VMStatusRunning {
		t.Fatalf("cleanup failure session status = %q, want running", session.Summary.VMStatus)
	}
}

func TestIntegrationManagedSchedulerAgentUsesProjectRunPipeline(t *testing.T) {
	testManagedSchedulerAgentUsesProjectRunPipeline(t)
}

func TestE2EManagedSchedulerAgentUsesProjectRunPipeline(t *testing.T) {
	testManagedSchedulerAgentUsesProjectRunPipeline(t)
}

func TestManagedSchedulerManualRunPreservesProjectSecretEnv(t *testing.T) {
	spec := newProjectServiceTestSpec("scheduler-secret", "gpt-test")
	spec.Agents[0].Env = []*agentcomposev2.EnvVarSpec{
		{Name: "SAFELINE_API_TOKEN", Value: "valid-token", Secret: true},
	}
	store, service, projectID := setupRunPreparationProject(t, spec, t.TempDir())
	manager := newRunServiceLoaderManager(service)
	ctx := context.Background()
	schedulers, err := store.ListProjectSchedulers(ctx, projectID)
	if err != nil {
		t.Fatalf("ListProjectSchedulers returned error: %v", err)
	}
	if len(schedulers) == 0 {
		t.Fatalf("expected project scheduler")
	}
	loader, err := store.GetLoader(ctx, schedulers[0].ManagedLoaderID)
	if err != nil {
		t.Fatalf("GetLoader managed scheduler returned error: %v", err)
	}
	if len(loader.Triggers) == 0 {
		t.Fatalf("managed loader has no triggers: %#v", loader)
	}

	_, err = manager.RunNow(ctx, loader.Summary.ID, loader.Triggers[0].ID, `{}`, 0)
	if err != nil {
		t.Fatalf("RunNow manual managed scheduler returned error: %v", err)
	}
	runs, err := store.ListProjectRunsByOptions(ctx, ProjectRunListOptions{
		ProjectID:   projectID,
		Source:      ProjectRunSourceScheduler,
		SchedulerID: loader.Summary.ManagedSchedulerID,
	})
	if err != nil {
		t.Fatalf("ListProjectRunsByOptions scheduler returned error: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("scheduler project runs = %#v", runs)
	}
	session, err := service.store.GetSession(ctx, runs[0].SessionID)
	if err != nil {
		t.Fatalf("GetSession scheduler manual run returned error: %v", err)
	}
	env := envItemsByName(session.EnvItems)
	if got := env["SAFELINE_API_TOKEN"]; got.Value != "valid-token" || !got.Secret {
		t.Fatalf("SAFELINE_API_TOKEN env = %#v, want preserved secret value", got)
	}
}

func testManagedSchedulerAgentUsesProjectRunPipeline(t *testing.T) {
	store, service, projectID := setupRunCoordinatorProject(t)
	manager := newRunServiceLoaderManager(service)
	ctx := context.Background()
	schedulers, err := store.ListProjectSchedulers(ctx, projectID)
	if err != nil {
		t.Fatalf("ListProjectSchedulers returned error: %v", err)
	}
	if len(schedulers) == 0 {
		t.Fatalf("expected project scheduler")
	}
	loader, err := store.GetLoader(ctx, schedulers[0].ManagedLoaderID)
	if err != nil {
		t.Fatalf("GetLoader managed scheduler returned error: %v", err)
	}
	if len(loader.Triggers) == 0 {
		t.Fatalf("managed loader has no triggers: %#v", loader)
	}
	host := &loaderRunHost{
		manager: manager,
		loader:  loader,
		run:     &LoaderRunSummary{ID: "loader-run-managed-project", LoaderID: loader.Summary.ID, TriggerID: loader.Triggers[0].ID},
	}
	result, err := host.Agent(ctx, "scheduled project prompt", LoaderAgentRequest{})
	if err != nil {
		t.Fatalf("managed scheduler Agent returned error: %v", err)
	}
	if !result.Success || result.SessionID == "" || result.CellID == "" || !strings.Contains(result.Output, "loader agent transcript") {
		t.Fatalf("managed scheduler result = %#v", result)
	}
	runs, err := store.ListProjectRunsByOptions(ctx, ProjectRunListOptions{
		ProjectID:   projectID,
		Source:      ProjectRunSourceScheduler,
		SchedulerID: loader.Summary.ManagedSchedulerID,
	})
	if err != nil {
		t.Fatalf("ListProjectRunsByOptions scheduler returned error: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("scheduler project runs = %#v", runs)
	}
	run := runs[0]
	if run.Status != ProjectRunStatusSucceeded || run.TriggerID != loader.Triggers[0].ID || run.SessionID != result.SessionID {
		t.Fatalf("scheduler project run = %#v", run)
	}
	session, err := service.store.GetSession(ctx, run.SessionID)
	if err != nil {
		t.Fatalf("GetSession scheduler run returned error: %v", err)
	}
	if session.Summary.VMStatus != VMStatusStopped {
		t.Fatalf("scheduler project session status = %q, want stopped", session.Summary.VMStatus)
	}
	for name, value := range map[string]string{
		"project":      projectID,
		"agent":        "reviewer",
		"run_id":       run.RunID,
		"source":       ProjectRunSourceScheduler,
		"scheduler_id": loader.Summary.ManagedSchedulerID,
	} {
		if !sessionHasTag(session, name, value) {
			t.Fatalf("scheduler session tags missing %s=%s: %#v", name, value, session.Summary.Tags)
		}
	}
	events, err := store.ListLoaderEvents(ctx, loader.Summary.ID, 20)
	if err != nil {
		t.Fatalf("ListLoaderEvents scheduler returned error: %v", err)
	}
	if !loaderEventsContain(events, "loader.agent.completed") {
		t.Fatalf("loader events missing agent completion: %#v", events)
	}
}

func TestRunServiceStopRunCancelsPendingRun(t *testing.T) {
	store, service, projectID := setupRunCoordinatorProject(t)
	ctx := context.Background()
	coordinator := NewRunCoordinator(store)
	run, err := coordinator.BeginRun(ctx, ProjectRunStartRequest{
		ProjectID:       projectID,
		AgentName:       "reviewer",
		Source:          ProjectRunSourceManual,
		ClientRequestID: "cancel-request-1",
	})
	if err != nil {
		t.Fatalf("BeginRun returned error: %v", err)
	}
	stopped, err := service.StopRun(ctx, connect.NewRequest(&agentcomposev2.StopRunRequest{
		RunId:  run.RunID,
		Reason: "user canceled",
	}))
	if err != nil {
		t.Fatalf("StopRun returned error: %v", err)
	}
	summary := stopped.Msg.GetRun().GetSummary()
	if !stopped.Msg.GetStopRequested() || summary.GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_CANCELED || summary.GetStartedAt() == "" || summary.GetCompletedAt() == "" {
		t.Fatalf("StopRun pending response = %#v", stopped.Msg)
	}
	if summary.GetError() != "user canceled" {
		t.Fatalf("StopRun reason = %q", summary.GetError())
	}
}

func TestIntegrationRunServiceRunAgentReusesSessionAndPreservesTags(t *testing.T) {
	testRunServiceRunAgentReusesSessionAndPreservesTags(t)
}

func TestE2ERunServiceRunAgentReusesSessionAndPreservesTags(t *testing.T) {
	testRunServiceRunAgentReusesSessionAndPreservesTags(t)
}

func testRunServiceRunAgentReusesSessionAndPreservesTags(t *testing.T) {
	store, service, projectID := setupRunCoordinatorProject(t)
	ctx := context.Background()
	existing, err := service.store.CreateSession(ctx, "Reusable Project Session", "", "boxlite", "guest:latest", "", SessionTypeManual, nil, nil,
		[]SessionTag{{Name: "legacy", Value: "true"}})
	if err != nil {
		t.Fatalf("CreateSession existing returned error: %v", err)
	}
	existing.Summary.VMStatus = VMStatusStopped
	if err := service.store.UpdateSession(ctx, existing); err != nil {
		t.Fatalf("UpdateSession existing returned error: %v", err)
	}
	resp, err := service.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		SessionId:       existing.Summary.ID,
		Prompt:          "reuse this session",
		CleanupPolicy:   agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_KEEP_RUNNING,
		ClientRequestId: "reuse-request",
	}))
	if err != nil {
		t.Fatalf("RunAgent reuse returned error: %v", err)
	}
	summary := resp.Msg.GetRun().GetSummary()
	if summary.GetSessionId() != existing.Summary.ID || summary.GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED {
		t.Fatalf("RunAgent reuse summary = %#v", summary)
	}
	loaded, err := service.store.GetSession(ctx, existing.Summary.ID)
	if err != nil {
		t.Fatalf("GetSession existing returned error: %v", err)
	}
	if loaded.Summary.VMStatus != VMStatusRunning {
		t.Fatalf("reused session status = %q, want running", loaded.Summary.VMStatus)
	}
	for name, value := range map[string]string{
		"legacy":  "true",
		"project": projectID,
		"agent":   "reviewer",
		"run_id":  summary.GetRunId(),
		"source":  ProjectRunSourceManual,
	} {
		if !sessionHasTag(loaded, name, value) {
			t.Fatalf("reused session tags missing %s=%s: %#v", name, value, loaded.Summary.Tags)
		}
	}
	runs, err := store.ListProjectRunsByOptions(ctx, ProjectRunListOptions{ProjectID: projectID, SessionID: existing.Summary.ID})
	if err != nil {
		t.Fatalf("ListProjectRunsByOptions returned error: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != summary.GetRunId() {
		t.Fatalf("runs for reused session = %#v", runs)
	}
}

func TestIntegrationRunServiceSessionStartFailureMarksRunFailed(t *testing.T) {
	testRunServiceSessionStartFailureMarksRunFailed(t)
}

func TestE2ERunServiceSessionStartFailureMarksRunFailed(t *testing.T) {
	testRunServiceSessionStartFailureMarksRunFailed(t)
}

func testRunServiceSessionStartFailureMarksRunFailed(t *testing.T) {
	store, service, projectID := setupRunCoordinatorProject(t)
	driver, ok := service.driver.(*fakeSessionDriver)
	if !ok {
		t.Fatalf("service driver = %T, want *fakeSessionDriver", service.driver)
	}
	driver.startHook = func(context.Context, *Session) error {
		return fmt.Errorf("boom")
	}
	ctx := context.Background()
	resp, err := service.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		ClientRequestId: "start-failure-request",
	}))
	if err != nil {
		t.Fatalf("RunAgent start failure returned error: %v", err)
	}
	summary := resp.Msg.GetRun().GetSummary()
	if summary.GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_FAILED || strings.TrimSpace(summary.GetSessionId()) == "" {
		t.Fatalf("RunAgent start failure summary = %#v", summary)
	}
	if !strings.Contains(summary.GetError(), "session start failed") || !strings.Contains(summary.GetError(), "boom") {
		t.Fatalf("RunAgent start failure error = %q", summary.GetError())
	}
	stored, err := store.GetProjectRun(ctx, summary.GetRunId())
	if err != nil {
		t.Fatalf("GetProjectRun returned error: %v", err)
	}
	if stored.SessionID != summary.GetSessionId() || stored.Status != ProjectRunStatusFailed {
		t.Fatalf("stored start failure run = %#v", stored)
	}
	session, err := service.store.GetSession(ctx, summary.GetSessionId())
	if err != nil {
		t.Fatalf("GetSession start failure returned error: %v", err)
	}
	if session.Summary.VMStatus != VMStatusFailed {
		t.Fatalf("session start failure status = %q, want failed", session.Summary.VMStatus)
	}
}

func TestProjectRunSessionTagsIncludeSchedulerAndMergeAdditively(t *testing.T) {
	run := ProjectRunRecord{
		RunID:       "run-1",
		ProjectID:   "project-1",
		AgentName:   "reviewer",
		Source:      ProjectRunSourceScheduler,
		SchedulerID: "scheduler-1",
	}
	tags := projectRunSessionTags(run)
	for name, value := range map[string]string{
		"project":      "project-1",
		"agent":        "reviewer",
		"run_id":       "run-1",
		"source":       ProjectRunSourceScheduler,
		"scheduler_id": "scheduler-1",
	} {
		if !sessionTagsContain(tags, name, value) {
			t.Fatalf("projectRunSessionTags missing %s=%s: %#v", name, value, tags)
		}
	}
	merged := mergeSessionTags([]SessionTag{{Name: "source", Value: "agent"}, {Name: "legacy", Value: "true"}}, tags)
	for _, want := range []SessionTag{
		{Name: "source", Value: "agent"},
		{Name: "source", Value: ProjectRunSourceScheduler},
		{Name: "legacy", Value: "true"},
		{Name: "scheduler_id", Value: "scheduler-1"},
	} {
		if !sessionTagsContain(merged, want.Name, want.Value) {
			t.Fatalf("merged tags missing %#v: %#v", want, merged)
		}
	}
}

func TestRunPreparationMergesEnvPrecedenceAndSecrets(t *testing.T) {
	spec := newProjectServiceTestSpec("demo", "gpt-test")
	spec.Variables = []*agentcomposev2.EnvVarSpec{
		{Name: "SHARED", Value: "project"},
		{Name: "PROJECT_ONLY", Value: "project-secret", Secret: true},
	}
	spec.Agents[0].Env = []*agentcomposev2.EnvVarSpec{
		{Name: "SHARED", Value: "agent-secret", Secret: true},
		{Name: "AGENT_ONLY", Value: "agent"},
	}
	store, service, projectID := setupRunPreparationProject(t, spec, t.TempDir())
	ctx := context.Background()
	if _, err := store.ReplaceGlobalEnv(ctx, []SessionEnvVar{
		{Name: "SHARED", Value: "global-secret", Secret: true},
		{Name: "GLOBAL_ONLY", Value: "global", Secret: true},
	}); err != nil {
		t.Fatalf("ReplaceGlobalEnv returned error: %v", err)
	}
	run := beginRunPreparationTestRun(t, store, projectID, "env-request")
	prepared, err := service.prepareProjectRun(ctx, run, []*agentcomposev2.EnvVarSpec{
		{Name: "SHARED", Value: "run", Secret: false},
		{Name: "RUN_ONLY", Value: "run-secret", Secret: true},
	})
	if err != nil {
		t.Fatalf("prepareProjectRun returned error: %v", err)
	}
	env := envItemsByName(prepared.EnvItems)
	if got := env["SHARED"]; got.Value != "run" || got.Secret {
		t.Fatalf("SHARED env = %#v, want run value with non-secret flag", got)
	}
	for name, want := range map[string]SessionEnvVar{
		"GLOBAL_ONLY":  {Name: "GLOBAL_ONLY", Value: "global", Secret: true},
		"PROJECT_ONLY": {Name: "PROJECT_ONLY", Value: "project-secret", Secret: true},
		"AGENT_ONLY":   {Name: "AGENT_ONLY", Value: "agent"},
		"RUN_ONLY":     {Name: "RUN_ONLY", Value: "run-secret", Secret: true},
	} {
		if got := env[name]; got != want {
			t.Fatalf("%s env = %#v, want %#v", name, got, want)
		}
	}
}

func TestRunPreparationMaterializesLocalWorkspaceSnapshot(t *testing.T) {
	projectDir := t.TempDir()
	writeTestFile(t, filepath.Join(projectDir, "project-src", "project.txt"), "project\n")
	writeTestFile(t, filepath.Join(projectDir, "agent-src", "agent.txt"), "agent\n")
	spec := newProjectServiceTestSpec("demo", "gpt-test")
	spec.Workspace = &agentcomposev2.WorkspaceSpec{Provider: "local", Path: "project-src"}
	spec.Agents[0].Workspace = &agentcomposev2.WorkspaceSpec{Provider: "local", Path: "agent-src"}
	store, service, projectID := setupRunPreparationProject(t, spec, projectDir)
	run := beginRunPreparationTestRun(t, store, projectID, "local-request")
	prepared, err := service.prepareProjectRun(context.Background(), run, nil)
	if err != nil {
		t.Fatalf("prepareProjectRun local workspace returned error: %v", err)
	}
	if prepared.WorkspaceConfig == nil || prepared.WorkspaceConfig.Type != "file" || prepared.Workspace == nil || prepared.Workspace.Type != "file" {
		t.Fatalf("prepared local workspace = %#v / %#v", prepared.WorkspaceConfig, prepared.Workspace)
	}
	contentRoot, err := fileWorkspaceContentRoot(service.config, *prepared.WorkspaceConfig)
	if err != nil {
		t.Fatalf("fileWorkspaceContentRoot returned error: %v", err)
	}
	assertFileContent(t, filepath.Join(contentRoot, "agent.txt"), "agent\n")
	if _, err := os.Stat(filepath.Join(contentRoot, "project.txt")); !os.IsNotExist(err) {
		t.Fatalf("agent workspace did not override project workspace, project.txt stat err = %v", err)
	}
}

func TestRunPreparationMapsGitWorkspace(t *testing.T) {
	spec := newProjectServiceTestSpec("demo", "gpt-test")
	spec.Workspace = &agentcomposev2.WorkspaceSpec{
		Provider: "git",
		Url:      "https://example.test/repo.git",
		Branch:   "main",
		Path:     "vendor/repo",
	}
	store, service, projectID := setupRunPreparationProject(t, spec, t.TempDir())
	run := beginRunPreparationTestRun(t, store, projectID, "git-request")
	prepared, err := service.prepareProjectRun(context.Background(), run, nil)
	if err != nil {
		t.Fatalf("prepareProjectRun git workspace returned error: %v", err)
	}
	if prepared.WorkspaceConfig == nil || prepared.WorkspaceConfig.Type != "git" || prepared.Workspace == nil {
		t.Fatalf("prepared git workspace = %#v / %#v", prepared.WorkspaceConfig, prepared.Workspace)
	}
	var cfg gitWorkspaceConfig
	if err := json.Unmarshal([]byte(prepared.WorkspaceConfig.ConfigJSON), &cfg); err != nil {
		t.Fatalf("decode git workspace config: %v", err)
	}
	if cfg.URL != "https://example.test/repo.git" || cfg.Branch != "main" || cfg.CloneTarget != "vendor/repo" {
		t.Fatalf("git workspace config = %#v", cfg)
	}
}

func TestIntegrationRunServiceWorkspacePreparationFailureMarksRunFailed(t *testing.T) {
	testRunServiceWorkspacePreparationFailureMarksRunFailed(t)
}

func TestE2ERunServiceWorkspacePreparationFailureMarksRunFailed(t *testing.T) {
	testRunServiceWorkspacePreparationFailureMarksRunFailed(t)
}

func testRunServiceWorkspacePreparationFailureMarksRunFailed(t *testing.T) {
	projectDir := t.TempDir()
	spec := newProjectServiceTestSpec("demo", "gpt-test")
	spec.Workspace = &agentcomposev2.WorkspaceSpec{Provider: "local", Path: "missing"}
	store, service, projectID := setupRunPreparationProject(t, spec, projectDir)
	ctx := context.Background()
	resp, err := service.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Source:          agentcomposev2.RunSource_RUN_SOURCE_MANUAL,
		ClientRequestId: "bad-workspace-request",
	}))
	if err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
	}
	summary := resp.Msg.GetRun().GetSummary()
	if summary.GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_FAILED || summary.GetStartedAt() == "" || summary.GetCompletedAt() == "" {
		t.Fatalf("RunAgent summary = %#v", summary)
	}
	if !strings.Contains(summary.GetError(), "workspace preparation failed") || !strings.Contains(summary.GetError(), "local workspace source") {
		t.Fatalf("RunAgent error = %q", summary.GetError())
	}
	stored, err := store.GetProjectRun(ctx, summary.GetRunId())
	if err != nil {
		t.Fatalf("GetProjectRun returned error: %v", err)
	}
	if stored.Status != ProjectRunStatusFailed || stored.Error != summary.GetError() {
		t.Fatalf("stored run = %#v, summary error = %q", stored, summary.GetError())
	}
}

func setupRunCoordinatorProject(t *testing.T) (*ConfigStore, *Service, string) {
	t.Helper()
	return setupRunPreparationProject(t, newProjectServiceTestSpec("demo", "gpt-test"), t.TempDir())
}

func setupRunPreparationProject(t *testing.T, spec *agentcomposev2.ProjectSpec, projectDir string) (*ConfigStore, *Service, string) {
	t.Helper()
	store := newTestConfigStore(t)
	service := newProjectServiceTestService(t, store)
	service.config.DataRoot = filepath.Join(t.TempDir(), "data")
	service.config.SessionRoot = filepath.Join(t.TempDir(), "sessions")
	service.config.JupyterProxyBasePath = "/agent-compose/session"
	service.config.JupyterGuestPort = 8888
	service.store = &Store{config: service.config}
	service.driver = &fakeSessionDriver{}
	runtime := &fakeLoaderAgentRuntime{}
	runtimes := fixedRuntimeProvider{runtime: runtime}
	streams := &SessionStreamBroker{subscribers: map[string]map[int]chan sessionWatchEvent{}}
	service.runtimes = runtimes
	service.streams = streams
	service.executor = &Executor{config: service.config, store: service.store, configDB: store, runtimes: runtimes, streams: streams}
	ctx := context.Background()
	composePath := filepath.Join(projectDir, "agent-compose.yml")
	resp, err := service.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec:   spec,
		Source: &agentcomposev2.ProjectSource{ComposePath: composePath},
	}))
	if err != nil {
		t.Fatalf("ApplyProject returned error: %v", err)
	}
	if !resp.Msg.GetApplied() {
		t.Fatalf("ApplyProject response = %#v", resp.Msg)
	}
	return store, service, resp.Msg.GetProject().GetSummary().GetProjectId()
}

func newRunServiceTestClient(t *testing.T, service *Service) (agentcomposev2connect.RunServiceClient, func()) {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := agentcomposev2connect.NewRunServiceHandler(service)
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	return agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL), server.Close
}

func collectRunAgentStreamEvents(ctx context.Context, client agentcomposev2connect.RunServiceClient, req *agentcomposev2.RunAgentRequest) ([]*agentcomposev2.RunAgentStreamResponse, error) {
	stream, err := client.RunAgentStream(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	var events []*agentcomposev2.RunAgentStreamResponse
	for stream.Receive() {
		events = append(events, stream.Msg())
	}
	return events, stream.Err()
}

func lastRunAgentStreamEvent(events []*agentcomposev2.RunAgentStreamResponse, eventType agentcomposev2.RunAgentStreamEventType) *agentcomposev2.RunAgentStreamResponse {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].GetEventType() == eventType {
			return events[index]
		}
	}
	return nil
}

func runServiceFakeRuntime(t *testing.T, service *Service) *fakeLoaderAgentRuntime {
	t.Helper()
	provider, ok := service.runtimes.(fixedRuntimeProvider)
	if !ok {
		t.Fatalf("service runtime provider = %T, want fixedRuntimeProvider", service.runtimes)
	}
	runtime, ok := provider.runtime.(*fakeLoaderAgentRuntime)
	if !ok {
		t.Fatalf("fixed runtime = %T, want *fakeLoaderAgentRuntime", provider.runtime)
	}
	return runtime
}

func newRunServiceLoaderManager(service *Service) *LoaderManager {
	return &LoaderManager{
		config:       service.config,
		rootCtx:      context.Background(),
		store:        service.store,
		configDB:     service.configDB,
		driver:       service.driver,
		executor:     service.executor,
		streams:      service.streams,
		bus:          &LoaderBus{ch: make(chan LoaderTopicEvent, 16)},
		engine:       &QJSLoaderEngine{},
		loaders:      map[string]Loader{},
		running:      map[string]int{},
		scheduleWake: make(chan struct{}, 1),
	}
}

func loaderEventsContain(events []LoaderEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func beginRunPreparationTestRun(t *testing.T, store *ConfigStore, projectID, requestID string) ProjectRunRecord {
	t.Helper()
	run, err := NewRunCoordinator(store).BeginRun(context.Background(), ProjectRunStartRequest{
		ProjectID:       projectID,
		AgentName:       "reviewer",
		Source:          ProjectRunSourceManual,
		ClientRequestID: requestID,
	})
	if err != nil {
		t.Fatalf("BeginRun returned error: %v", err)
	}
	return run
}

func envItemsByName(items []SessionEnvVar) map[string]SessionEnvVar {
	env := make(map[string]SessionEnvVar, len(items))
	for _, item := range items {
		env[item.Name] = item
	}
	return env
}

func sessionTagsContain(items []SessionTag, name, value string) bool {
	for _, item := range items {
		if strings.TrimSpace(item.Name) == name && strings.TrimSpace(item.Value) == value {
			return true
		}
	}
	return false
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
}
