package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	agentcomposev2connect "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c" //nolint:staticcheck // Tests required h2c transport compatibility.
)

func TestValidateEventLogTargetRejectsPrefixAndNonEventID(t *testing.T) {
	if err := validateEventLogTarget("evt_abc123"); err != nil {
		t.Fatalf("validateEventLogTarget(full id) returned error: %v", err)
	}
	for _, value := range []string{"evt_", "evt", "run-123", "abc"} {
		err := validateEventLogTarget(value)
		if err == nil {
			t.Fatalf("validateEventLogTarget(%q) = nil, want usage error", value)
		}
		var exitErr commandExitError
		if !errors.As(err, &exitErr) || exitErr.Code != exitCodeUsage {
			t.Fatalf("validateEventLogTarget(%q) = %#v, want usage exit error", value, err)
		}
	}
}

// eventRunStub records ListRuns requests and emulates the daemon-side event
// filter: known event ids return the seeded runs, unknown ids return NotFound.
type eventRunStub struct {
	agentcomposev2connect.UnimplementedRunServiceHandler
	mu           sync.Mutex
	runs         []*agentcomposev2.RunSummary
	notFound     bool
	truncated    bool
	listRequests []*agentcomposev2.ListRunsRequest
}

func (s *eventRunStub) ListRuns(_ context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
	s.mu.Lock()
	s.listRequests = append(s.listRequests, req.Msg)
	notFound := s.notFound
	truncated := s.truncated
	s.mu.Unlock()
	if req.Msg.GetEventId() == "" {
		return connect.NewResponse(&agentcomposev2.ListRunsResponse{}), nil
	}
	if notFound {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("event %s not found", req.Msg.GetEventId()))
	}
	return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: s.runs, Total: uint32(len(s.runs)), EventScopeTruncated: truncated}), nil
}

func (s *eventRunStub) GetRun(_ context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
	return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-event-review", "reviewer", "session-event", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "event output\n")}), nil
}

func (s *eventRunStub) FollowRunLogs(_ context.Context, req *connect.Request[agentcomposev2.FollowRunLogsRequest], stream *connect.ServerStream[agentcomposev2.RunLogChunk]) error {
	if err := stream.Send(&agentcomposev2.RunLogChunk{Data: "event output\n", Offset: 13, RunStatus: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED}); err != nil {
		return err
	}
	return stream.Send(&agentcomposev2.RunLogChunk{Offset: 13, IsFinal: true, RunStatus: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED})
}

func TestIntegrationCLILogsEventSendsFilterAndValidatesSelectors(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-logs-event
agents:
  reviewer:
    provider: codex
`)
	runStub := &eventRunStub{runs: []*agentcomposev2.RunSummary{{
		RunId:     "run-event-review",
		ProjectId: "project-event",
		AgentName: "reviewer",
		Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
		SandboxId: "session-event",
	}}}
	server := newEventRunStubServer(t, runStub)

	stdout, stderr, _, exitCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--event", "evt_event_test")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("logs --event code/stderr = %d / %q", exitCode, stderr)
	}
	prefix := "reviewer-run-event-review | "
	want := expectedLogSeparator(prefix, ">") +
		"reviewer-run-event-review | test prompt\n" +
		expectedLogSeparator(prefix, "<") +
		"reviewer-run-event-review | event output\n"
	if stdout != want {
		t.Fatalf("logs --event stdout = %q, want %q", stdout, want)
	}
	if len(runStub.listRequests) != 1 {
		t.Fatalf("ListRuns calls = %d, want 1", len(runStub.listRequests))
	}
	request := runStub.listRequests[0]
	if request.GetEventId() != "evt_event_test" {
		t.Fatalf("ListRuns event_id = %q", request.GetEventId())
	}
	if request.GetLimit() != 200 || request.GetOffset() != 0 {
		t.Fatalf("ListRuns paging = limit %d offset %d, want 200/0", request.GetLimit(), request.GetOffset())
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--event", "evt_event_test", "--json")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("logs --event --json code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeLogsOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("logs --event --json decode failed: %v\n%s", err, jsonOut)
	}
	if len(decoded.Runs) != 1 || decoded.Runs[0].RunID != displayOpaqueID("run-event-review") || decoded.Runs[0].Content != "event output\n" {
		t.Fatalf("logs --event --json = %#v", decoded.Runs)
	}

	for _, testCase := range []struct {
		args    []string
		message string
	}{
		{[]string{"--event", "evt_event_test", "--run", "run-x"}, "logs --run cannot be combined with --event"},
		{[]string{"--event", "evt_event_test", "--sandbox", "sandbox-x"}, "logs --event cannot be combined with --sandbox"},
		{[]string{"reviewer", "--event", "evt_event_test"}, "logs --event cannot be combined with a positional target"},
		{[]string{"abc123def456", "--event", "evt_event_test"}, "logs --event cannot be combined with a positional target"},
		{[]string{"--event", "evt_"}, "full event id"},
	} {
		_, stderr, _, exitCode := executeCLICommand(append([]string{"logs", "--host", server.URL, "--file", composePath}, testCase.args...)...)
		if exitCode != exitCodeUsage {
			t.Fatalf("logs %v exit code = %d, stderr = %q", testCase.args, exitCode, stderr)
		}
		if !strings.Contains(stderr, testCase.message) {
			t.Fatalf("logs %v stderr = %q, want %q", testCase.args, stderr, testCase.message)
		}
	}
}

func TestIntegrationCLILogsEventWithoutRunsIsReported(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-logs-event-empty
agents:
  reviewer:
    provider: codex
`)
	server := newEventRunStubServer(t, &eventRunStub{})

	_, stderr, _, exitCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--event", "evt_no_runs")
	if exitCode != 0 {
		t.Fatalf("logs --event empty exit code = %d, stderr = %q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "No runs are associated with event evt_no_runs") {
		t.Fatalf("logs --event empty stderr = %q", stderr)
	}

	jsonOut, _, _, jsonCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--event", "evt_no_runs", "--json")
	if jsonCode != 0 {
		t.Fatalf("logs --event empty --json exit code = %d", jsonCode)
	}
	var decoded composeLogsOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("logs --event empty --json decode failed: %v\n%s", err, jsonOut)
	}
	if len(decoded.Runs) != 0 {
		t.Fatalf("logs --event empty --json runs = %#v", decoded.Runs)
	}
}

func TestIntegrationCLILogsEventNotFoundMapsToUsage(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-logs-event-missing
agents:
  reviewer:
    provider: codex
`)
	server := newEventRunStubServer(t, &eventRunStub{notFound: true})

	_, stderr, _, exitCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--event", "evt_missing")
	if exitCode != exitCodeUsage {
		t.Fatalf("logs --event not-found exit code = %d, stderr = %q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "evt_missing") {
		t.Fatalf("logs --event not-found stderr = %q", stderr)
	}
}

func TestIntegrationCLILogsEventScopeTruncatedWarns(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-logs-event-truncated
agents:
  reviewer:
    provider: codex
`)
	server := newEventRunStubServer(t, &eventRunStub{truncated: true})

	_, stderr, _, exitCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--event", "evt_big")
	if exitCode != 0 {
		t.Fatalf("logs --event truncated exit code = %d", exitCode)
	}
	if !strings.Contains(stderr, "more associated events than the daemon resolves") || !strings.Contains(stderr, "evt_big") {
		t.Fatalf("logs --event truncated stderr = %q", stderr)
	}

	_, stderr, _, exitCode = executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--event", "evt_big", "--json")
	if exitCode != 0 {
		t.Fatalf("logs --event truncated --json exit code = %d", exitCode)
	}
	if !strings.Contains(stderr, "more associated events than the daemon resolves") {
		t.Fatalf("logs --event truncated --json stderr = %q", stderr)
	}
}

func newEventRunStubServer(t *testing.T, stub *eventRunStub) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := agentcomposev2connect.NewRunServiceHandler(stub)
	mux.Handle(path, handler)
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{})) //nolint:staticcheck // Tests required h2c transport compatibility.
	t.Cleanup(server.Close)
	return server
}
