package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	agentcomposev2connect "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c" //nolint:staticcheck // Tests required h2c transport compatibility.
)

func TestEventLogTargetErrorRejectsPrefixAndNonEventID(t *testing.T) {
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

func TestEventLogReplayRunsNarrowsByAgent(t *testing.T) {
	runs := []*agentcomposev2.RunSummary{
		{RunId: "run-reviewer", AgentName: "reviewer"},
		{RunId: "run-writer", AgentName: "writer"},
	}
	if got := eventLogReplayRuns(runs, ""); len(got) != 2 {
		t.Fatalf("eventLogReplayRuns without agent = %d runs, want 2", len(got))
	}
	narrowed := eventLogReplayRuns(runs, "writer")
	if len(narrowed) != 1 || narrowed[0].GetRunId() != "run-writer" {
		t.Fatalf("eventLogReplayRuns(writer) = %#v", narrowed)
	}
	if got := eventLogReplayRuns(runs, "missing"); len(got) != 0 {
		t.Fatalf("eventLogReplayRuns(missing) = %#v, want empty", got)
	}
}

func TestResolveEventLogSchedulerRunIDsParsesTraceAndDeduplicates(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"event": map[string]any{"id": "evt_dedup"},
			"runs": []map[string]any{
				{"delivery": map[string]any{"run_id": "sched-run-1", "status": "run_succeeded"}},
				{"delivery": map[string]any{"run_id": "sched-run-2", "status": "matched"}},
				{"delivery": map[string]any{"run_id": "sched-run-1", "status": "run_succeeded"}},
				{"delivery": map[string]any{"run_id": ""}},
			},
		})
	}))
	defer server.Close()

	runIDs, err := resolveEventLogSchedulerRunIDs(context.Background(), server.Client(), server.URL, "evt_dedup")
	if err != nil {
		t.Fatalf("resolveEventLogSchedulerRunIDs returned error: %v", err)
	}
	if gotPath != "/api/events/evt_dedup/trace" {
		t.Fatalf("trace request path = %q", gotPath)
	}
	if len(runIDs) != 2 || runIDs[0] != "sched-run-1" || runIDs[1] != "sched-run-2" {
		t.Fatalf("scheduler run ids = %#v, want [sched-run-1 sched-run-2]", runIDs)
	}
}

func TestResolveEventLogSchedulerRunIDsNotFoundAndErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/events/evt_missing/trace":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"event not found"}`))
		case "/api/events/evt_broken/trace":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"failed to trace event"}`))
		default:
			t.Fatalf("unexpected trace path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	_, err := resolveEventLogSchedulerRunIDs(context.Background(), server.Client(), server.URL, "evt_missing")
	var exitErr commandExitError
	if !errors.As(err, &exitErr) || exitErr.Code != exitCodeUsage || !strings.Contains(exitErr.Error(), "evt_missing not found") {
		t.Fatalf("not-found error = %#v, want usage exit error", err)
	}

	_, err = resolveEventLogSchedulerRunIDs(context.Background(), server.Client(), server.URL, "evt_broken")
	exitErr = commandExitError{}
	if !errors.As(err, &exitErr) || exitErr.Code != exitCodeUnavailable {
		t.Fatalf("server-error = %#v, want unavailable exit error", err)
	}
}

func TestResolveEventLogSchedulerRunIDsRejectsMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	_, err := resolveEventLogSchedulerRunIDs(context.Background(), server.Client(), server.URL, "evt_bad")
	var exitErr commandExitError
	if !errors.As(err, &exitErr) || exitErr.Code != exitCodeUnavailable || !strings.Contains(exitErr.Error(), "parse event") {
		t.Fatalf("malformed json error = %#v, want unavailable exit error", err)
	}
}

func newEventLogsStubServer(t *testing.T, runStub runServiceStub, trace func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := agentcomposev2connect.NewRunServiceHandler(runStub)
	mux.Handle(path, handler)
	mux.HandleFunc("/api/events/", trace)
	return httptest.NewServer(h2c.NewHandler(mux, &http2.Server{})) //nolint:staticcheck // Tests required h2c transport compatibility.
}

func TestIntegrationCLILogsEventFiltersRunsAndValidatesSelectors(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-logs-event
agents:
  reviewer:
    provider: codex
`)
	var listRequests []*agentcomposev2.ListRunsRequest
	runStub := runServiceStub{
		listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
			listRequests = append(listRequests, req.Msg)
			if req.Msg.GetSchedulerRunId() == "" {
				t.Fatalf("ListRuns without scheduler_run_id filter: %#v", req.Msg)
			}
			if req.Msg.GetSchedulerRunId() == "sched-run-reviewer" {
				return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{{
					RunId:     "run-event-reviewer",
					ProjectId: req.Msg.GetProjectId(),
					AgentName: "reviewer",
					Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
					SandboxId: "session-event-reviewer",
				}}}), nil
			}
			return connect.NewResponse(&agentcomposev2.ListRunsResponse{}), nil
		},
		getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
			return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-event-reviewer", "reviewer", "session-event-reviewer", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "event output\n")}), nil
		},
		followRunLogs: func(ctx context.Context, req *connect.Request[agentcomposev2.FollowRunLogsRequest], stream *connect.ServerStream[agentcomposev2.RunLogChunk]) error {
			if err := stream.Send(&agentcomposev2.RunLogChunk{Data: "event output\n", Offset: 13, RunStatus: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED}); err != nil {
				return err
			}
			return stream.Send(&agentcomposev2.RunLogChunk{Offset: 13, IsFinal: true, RunStatus: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED})
		},
	}
	trace := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events/evt_event_test/trace" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"event": map[string]any{"id": "evt_event_test"},
			"runs": []map[string]any{
				{"delivery": map[string]any{"run_id": "sched-run-reviewer", "status": "run_succeeded"}},
				{"delivery": map[string]any{"run_id": "sched-run-empty", "status": "matched"}},
			},
		})
	}
	server := newEventLogsStubServer(t, runStub, trace)

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
	if len(listRequests) != 2 {
		t.Fatalf("ListRuns calls = %d, want 2", len(listRequests))
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--event", "evt_event_test", "--json")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("logs --event --json code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeLogsOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("logs --event --json decode failed: %v\n%s", err, jsonOut)
	}
	if len(decoded.Runs) != 1 || decoded.Runs[0].RunID != displayOpaqueID("run-event-reviewer") || decoded.Runs[0].Content != "event output\n" {
		t.Fatalf("logs --event --json = %#v", decoded.Runs)
	}

	for _, testCase := range []struct {
		args    []string
		message string
	}{
		{[]string{"--event", "evt_event_test", "--run", "run-x"}, "logs --run cannot be combined with --event"},
		{[]string{"--event", "evt_event_test", "--sandbox", "sandbox-x"}, "logs --event cannot be combined with --sandbox"},
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
  writer:
    provider: codex
`)
	trace := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events/evt_no_runs/trace" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"event": map[string]any{"id": "evt_no_runs"},
			"runs":  []map[string]any{},
		}); err != nil {
			t.Fatalf("encode empty trace: %v", err)
		}
	}
	server := newEventLogsStubServer(t, runServiceStub{}, trace)
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

func TestIntegrationCLILogsEventAgentNarrowingToEmptyIsReported(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-logs-event-narrow
agents:
  reviewer:
    provider: codex
  writer:
    provider: codex
`)
	runStub := runServiceStub{
		listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
			return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{{
				RunId:     "run-event-reviewer",
				ProjectId: req.Msg.GetProjectId(),
				AgentName: "reviewer",
				Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
				SandboxId: "session-event-reviewer",
			}}}), nil
		},
	}
	trace := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events/evt_narrow/trace" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"event": map[string]any{"id": "evt_narrow"},
			"runs":  []map[string]any{{"delivery": map[string]any{"run_id": "sched-run-reviewer", "status": "run_succeeded"}}},
		})
	}
	server := newEventLogsStubServer(t, runStub, trace)

	_, stderr, _, exitCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--event", "evt_narrow", "--agent", "writer")
	if exitCode != 0 {
		t.Fatalf("logs --event --agent narrow exit code = %d, stderr = %q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "No runs are associated with event evt_narrow") {
		t.Fatalf("logs --event --agent narrow stderr = %q", stderr)
	}
}
