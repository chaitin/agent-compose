package agentcompose

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"agent-compose/pkg/agentcompose/domain"
	driverpkg "agent-compose/pkg/driver"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func TestExecServiceExecStreamResolvesSelectorAndStreamsOutput(t *testing.T) {
	store, service, projectID := setupRunPreparationDemoProject(t)
	runtime := runServiceFakeRuntime(t, service)
	runtime.commandStdout = "exec stdout\n"
	runtime.commandStderr = "exec stderr\n"
	ctx := context.Background()
	session := createExecServiceProjectSession(t, service, store, projectID, "run-exec", "reviewer", domain.VMStatusRunning)
	client, closeServer := newExecServiceTestClient(t, service)
	defer closeServer()

	events, err := collectExecStreamEvents(ctx, client, &agentcomposev2.ExecRequest{
		Target: &agentcomposev2.ExecRequest_Selector{Selector: &agentcomposev2.ExecSessionSelector{
			ProjectId: projectID,
			AgentName: "reviewer",
		}},
		Command: &agentcomposev2.ExecCommand{Command: "bash", Args: []string{"-lc", "pwd"}},
		Cwd:     "/workspace",
	})
	if err != nil {
		t.Fatalf("ExecStream returned error: %v", err)
	}
	if runtime.execCalls != 1 {
		t.Fatalf("runtime ExecStream calls = %d, want 1", runtime.execCalls)
	}
	if len(events) != 4 {
		t.Fatalf("ExecStream events = %d, want 4: %#v", len(events), events)
	}
	if events[0].GetEventType() != agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_STARTED || events[0].GetSessionId() != session.Summary.ID {
		t.Fatalf("started event = %#v", events[0])
	}
	if events[1].GetChunk() != "exec stdout\n" || events[1].GetIsStderr() {
		t.Fatalf("stdout event = %#v", events[1])
	}
	if events[2].GetChunk() != "exec stderr\n" || !events[2].GetIsStderr() {
		t.Fatalf("stderr event = %#v", events[2])
	}
	result := events[3].GetResult()
	if result.GetSessionId() != session.Summary.ID || result.GetRunId() != "run-exec" || !result.GetSuccess() || result.GetStdout() != "exec stdout\n" || result.GetStderr() != "exec stderr\n" {
		t.Fatalf("completed result = %#v", result)
	}
}

func TestExecServiceExecStreamSelectorErrorsWhenNoRunningSession(t *testing.T) {
	_, service, projectID := setupRunPreparationDemoProject(t)
	client, closeServer := newExecServiceTestClient(t, service)
	defer closeServer()

	_, err := collectExecStreamEvents(context.Background(), client, &agentcomposev2.ExecRequest{
		Target: &agentcomposev2.ExecRequest_Selector{Selector: &agentcomposev2.ExecSessionSelector{
			ProjectId: projectID,
			AgentName: "reviewer",
		}},
		Command: &agentcomposev2.ExecCommand{Command: "bash"},
	})
	if connect.CodeOf(err) != connect.CodeNotFound || !strings.Contains(err.Error(), "no running session") {
		t.Fatalf("ExecStream no session error = %v, code %s", err, connect.CodeOf(err))
	}
}

func TestExecServiceExecStreamSelectorErrorsWhenAmbiguous(t *testing.T) {
	store, service, projectID := setupRunPreparationDemoProject(t)
	createExecServiceProjectSession(t, service, store, projectID, "run-one", "reviewer", domain.VMStatusRunning)
	createExecServiceProjectSession(t, service, store, projectID, "run-two", "reviewer", domain.VMStatusRunning)
	client, closeServer := newExecServiceTestClient(t, service)
	defer closeServer()

	_, err := collectExecStreamEvents(context.Background(), client, &agentcomposev2.ExecRequest{
		Target: &agentcomposev2.ExecRequest_Selector{Selector: &agentcomposev2.ExecSessionSelector{
			ProjectId: projectID,
			AgentName: "reviewer",
		}},
		Command: &agentcomposev2.ExecCommand{Command: "bash"},
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "multiple running sessions") {
		t.Fatalf("ExecStream ambiguous error = %v, code %s", err, connect.CodeOf(err))
	}
}

func TestExecServiceExecStreamRunTargetRequiresRunningSession(t *testing.T) {
	store, service, projectID := setupRunPreparationDemoProject(t)
	createExecServiceProjectSession(t, service, store, projectID, "run-stopped", "reviewer", domain.VMStatusPending)
	client, closeServer := newExecServiceTestClient(t, service)
	defer closeServer()

	_, err := collectExecStreamEvents(context.Background(), client, &agentcomposev2.ExecRequest{
		Target:  &agentcomposev2.ExecRequest_RunId{RunId: "run-stopped"},
		Command: &agentcomposev2.ExecCommand{Command: "bash"},
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "is not running") {
		t.Fatalf("ExecStream stopped session error = %v, code %s", err, connect.CodeOf(err))
	}
}

func setupRunPreparationDemoProject(t *testing.T) (*ConfigStore, *Service, string) {
	t.Helper()
	return setupRunPreparationProject(t, newProjectServiceTestSpec("demo", "gpt-test"), t.TempDir())
}

func createExecServiceProjectSession(t *testing.T, service *Service, store *ConfigStore, projectID, runID, agentName, vmStatus string) *Session {
	t.Helper()
	ctx := context.Background()
	session, err := service.store.CreateSession(ctx, "Exec Session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = vmStatus
	if err := service.store.UpdateSession(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	if _, err := store.CreateProjectRun(ctx, ProjectRunRecord{
		RunID:           runID,
		ProjectID:       projectID,
		ProjectName:     "demo",
		ProjectRevision: 1,
		AgentName:       agentName,
		Source:          domain.ProjectRunSourceManual,
		Status:          domain.ProjectRunStatusRunning,
		SessionID:       session.Summary.ID,
		Driver:          driverpkg.RuntimeDriverBoxlite,
		ImageRef:        "guest:latest",
	}); err != nil {
		t.Fatalf("CreateProjectRun returned error: %v", err)
	}
	return session
}

func newExecServiceTestClient(t *testing.T, service *Service) (agentcomposev2connect.ExecServiceClient, func()) {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := agentcomposev2connect.NewExecServiceHandler(service)
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	return agentcomposev2connect.NewExecServiceClient(server.Client(), server.URL), server.Close
}

func collectExecStreamEvents(ctx context.Context, client agentcomposev2connect.ExecServiceClient, req *agentcomposev2.ExecRequest) ([]*agentcomposev2.ExecStreamResponse, error) {
	stream, err := client.ExecStream(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	var events []*agentcomposev2.ExecStreamResponse
	for stream.Receive() {
		events = append(events, stream.Msg())
	}
	return events, stream.Err()
}
