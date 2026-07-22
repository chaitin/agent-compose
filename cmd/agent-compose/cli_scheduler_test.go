package main

import (
	"agent-compose/pkg/compose"
	"agent-compose/pkg/identity"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestConfigCommandExpandsSchedulerScriptURLs(t *testing.T) {
	const script = `scheduler.interval("from-url", "1h");`
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, script)
	}))
	defer httpServer.Close()

	for _, tc := range []struct {
		name     string
		location func(string) string
	}{
		{name: "relative file", location: func(dir string) string {
			path := filepath.Join(dir, "scripts", "scheduler.js")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			return "provider: file\n        path: ./scripts/scheduler.js"
		}},
		{name: "HTTP", location: func(string) string { return "provider: http\n        url: " + httpServer.URL + "/scheduler.js" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			composePath := writeComposeFile(t, dir, fmt.Sprintf(`
name: config-script-url
agents:
  reviewer:
    scheduler:
      script:
        %s
`, tc.location(dir)))
			stdout, stderr, runCount, err := executeCommand("config", "--file", composePath)
			if err != nil || stderr != "" || runCount != 0 {
				t.Fatalf("config stdout=%q stderr=%q runCount=%d err=%v", stdout, stderr, runCount, err)
			}
			if !strings.Contains(stdout, script) || strings.Contains(stdout, "url:") {
				t.Fatalf("config did not expand URL to inline snapshot:\n%s", stdout)
			}
		})
	}
}

func TestUpResolvesSchedulerScriptURLBeforeApply(t *testing.T) {
	const script = `scheduler.interval("from-url", "1h");`
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, script)
	}))
	defer sourceServer.Close()

	var captured *agentcomposev2.ApplyProjectRequest
	daemon := newComposeServiceStubServer(t, composeServiceStubs{project: projectServiceStub{
		applyProject: func(_ context.Context, req *connect.Request[agentcomposev2.ApplyProjectRequest]) (*connect.Response[agentcomposev2.ApplyProjectResponse], error) {
			captured = req.Msg
			return connect.NewResponse(&agentcomposev2.ApplyProjectResponse{Applied: true}), nil
		},
	}})
	defer daemon.Close()

	composePath := writeComposeFile(t, t.TempDir(), fmt.Sprintf(`
name: up-script-url
agents:
  reviewer:
    scheduler:
      script:
        provider: http
        url: %s/scheduler.js
`, sourceServer.URL))
	_, expected, err := loadResolvedNormalizedCompose(context.Background(), cliOptions{ComposeFile: composePath})
	if err != nil {
		t.Fatalf("load expected spec: %v", err)
	}
	expectedHash, err := expected.Hash()
	if err != nil {
		t.Fatalf("hash expected spec: %v", err)
	}

	stdout, stderr, _, exitCode := executeCLICommand("up", "--file", composePath, "--host", daemon.URL, "--json")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("up stdout=%q stderr=%q exit=%d", stdout, stderr, exitCode)
	}
	if captured == nil || captured.GetExpectedSpecHash() != expectedHash {
		t.Fatalf("Apply request = %#v, want expected hash %q", captured, expectedHash)
	}
	gotScript := captured.GetSpec().GetAgents()[0].GetScheduler().GetScript()
	if gotScript != script {
		t.Fatalf("wire scheduler script = %q, want %q", gotScript, script)
	}
}

func TestDownDoesNotFetchSchedulerScriptURL(t *testing.T) {
	var removeCalls int
	daemon := newComposeServiceStubServer(t, composeServiceStubs{project: projectServiceStub{
		removeProject: func(context.Context, *connect.Request[agentcomposev2.RemoveProjectRequest]) (*connect.Response[agentcomposev2.RemoveProjectResponse], error) {
			removeCalls++
			return connect.NewResponse(&agentcomposev2.RemoveProjectResponse{}), nil
		},
	}})
	defer daemon.Close()
	composePath := writeComposeFile(t, t.TempDir(), `
name: down-unreachable-script
agents:
  reviewer:
    scheduler:
      script:
        provider: http
        url: http://127.0.0.1:1/unreachable.js
`)
	_, stderr, _, exitCode := executeCLICommand("down", "--file", composePath, "--host", daemon.URL, "--json")
	if exitCode != 0 || stderr != "" || removeCalls != 1 {
		t.Fatalf("down stderr=%q exit=%d removeCalls=%d", stderr, exitCode, removeCalls)
	}
}

func TestCommandExitCodeClassifiesSchedulerResourceNotFoundAsUsage(t *testing.T) {
	notFound := schedulerResourceNotFoundError{kind: "run", ref: "missing"}
	for _, err := range []error{notFound, fmt.Errorf("resolve scheduler run: %w", notFound)} {
		if got := commandExitCode(err); got != exitCodeUsage {
			t.Fatalf("exit code = %d, want %d; err=%v", got, exitCodeUsage, err)
		}
	}
}

func TestIntegrationCLIUpAppliesInlineSchedulerScriptAndPSJSON(t *testing.T) {
	composePath := writeComposeFile(t, filepath.Join(t.TempDir(), "cli-inline-project"), inlineSchedulerComposeYAML("cli-inline-demo", 60000))
	configOut, configErr, configRunCount, err := executeCommand("config", "--file", composePath)
	if err != nil {
		t.Fatalf("config inline returned error: %v", err)
	}
	if configErr != "" {
		t.Fatalf("config inline stderr = %q, want empty", configErr)
	}
	if configRunCount != 0 {
		t.Fatalf("config inline daemon runner called %d times, want 0", configRunCount)
	}
	if !strings.Contains(configOut, "script:") || !strings.Contains(configOut, `scheduler.interval("interval-review"`) {
		t.Fatalf("config inline output missing scheduler script:\n%s", configOut)
	}

	useTestDockerImage(t, "guest:v1")
	socketPath := shortUnixSocketPath(t)
	app, cancel := newTestDaemonAppWithSocketAndTCP(t, socketPath, "", nil)
	defer cancel()
	runCtx, stop := context.WithCancel(context.Background())
	errCh := runDaemonAppAsync(app, runCtx)
	t.Cleanup(func() {
		stop()
		waitForDaemonExit(t, errCh)
	})
	waitForHTTPStatus(t, newUnixHTTPClient(socketPath), "http://agent-compose/api/version", http.StatusOK)
	t.Setenv("AGENT_COMPOSE_SOCKET", socketPath)
	t.Setenv("AGENT_COMPOSE_HOST", "")

	firstOut, firstErr, _, firstCode := executeCLICommand("up", "--file", composePath)
	if firstCode != 0 {
		t.Fatalf("up inline first exit code = %d, stderr=%q", firstCode, firstErr)
	}
	if firstErr != "" {
		t.Fatalf("up inline first stderr = %q, want empty", firstErr)
	}
	for _, want := range []string{"ID", "NAME", "TYPE", "ACTION", "trigger"} {
		if !strings.Contains(firstOut, want) {
			t.Fatalf("up inline first stdout %q does not contain %q", firstOut, want)
		}
	}

	repeatedOut, repeatedErr, _, repeatedCode := executeCLICommand("up", "--file", composePath, "--json")
	if repeatedCode != 0 {
		t.Fatalf("up inline repeated exit code = %d, stderr=%q", repeatedCode, repeatedErr)
	}
	if repeatedErr != "" {
		t.Fatalf("up inline repeated stderr = %q, want empty", repeatedErr)
	}
	repeated := decodeComposeUpOutput(t, repeatedOut)
	if repeated.Project.Name != "cli-inline-demo" || repeated.Project.CurrentRevision != 1 || repeated.Project.SchedulerCount != 1 {
		t.Fatalf("up inline repeated project = %#v", repeated.Project)
	}
	if !repeated.Applied || !repeated.Unchanged || repeated.Revision.Revision != 1 {
		t.Fatalf("up inline repeated state = applied %v unchanged %v revision %#v", repeated.Applied, repeated.Unchanged, repeated.Revision)
	}
	assertComposeUpChange(t, repeated.Changes, "unchanged", "project_scheduler", "reviewer")

	psOut, psErr, _, psCode := executeCLICommand("ps", "--file", composePath, "--json")
	if psCode != 0 {
		t.Fatalf("ps inline exit code = %d, stderr=%q", psCode, psErr)
	}
	if psErr != "" {
		t.Fatalf("ps inline stderr = %q, want empty", psErr)
	}
	var psDecoded composePSOutput
	if err := json.Unmarshal([]byte(psOut), &psDecoded); err != nil {
		t.Fatalf("ps inline JSON decode failed: %v\n%s", err, psOut)
	}
	if psDecoded.Project.Name != "cli-inline-demo" || len(psDecoded.Sandboxes) != 0 {
		t.Fatalf("ps inline project/sandboxes = %#v", psDecoded)
	}
	if strings.Contains(psOut, "managed_loader") {
		t.Fatalf("ps inline JSON exposes internal loader identity: %s", psOut)
	}
}

func TestIntegrationCLIRunTriggerPositionalRejected(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-trigger
agents:
  reviewer:
    provider: codex
    scheduler:
      triggers:
        - name: nightly-review
          cron: "0 1 * * *"
          prompt: review nightly
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				t.Fatalf("RunAgentStream should not be called for positional trigger")
				return nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "reviewer", "nightly-review")
	if exitCode != exitCodeUsage || stdout != "" || !strings.Contains(stderr, "does not accept positional trigger arguments") {
		t.Fatalf("run positional trigger code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
}

func TestIntegrationCLIRunTriggerPositionalJSONRejected(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-trigger-warning
agents:
  reviewer:
    provider: codex
    scheduler:
      triggers:
        - name: nightly-warning
          cron: "0 2 * * *"
          prompt: review nightly
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				t.Fatalf("RunAgentStream should not be called for positional trigger")
				return nil
			},
		},
	})
	defer server.Close()

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "--json", "reviewer", "nightly-warning")
	if jsonCode != exitCodeUsage || jsonOut != "" || !strings.Contains(jsonErr, "does not accept positional trigger arguments") {
		t.Fatalf("run positional trigger --json code/stdout/stderr = %d / %q / %q", jsonCode, jsonOut, jsonErr)
	}
}

func TestIntegrationCLISchedulerList(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-scheduler-list
agents:
  reviewer:
    provider: codex
    scheduler:
      triggers:
        - name: nightly
          cron: "0 2 * * *"
          prompt: review nightly
        - name: events
          event:
            topic: repo.updated
          prompt: review event
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: testCLIProject(req.Msg.GetProject().GetProjectId(), "cli-scheduler-list", composePath)}), nil
			},
			listSchedulerEvents: func(context.Context, *connect.Request[agentcomposev2.ListSchedulerEventsRequest]) (*connect.Response[agentcomposev2.ListSchedulerEventsResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListSchedulerEventsResponse{}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "ls", "--host", server.URL, "--file", composePath)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("scheduler ls code/stderr = %d / %q", exitCode, stderr)
	}
	if !strings.Contains(stdout, "AGENT") || !strings.Contains(stdout, "nightly") || !strings.Contains(stdout, "events") || !strings.Contains(stdout, "declarative") {
		t.Fatalf("scheduler ls stdout = %q", stdout)
	}

	projectID, err := domain.StableProjectID("cli-scheduler-list", composePath)
	if err != nil {
		t.Fatalf("StableProjectID returned error: %v", err)
	}
	agentID, err := domain.StableManagedAgentID(projectID, "reviewer")
	if err != nil {
		t.Fatalf("StableManagedAgentID returned error: %v", err)
	}
	schedulerID, err := domain.StableProjectSchedulerID(projectID, "reviewer", "")
	if err != nil {
		t.Fatalf("StableProjectSchedulerID returned error: %v", err)
	}
	if !strings.Contains(stdout, shortOpaqueID(schedulerID)) || strings.Contains(stdout, displayOpaqueID(schedulerID)) {
		t.Fatalf("scheduler ls should show only short scheduler id %q, stdout = %q", shortOpaqueID(schedulerID), stdout)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if !strings.HasPrefix(lines[0], "SCHEDULER") || !strings.HasPrefix(strings.TrimSpace(lines[1]), shortOpaqueID(schedulerID)) {
		t.Fatalf("scheduler ls should show scheduler as first column, stdout = %q", stdout)
	}
	verboseOut, verboseErr, _, verboseCode := executeCLICommand("scheduler", "ls", "--verbose", "--host", server.URL, "--file", composePath)
	if verboseCode != 0 || verboseErr != "" || !strings.Contains(verboseOut, displayOpaqueID(schedulerID)) || !strings.Contains(verboseOut, "TRIGGER ID") {
		t.Fatalf("scheduler ls --verbose code/stdout/stderr = %d / %q / %q", verboseCode, verboseOut, verboseErr)
	}
	verboseLines := strings.Split(strings.TrimSpace(verboseOut), "\n")
	if !strings.HasPrefix(verboseLines[0], "SCHEDULER") || !strings.HasPrefix(strings.TrimSpace(verboseLines[1]), displayOpaqueID(schedulerID)) {
		t.Fatalf("scheduler ls --verbose should show scheduler as first column, stdout = %q", verboseOut)
	}
	jsonOut, jsonErr, _, jsonCode := executeCLICommand("scheduler", "ls", identity.ShortID(agentID), "--json", "--host", server.URL, "--file", composePath)
	if jsonCode != 0 || jsonErr != "" || !strings.Contains(jsonOut, `"agent_name": "reviewer"`) || !strings.Contains(jsonOut, `"source": "declarative"`) ||
		!strings.Contains(jsonOut, `"scheduler_id": "`+displayOpaqueID(schedulerID)+`"`) || !strings.Contains(jsonOut, `"scheduler_short_id": "`+shortOpaqueID(schedulerID)+`"`) ||
		!strings.Contains(jsonOut, `"trigger_short_id": "`) || strings.Contains(jsonOut, "managed_loader") {
		t.Fatalf("scheduler ls --json code/stdout/stderr = %d / %q / %q", jsonCode, jsonOut, jsonErr)
	}
}

func TestComposeUpUsesDistinctStableTriggerIDs(t *testing.T) {
	projectID, err := domain.StableProjectID("trigger-ids", "/tmp/trigger-ids/agent-compose.yml")
	if err != nil {
		t.Fatalf("StableProjectID returned error: %v", err)
	}
	spec := &compose.NormalizedProjectSpec{Agents: []compose.NormalizedAgentSpec{{
		Name: "reviewer",
		Scheduler: &compose.NormalizedSchedulerSpec{Triggers: []compose.NormalizedTriggerSpec{
			{Name: "hourly"},
			{Name: "startup"},
		}},
	}}}
	changes := composeDisplayChangesFromProjectChanges([]*agentcomposev2.ProjectChange{{
		Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED,
		ResourceType: "project_scheduler",
		ResourceId:   "shared-scheduler-id",
		Name:         "reviewer",
	}}, spec, projectID)
	if len(changes) != 2 {
		t.Fatalf("trigger display changes = %#v, want 2", changes)
	}
	for index, name := range []string{"hourly", "startup"} {
		wantID, err := domain.StableManagedTriggerID(projectID, "reviewer", "", name, index)
		if err != nil {
			t.Fatalf("StableManagedTriggerID(%q) returned error: %v", name, err)
		}
		if changes[index].Name != name || changes[index].ID != shortOpaqueID(wantID) {
			t.Fatalf("trigger display change[%d] = %#v, want name %q id %q", index, changes[index], name, shortOpaqueID(wantID))
		}
	}
	if changes[0].ID == changes[1].ID {
		t.Fatalf("trigger IDs must be distinct: %#v", changes)
	}
}

func TestNormalizeComposeSchedulerTriggerOptionsPayload(t *testing.T) {
	options, err := normalizeComposeSchedulerTriggerOptions(composeSchedulerTriggerOptions{
		PayloadJSON: " { \"topic\" : \"nightly\" } ",
	})
	if err != nil {
		t.Fatalf("normalize payload returned error: %v", err)
	}
	if options.PayloadJSON != `{"topic":"nightly"}` {
		t.Fatalf("payload = %q", options.PayloadJSON)
	}
	if _, err := normalizeComposeSchedulerTriggerOptions(composeSchedulerTriggerOptions{PayloadJSON: "{bad"}); err == nil {
		t.Fatalf("invalid payload returned nil error")
	}
	if _, err := normalizeComposeSchedulerTriggerOptions(composeSchedulerTriggerOptions{Prompt: "override"}); err == nil || !strings.Contains(err.Error(), "unsupported for complete scheduler runs") {
		t.Fatalf("unsupported prompt error = %v", err)
	}
}

func TestDeprecatedSchedulerAgentFlagWarningUsesStderr(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("agent", "", "")
	if err := cmd.Flags().Set("agent", "reviewer"); err != nil {
		t.Fatalf("Set agent flag returned error: %v", err)
	}
	var stdout, stderr strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	if err := writeDeprecatedSchedulerAgentFlagWarning(cmd, "use --scheduler instead"); err != nil {
		t.Fatalf("writeDeprecatedSchedulerAgentFlagWarning returned error: %v", err)
	}
	if stdout.String() != "" || !strings.Contains(stderr.String(), "--agent is deprecated") || !strings.Contains(stderr.String(), "use --scheduler instead") {
		t.Fatalf("stdout/stderr = %q / %q", stdout.String(), stderr.String())
	}
}

func TestIntegrationCLISchedulerRunsLogsAndInspectResources(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-scheduler-observability
agents:
  reviewer:
    provider: codex
    scheduler:
      triggers:
        - name: nightly
          cron: "0 2 * * *"
          prompt: review nightly
`)
	runID := identity.NewRandomID(identity.ResourceRun)
	legacyRunID := "550e8400-e29b-41d4-a716-446655440000"
	errorRunID := identity.NewRandomID(identity.ResourceRun)
	sandboxID := identity.NewRandomID(identity.ResourceSandbox)
	getSchedulerRunCalls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: testCLIProject(req.Msg.GetProject().GetProjectId(), "cli-scheduler-observability", composePath)}), nil
			},
			listSchedulerRuns: func(context.Context, *connect.Request[agentcomposev2.ListSchedulerRunsRequest]) (*connect.Response[agentcomposev2.ListSchedulerRunsResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListSchedulerRunsResponse{Runs: []*agentcomposev2.SchedulerRun{{
					RunId: runID, AgentName: "reviewer", SchedulerId: "scheduler-reviewer", TriggerId: "nightly",
					Status: agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SUCCEEDED, SandboxIds: []string{sandboxID},
					StartedAt: timestamppb.New(time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC)), CompletedAt: timestamppb.New(time.Date(2026, 7, 15, 1, 0, 2, 0, time.UTC)), DurationMs: 2000,
				}}}), nil
			},
			getSchedulerRun: func(_ context.Context, req *connect.Request[agentcomposev2.GetSchedulerRunRequest]) (*connect.Response[agentcomposev2.GetSchedulerRunResponse], error) {
				getSchedulerRunCalls++
				if req.Msg.GetRunId() == errorRunID {
					return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("run store unavailable"))
				}
				if req.Msg.GetRunId() != runID[:12] && req.Msg.GetRunId() != legacyRunID {
					return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("run not found"))
				}
				return connect.NewResponse(&agentcomposev2.GetSchedulerRunResponse{Run: &agentcomposev2.SchedulerRun{
					RunId: firstNonEmptyString(map[bool]string{true: legacyRunID}[req.Msg.GetRunId() == legacyRunID], runID), AgentName: "reviewer", SchedulerId: "scheduler-reviewer", TriggerId: "nightly",
					Status: agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SUCCEEDED, SandboxIds: []string{sandboxID},
				}}), nil
			},
			listProjectSchedulerEvents: func(context.Context, *connect.Request[agentcomposev2.ListProjectSchedulerEventsRequest]) (*connect.Response[agentcomposev2.ListProjectSchedulerEventsResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListProjectSchedulerEventsResponse{Events: []*agentcomposev2.SchedulerEvent{
					{Id: "event-2", RunId: runID, AgentName: "reviewer", TriggerId: "nightly", Type: "loader.agent.activity", Level: "info", Message: "done", CreatedAt: timestamppb.New(time.Date(2026, 7, 15, 1, 0, 2, 0, time.UTC))},
					{Id: "event-1", RunId: runID, AgentName: "reviewer", TriggerId: "nightly", Type: "loader.status", Level: "info", Message: "started", CreatedAt: timestamppb.New(time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC))},
				}}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "runs", "--host", server.URL, "--file", composePath)
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, shortOpaqueID(runID)) || !strings.Contains(stdout, shortOpaqueID(sandboxID)) || !strings.Contains(stdout, "reviewer") {
		t.Fatalf("scheduler runs code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("scheduler", "runs", "--json", "--host", server.URL, "--file", composePath)
	if jsonCode != 0 || jsonErr != "" || !strings.Contains(jsonOut, `"sandbox_ids": [`) || !strings.Contains(jsonOut, sandboxID) {
		t.Fatalf("scheduler runs --json code/stdout/stderr = %d / %q / %q", jsonCode, jsonOut, jsonErr)
	}

	stdout, stderr, _, exitCode = executeCLICommand("scheduler", "logs", runID[:12], "--host", server.URL, "--file", composePath)
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "scheduler.status") || !strings.Contains(stdout, "scheduler.agent.activity") || strings.Index(stdout, "started") > strings.Index(stdout, "done") {
		t.Fatalf("scheduler logs code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}

	jsonOut, jsonErr, _, jsonCode = executeCLICommand("scheduler", "inspect", runID[:12], "--json", "--host", server.URL, "--file", composePath)
	if jsonCode != 0 || jsonErr != "" || !strings.Contains(jsonOut, `"resource": "run"`) || !strings.Contains(jsonOut, sandboxID) {
		t.Fatalf("scheduler inspect run code/stdout/stderr = %d / %q / %q", jsonCode, jsonOut, jsonErr)
	}

	jsonOut, jsonErr, _, jsonCode = executeCLICommand("scheduler", "inspect", legacyRunID, "--json", "--host", server.URL, "--file", composePath)
	if jsonCode != 0 || jsonErr != "" || !strings.Contains(jsonOut, legacyRunID) {
		t.Fatalf("scheduler inspect legacy UUID run code/stdout/stderr = %d / %q / %q", jsonCode, jsonOut, jsonErr)
	}

	jsonOut, jsonErr, _, jsonCode = executeCLICommand("scheduler", "inspect", "reviewer", "--json", "--host", server.URL, "--file", composePath)
	if jsonCode != 0 || jsonErr != "" || !strings.Contains(jsonOut, `"resource": "scheduler"`) || !strings.Contains(jsonOut, `"agent_name": "reviewer"`) {
		t.Fatalf("scheduler inspect scheduler code/stdout/stderr = %d / %q / %q", jsonCode, jsonOut, jsonErr)
	}
	if getSchedulerRunCalls != 3 {
		t.Fatalf("GetSchedulerRun calls = %d, want 3; scheduler name inspection must not probe runs", getSchedulerRunCalls)
	}

	_, stderr, _, exitCode = executeCLICommand("scheduler", "inspect", errorRunID, "--host", server.URL, "--file", composePath)
	if exitCode == 0 || !strings.Contains(stderr, "run store unavailable") || strings.Contains(stderr, "not found") {
		t.Fatalf("scheduler inspect backend error code/stderr = %d / %q", exitCode, stderr)
	}

	_, stderr, _, exitCode = executeCLICommand("scheduler", "runs", "--status", "unknown", "--host", server.URL, "--file", composePath)
	if exitCode != exitCodeUsage || !strings.Contains(stderr, "--status must be") {
		t.Fatalf("scheduler runs invalid status code/stderr = %d / %q", exitCode, stderr)
	}

	_, stderr, _, exitCode = executeCLICommand("scheduler", "logs", runID, "--tail", "-2", "--host", server.URL, "--file", composePath)
	if exitCode != exitCodeUsage || !strings.Contains(stderr, "--tail must be") {
		t.Fatalf("scheduler logs invalid tail code/stderr = %d / %q", exitCode, stderr)
	}

	_, stderr, _, exitCode = executeCLICommand("scheduler", "logs", runID, "--agent", "reviewer", "--host", server.URL, "--file", composePath)
	if exitCode != exitCodeUsage || !strings.Contains(stderr, "cannot be combined") {
		t.Fatalf("scheduler logs explicit run filters code/stderr = %d / %q", exitCode, stderr)
	}
}

func TestIntegrationCLISchedulerQueriesHistoricalTriggerIDs(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-scheduler-historical-triggers
agents:
  reviewer:
    provider: codex
    scheduler:
      triggers:
        - name: current-shared
          cron: "0 2 * * *"
          prompt: review nightly
  builder:
    provider: codex
    scheduler:
      triggers:
        - name: current-shared
          cron: "0 3 * * *"
          prompt: build nightly
`)
	const historicalTriggerID = "removed-reviewer-trigger"
	historicalRunID := identity.NewRandomID(identity.ResourceRun)
	legacyTriggerID := identity.NewRandomID(identity.ResourceTrigger)
	legacyRunID := identity.NewRandomID(identity.ResourceRun)
	probeRequests := make([]*agentcomposev2.ListSchedulerRunsRequest, 0)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			listSchedulerRuns: func(_ context.Context, req *connect.Request[agentcomposev2.ListSchedulerRunsRequest]) (*connect.Response[agentcomposev2.ListSchedulerRunsResponse], error) {
				probeRequests = append(probeRequests, req.Msg)
				var runID string
				runAgent := req.Msg.GetAgentName()
				switch {
				case req.Msg.GetTriggerId() == historicalTriggerID && (req.Msg.GetAgentName() == "" || req.Msg.GetAgentName() == "reviewer"):
					runID = historicalRunID
					runAgent = "reviewer"
				case req.Msg.GetTriggerId() == "removed-shared-trigger" && (req.Msg.GetAgentName() == "reviewer" || req.Msg.GetAgentName() == "builder"):
					runID = identity.NewID(identity.ResourceRun, req.Msg.GetAgentName(), req.Msg.GetTriggerId())
				case req.Msg.GetTriggerId() == legacyTriggerID && req.Msg.GetAgentName() == "reviewer":
					runID = legacyRunID
				default:
					return connect.NewResponse(&agentcomposev2.ListSchedulerRunsResponse{}), nil
				}
				return connect.NewResponse(&agentcomposev2.ListSchedulerRunsResponse{Runs: []*agentcomposev2.SchedulerRun{{
					RunId: runID, AgentName: runAgent, TriggerId: req.Msg.GetTriggerId(),
					Status: agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SUCCEEDED,
				}}}), nil
			},
			listProjectSchedulerEvents: func(_ context.Context, req *connect.Request[agentcomposev2.ListProjectSchedulerEventsRequest]) (*connect.Response[agentcomposev2.ListProjectSchedulerEventsResponse], error) {
				if req.Msg.GetAgentName() != "" || req.Msg.GetTriggerId() != historicalTriggerID {
					t.Fatalf("ListProjectSchedulerEvents filter = agent %q trigger %q", req.Msg.GetAgentName(), req.Msg.GetTriggerId())
				}
				return connect.NewResponse(&agentcomposev2.ListProjectSchedulerEventsResponse{Events: []*agentcomposev2.SchedulerEvent{{
					Id: "historical-event", RunId: historicalRunID, AgentName: "reviewer", TriggerId: historicalTriggerID, Message: "historical log",
				}}}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "runs", "--trigger", historicalTriggerID, "--json", "--host", server.URL, "--file", composePath)
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, historicalRunID) || !strings.Contains(stdout, `"trigger_id": "`+historicalTriggerID+`"`) {
		t.Fatalf("historical scheduler runs code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}

	stdout, stderr, _, exitCode = executeCLICommand("scheduler", "logs", "--trigger", historicalTriggerID, "--json", "--host", server.URL, "--file", composePath)
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, `"id": "historical-event"`) || !strings.Contains(stdout, `"message": "historical log"`) {
		t.Fatalf("historical scheduler logs code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}

	stdout, stderr, _, exitCode = executeCLICommand("scheduler", "runs", "reviewer", "--trigger", identity.Prefix+legacyTriggerID, "--json", "--host", server.URL, "--file", composePath)
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, legacyRunID) {
		t.Fatalf("legacy historical trigger ID code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	if len(probeRequests) == 0 || probeRequests[len(probeRequests)-2].GetTriggerId() != legacyTriggerID {
		t.Fatalf("legacy historical trigger was not normalized in requests: %#v", probeRequests)
	}

	_, stderr, _, exitCode = executeCLICommand("scheduler", "runs", "reviewer", "--trigger", "typo-with-no-history", "--host", server.URL, "--file", composePath)
	if exitCode != exitCodeUsage || !strings.Contains(stderr, `scheduler trigger "typo-with-no-history" not found`) {
		t.Fatalf("missing historical trigger code/stderr = %d / %q", exitCode, stderr)
	}

	_, stderr, _, exitCode = executeCLICommand("scheduler", "runs", "--trigger", "removed-shared-trigger", "--host", server.URL, "--file", composePath)
	if exitCode != exitCodeUsage || !strings.Contains(stderr, "ambiguous") || !strings.Contains(stderr, "specify a scheduler") {
		t.Fatalf("ambiguous historical trigger code/stderr = %d / %q", exitCode, stderr)
	}

	requestsBeforeCurrentAmbiguity := len(probeRequests)
	_, stderr, _, exitCode = executeCLICommand("scheduler", "logs", "--trigger", "current-shared", "--host", server.URL, "--file", composePath)
	if exitCode != exitCodeUsage || !strings.Contains(stderr, "ambiguous") || !strings.Contains(stderr, "--scheduler") {
		t.Fatalf("ambiguous current trigger code/stderr = %d / %q", exitCode, stderr)
	}
	if len(probeRequests) != requestsBeforeCurrentAmbiguity {
		t.Fatalf("current trigger ambiguity performed historical probes: before=%d after=%d", requestsBeforeCurrentAmbiguity, len(probeRequests))
	}
}

func TestIntegrationCLISchedulerTriggerUsesSchedulerRunAPI(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-scheduler-trigger
agents:
  reviewer:
    provider: codex
    scheduler:
      triggers:
        - name: nightly
          cron: "0 2 * * *"
          prompt: review nightly
`)
	var requestedPayloads []string
	var requestedTriggerIDs []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			runScheduler: func(ctx context.Context, req *connect.Request[agentcomposev2.RunSchedulerRequest]) (*connect.Response[agentcomposev2.RunSchedulerResponse], error) {
				requestedPayloads = append(requestedPayloads, req.Msg.GetPayloadJson())
				requestedTriggerIDs = append(requestedTriggerIDs, req.Msg.GetTriggerId())
				if req.Msg.GetAgentName() != "reviewer" || !identity.IsID(req.Msg.GetTriggerId()) {
					t.Fatalf("RunScheduler request = %#v", req.Msg)
				}
				return connect.NewResponse(&agentcomposev2.RunSchedulerResponse{Run: &agentcomposev2.SchedulerRun{
					RunId:       "scheduler-run-1",
					ProjectId:   req.Msg.GetProject().GetProjectId(),
					AgentName:   req.Msg.GetAgentName(),
					TriggerId:   req.Msg.GetTriggerId(),
					Status:      agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SUCCEEDED,
					ResultJson:  `{"ok":true}`,
					PayloadJson: req.Msg.GetPayloadJson(),
				}}), nil
			},
		},
	})
	defer server.Close()

	for _, tc := range []struct {
		name      string
		extraArgs []string
	}{
		{name: "runs trigger"},
		{name: "passes payload", extraArgs: []string{"--payload", `{"topic":"nightly"}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"scheduler", "trigger", "--host", server.URL, "--file", composePath}
			args = append(args, tc.extraArgs...)
			args = append(args, "reviewer", "nightly")
			stdout, stderr, _, exitCode := executeCLICommand(args...)
			if exitCode != 0 || !strings.Contains(stdout, "Status: succeeded") || !strings.Contains(stdout, `Result: {"ok":true}`) || stderr != "" {
				t.Fatalf("scheduler trigger code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
			}
		})
	}
	if !reflect.DeepEqual(requestedPayloads, []string{"", `{"topic":"nightly"}`}) {
		t.Fatalf("scheduler trigger payloads = %#v", requestedPayloads)
	}
	if len(requestedTriggerIDs) != 2 || requestedTriggerIDs[0] != requestedTriggerIDs[1] {
		t.Fatalf("scheduler trigger IDs = %#v", requestedTriggerIDs)
	}
}

func TestRunComposeSchedulerTriggerRejectsUnsupportedPrompt(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("prompt", "", "")
	if err := cmd.Flags().Set("prompt", " "); err != nil {
		t.Fatalf("set prompt flag: %v", err)
	}
	err := runComposeSchedulerTriggerCommand(cmd, cliOptions{}, composeSchedulerTriggerOptions{Prompt: " "}, "reviewer", "nightly")
	var exitErr commandExitError
	if !errors.As(err, &exitErr) || exitErr.Code != exitCodeUsage || !strings.Contains(err.Error(), "--prompt is unsupported for complete scheduler runs") {
		t.Fatalf("prompt error = %#v", err)
	}
}

func TestIntegrationCLISchedulerInspectDeclarativeTriggerYAML(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-scheduler-inspect
agents:
  reviewer:
    provider: codex
    scheduler:
      triggers:
        - name: nightly
          cron: "0 2 * * *"
          prompt: review nightly
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: testCLIProject(req.Msg.GetProject().GetProjectId(), "cli-scheduler-inspect", composePath)}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "inspect", "--host", server.URL, "--file", composePath, "--scheduler", "reviewer", "nightly")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("scheduler inspect code/stderr = %d / %q", exitCode, stderr)
	}
	if !strings.Contains(stdout, "name: nightly") || !strings.Contains(stdout, "cron: 0 2 * * *") || !strings.Contains(stdout, "prompt: review nightly") {
		t.Fatalf("scheduler inspect stdout = %q", stdout)
	}
	projectID, err := domain.StableProjectID("cli-scheduler-inspect", composePath)
	if err != nil {
		t.Fatalf("StableProjectID returned error: %v", err)
	}
	triggerID, err := domain.StableManagedTriggerID(projectID, "reviewer", "", "nightly", 0)
	if err != nil {
		t.Fatalf("StableManagedTriggerID returned error: %v", err)
	}
	shortOut, shortErr, _, shortCode := executeCLICommand("scheduler", "inspect", "--host", server.URL, "--file", composePath, "--scheduler", "reviewer", shortOpaqueID(triggerID))
	if shortCode != 0 || shortErr != "" || !strings.Contains(shortOut, "name: nightly") {
		t.Fatalf("scheduler inspect short trigger code/stdout/stderr = %d / %q / %q", shortCode, shortOut, shortErr)
	}
	_, legacyErr, _, legacyCode := executeCLICommand("scheduler", "inspect", "--host", server.URL, "--file", composePath, "reviewer", "nightly")
	if legacyCode != exitCodeUsage || !strings.Contains(legacyErr, "use --scheduler <scheduler-ref>") {
		t.Fatalf("legacy scheduler inspect code/stderr = %d / %q", legacyCode, legacyErr)
	}
}

func TestIntegrationCLISchedulerInspectLoaderRegisteredTrigger(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-scheduler-loader
agents:
  reviewer:
    provider: codex
    scheduler:
      script: |
        scheduler.interval("loader-every-minute", async function() {}, 60000);
`)
	var requestedAgent string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				project := testCLIProject(req.Msg.GetProject().GetProjectId(), "cli-scheduler-loader", composePath)
				return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: project}), nil
			},
			getScheduler: func(_ context.Context, req *connect.Request[agentcomposev2.GetSchedulerRequest]) (*connect.Response[agentcomposev2.GetSchedulerResponse], error) {
				requestedAgent = req.Msg.GetAgentName()
				return connect.NewResponse(&agentcomposev2.GetSchedulerResponse{Triggers: []*agentcomposev2.ResolvedTrigger{{TriggerId: "loader-every-minute", Enabled: true, Spec: &agentcomposev2.TriggerSpec{Kind: "interval", Interval: "1m"}, NextFireAt: timestamppb.New(time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)), LastFiredAt: timestamppb.New(time.Date(2026, 7, 6, 11, 59, 0, 0, time.UTC))}}}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "inspect", "--host", server.URL, "--file", composePath, "--scheduler", "reviewer", "loader-every-minute")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("scheduler inspect loader code/stderr = %d / %q", exitCode, stderr)
	}
	if requestedAgent != "reviewer" || !strings.Contains(stdout, "trigger_id: loader-every-minute") || !strings.Contains(stdout, "interval_ms: 60000") || !strings.Contains(stdout, "kind: interval") {
		t.Fatalf("requestedAgent=%q stdout=%q", requestedAgent, stdout)
	}
}

func inlineSchedulerComposeYAML(name string, intervalMs int) string {
	return fmt.Sprintf(`
name: %s
agents:
  reviewer:
    provider: codex
    image: guest:v1
    driver:
      docker: {}
    scheduler:
      script: |
        scheduler.interval("interval-review", function intervalReview() {}, %d);
`, name, intervalMs)
}

func (s projectServiceStub) ListSchedulerEvents(ctx context.Context, req *connect.Request[agentcomposev2.ListSchedulerEventsRequest]) (*connect.Response[agentcomposev2.ListSchedulerEventsResponse], error) {
	if s.listSchedulerEvents == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ListSchedulerEvents stub is not configured"))
	}
	return s.listSchedulerEvents(ctx, req)
}

func (s projectServiceStub) GetScheduler(ctx context.Context, req *connect.Request[agentcomposev2.GetSchedulerRequest]) (*connect.Response[agentcomposev2.GetSchedulerResponse], error) {
	if s.getScheduler == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("GetScheduler stub is not configured"))
	}
	return s.getScheduler(ctx, req)
}

func (s projectServiceStub) RunScheduler(ctx context.Context, req *connect.Request[agentcomposev2.RunSchedulerRequest]) (*connect.Response[agentcomposev2.RunSchedulerResponse], error) {
	if s.runScheduler == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("RunScheduler stub is not configured"))
	}
	return s.runScheduler(ctx, req)
}

func (s projectServiceStub) StartSchedulerRun(ctx context.Context, req *connect.Request[agentcomposev2.StartSchedulerRunRequest]) (*connect.Response[agentcomposev2.StartSchedulerRunResponse], error) {
	if s.startSchedulerRun == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("StartSchedulerRun stub is not configured"))
	}
	return s.startSchedulerRun(ctx, req)
}

func (s projectServiceStub) GetSchedulerRun(ctx context.Context, req *connect.Request[agentcomposev2.GetSchedulerRunRequest]) (*connect.Response[agentcomposev2.GetSchedulerRunResponse], error) {
	if s.getSchedulerRun == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("GetSchedulerRun stub is not configured"))
	}
	return s.getSchedulerRun(ctx, req)
}

func (s projectServiceStub) ListSchedulerRuns(ctx context.Context, req *connect.Request[agentcomposev2.ListSchedulerRunsRequest]) (*connect.Response[agentcomposev2.ListSchedulerRunsResponse], error) {
	if s.listSchedulerRuns == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ListSchedulerRuns stub is not configured"))
	}
	return s.listSchedulerRuns(ctx, req)
}

func (s projectServiceStub) StopSchedulerRun(ctx context.Context, req *connect.Request[agentcomposev2.StopSchedulerRunRequest]) (*connect.Response[agentcomposev2.StopSchedulerRunResponse], error) {
	if s.stopSchedulerRun == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("StopSchedulerRun stub is not configured"))
	}
	return s.stopSchedulerRun(ctx, req)
}
