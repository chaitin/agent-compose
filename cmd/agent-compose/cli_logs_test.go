package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestIntegrationCLIRunDetachCommandCanBeFollowedByLogs(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-detach-logs
agents:
  reviewer:
    provider: codex
`)
	var sawCommand bool
	var sawFollow bool
	var followRequests []*agentcomposev2.FollowRunLogsRequest
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			startRun: func(ctx context.Context, req *connect.Request[agentcomposev2.StartRunRequest]) (*connect.Response[agentcomposev2.StartRunResponse], error) {
				if req.Msg.GetRun().GetCommand() != "printf detached" {
					t.Fatalf("StartRun command = %#v", req.Msg.GetRun())
				}
				sawCommand = true
				return connect.NewResponse(&agentcomposev2.StartRunResponse{Run: &agentcomposev2.RunSummary{
					RunId:     "run-detached-logs",
					ProjectId: req.Msg.GetRun().GetProjectId(),
					AgentName: "reviewer",
					Status:    agentcomposev2.RunStatus_RUN_STATUS_RUNNING,
					SandboxId: "sandbox-detached-logs",
					Source:    agentcomposev2.RunSource_RUN_SOURCE_MANUAL,
				}, Started: true}), nil
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-detached-logs", "reviewer", "sandbox-detached-logs", agentcomposev2.RunStatus_RUN_STATUS_RUNNING, 0, "")}), nil
			},
			followRunLogs: func(ctx context.Context, req *connect.Request[agentcomposev2.FollowRunLogsRequest], stream *connect.ServerStream[agentcomposev2.RunLogChunk]) error {
				sawFollow = true
				followRequests = append(followRequests, req.Msg)
				if req.Msg.GetRunId() != "run-detached-logs" || !req.Msg.GetFollow() || req.Msg.GetTailLines() != 3 {
					t.Fatalf("FollowRunLogs request = %#v", req.Msg)
				}
				if err := stream.Send(&agentcomposev2.RunLogChunk{
					Data:      "history output\n",
					Offset:    uint64(len("history output\n")),
					CreatedAt: "2026-07-04T08:00:00Z",
				}); err != nil {
					return err
				}
				if err := stream.Send(&agentcomposev2.RunLogChunk{
					Data:      "live output\n",
					Offset:    uint64(len("history output\nlive output\n")),
					CreatedAt: "2026-07-04T08:00:01Z",
				}); err != nil {
					return err
				}
				return stream.Send(&agentcomposev2.RunLogChunk{
					Offset:    uint64(len("history output\nlive output\n")),
					IsFinal:   true,
					RunStatus: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
					CreatedAt: "2026-07-04T08:00:02Z",
				})
			},
		},
	})
	defer server.Close()

	_, stderr, _, exitCode := executeCLICommand("run", "-d", "--host", server.URL, "--file", composePath, "reviewer", "--command", "printf detached")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run -d --command code/stderr = %d / %q", exitCode, stderr)
	}
	logOut, logErr, _, logCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--run", "run-detached-logs", "--follow", "--tail", "3")
	runPrefix := "reviewer-run-detached-log | "
	wantLogOut := expectedLogSeparator(runPrefix, ">") +
		"reviewer-run-detached-log | test prompt\n" +
		expectedLogSeparator(runPrefix, "<") +
		"reviewer-run-detached-log | history output\n" +
		"reviewer-run-detached-log | live output\n"
	if logCode != 0 || logErr != "" || logOut != wantLogOut {
		t.Fatalf("logs --follow code/stdout/stderr = %d / %q / %q", logCode, logOut, logErr)
	}
	if strings.Count(logOut, "history output") != 1 || strings.Count(logOut, "live output") != 1 {
		t.Fatalf("logs --follow output duplicated chunk(s): %q", logOut)
	}
	if !sawCommand || !sawFollow {
		t.Fatalf("sawCommand=%v sawFollow=%v", sawCommand, sawFollow)
	}
	if len(followRequests) != 1 {
		t.Fatalf("FollowRunLogs calls = %d, want 1", len(followRequests))
	}
}

func TestIntegrationCLILogsTailTextJSONAndRunID(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-logs-tail
agents:
  reviewer:
    provider: codex
`)
	output := "one\ntwo\nthree\n"
	server := newRunServiceStubServer(t, runServiceStub{
		listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
			return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{{
				RunId:     "run-tail",
				ProjectId: req.Msg.GetProjectId(),
				AgentName: "reviewer",
				Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
				SandboxId: "session-tail",
			}}}), nil
		},
		getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
			return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-tail", "reviewer", "session-tail", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, output)}), nil
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--tail", "2")
	tailPrefix := "reviewer-run-tail | "
	wantTail := expectedLogSeparator(tailPrefix, ">") +
		"reviewer-run-tail | test prompt\n" +
		expectedLogSeparator(tailPrefix, "<") +
		"reviewer-run-tail | two\n" +
		"reviewer-run-tail | three\n"
	if exitCode != 0 || stderr != "" || stdout != wantTail {
		t.Fatalf("logs --tail text code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "-n", "2", "--json")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("logs --tail --json code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeLogsOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("logs --tail JSON decode failed: %v\n%s", err, jsonOut)
	}
	if len(decoded.Runs) != 1 || decoded.Runs[0].Prompt != "test prompt" || decoded.Runs[0].Content != "two\nthree\n" {
		t.Fatalf("logs --tail JSON = %#v", decoded)
	}

	runOut, runErr, _, runCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--run", "run-tail", "-n", "1")
	wantRunTail := expectedLogSeparator(tailPrefix, ">") +
		"reviewer-run-tail | test prompt\n" +
		expectedLogSeparator(tailPrefix, "<") +
		"reviewer-run-tail | three\n"
	if runCode != 0 || runErr != "" || runOut != wantRunTail {
		t.Fatalf("logs --run --tail code/stdout/stderr = %d / %q / %q", runCode, runOut, runErr)
	}
}

func TestIntegrationCLILogsTimestampAndMultiRunPrefixes(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-logs-prefix
agents:
  reviewer:
    provider: codex
  writer:
    provider: codex
`)
	server := newRunServiceStubServer(t, runServiceStub{
		listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
			return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{
				{
					RunId:     "run-reviewer",
					ProjectId: req.Msg.GetProjectId(),
					AgentName: "reviewer",
					Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
					SandboxId: "session-reviewer",
				},
				{
					RunId:     "run-writer",
					ProjectId: req.Msg.GetProjectId(),
					AgentName: "writer",
					Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
					SandboxId: "session-writer",
				},
			}}), nil
		},
		getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
			switch req.Msg.GetRunId() {
			case "run-reviewer":
				run := testRunDetail(req.Msg.GetProjectId(), "run-reviewer", "reviewer", "session-reviewer", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "review one\n")
				run.Summary.StartedAt = "2026-06-11T00:00:02Z"
				run.Summary.CompletedAt = "2026-06-11T00:00:03Z"
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: run}), nil
			case "run-writer":
				run := testRunDetail(req.Msg.GetProjectId(), "run-writer", "writer", "session-writer", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "write one\nwrite two\n")
				run.Summary.StartedAt = "2026-06-11T00:00:01Z"
				run.Summary.CompletedAt = "2026-06-11T00:00:04Z"
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: run}), nil
			default:
				t.Fatalf("unexpected run id %q", req.Msg.GetRunId())
				return nil, nil
			}
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--timestamp")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("logs --timestamp code/stderr = %d / %q", exitCode, stderr)
	}
	writerPrefix := "writer-run-writer [2026-06-11T00:00:04.000Z]| "
	reviewerPrefix := "reviewer-run-reviewer [2026-06-11T00:00:03.000Z]| "
	want := expectedLogSeparator(writerPrefix, ">") +
		"writer-run-writer [2026-06-11T00:00:04.000Z]| test prompt\n" +
		expectedLogSeparator(writerPrefix, "<") +
		"writer-run-writer [2026-06-11T00:00:04.000Z]| write one\n" +
		"writer-run-writer [2026-06-11T00:00:04.000Z]| write two\n" +
		expectedLogSeparator(reviewerPrefix, ">") +
		"reviewer-run-reviewer [2026-06-11T00:00:03.000Z]| test prompt\n" +
		expectedLogSeparator(reviewerPrefix, "<") +
		"reviewer-run-reviewer [2026-06-11T00:00:03.000Z]| review one\n"
	if stdout != want {
		t.Fatalf("logs --timestamp stdout = %q, want %q", stdout, want)
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--json")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("logs --json code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeLogsOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("logs --json decode failed: %v\n%s", err, jsonOut)
	}
	if len(decoded.Runs) != 2 || decoded.Runs[0].RunID != displayOpaqueID("run-writer") || decoded.Runs[1].RunID != displayOpaqueID("run-reviewer") {
		t.Fatalf("logs --json order = %#v", decoded.Runs)
	}
}

func TestLogsAgentFlagAndPositionalIsUsageError(t *testing.T) {
	stdout, stderr, _, exitCode := executeCLICommand("logs", "reviewer", "--agent", "writer")
	if exitCode != exitCodeUsage {
		t.Fatalf("logs positional and --agent exit code = %d, want %d", exitCode, exitCodeUsage)
	}
	if stdout != "" {
		t.Fatalf("logs positional and --agent stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "positionally or with --agent") {
		t.Fatalf("logs positional and --agent stderr = %q", stderr)
	}
}

func expectedLogSeparator(prefix, marker string) string {
	width := 80 - len(prefix)
	if width < 8 {
		width = 8
	}
	return prefix + strings.Repeat(marker, width) + "\n"
}

func TestRunLogLinePrefixWidthUsesDisplayWidth(t *testing.T) {
	summary := &agentcomposev2.RunSummary{
		RunId:       "run-123456789abc",
		AgentName:   "审查",
		CompletedAt: "2026-06-11T00:00:03Z",
	}
	if got, want := runLogLinePrefixWidth(summary, "", false), 24; got != want {
		t.Fatalf("runLogLinePrefixWidth without timestamp = %d, want %d", got, want)
	}
	if got, want := runLogLinePrefixWidth(summary, summary.GetCompletedAt(), true), 50; got != want {
		t.Fatalf("runLogLinePrefixWidth with timestamp = %d, want %d", got, want)
	}
}

func TestWriteLogDetailsFollowPrintsPromptOnce(t *testing.T) {
	detail := testRunDetail("project-logs", "run-follow-repeat", "reviewer", "session-follow-repeat", agentcomposev2.RunStatus_RUN_STATUS_RUNNING, 0, "first\n")
	printed := map[string]runLogPrintState{}
	options := composeLogsOptions{Follow: true, TailLines: -1}
	var out bytes.Buffer
	if err := writeLogDetails(&out, []*agentcomposev2.RunDetail{detail}, printed, options); err != nil {
		t.Fatalf("writeLogDetails first follow call returned error: %v", err)
	}
	detail.Output = "first\nsecond\n"
	if err := writeLogDetails(&out, []*agentcomposev2.RunDetail{detail}, printed, options); err != nil {
		t.Fatalf("writeLogDetails second follow call returned error: %v", err)
	}
	if err := writeLogDetails(&out, []*agentcomposev2.RunDetail{detail}, printed, options); err != nil {
		t.Fatalf("writeLogDetails third follow call returned error: %v", err)
	}
	got := out.String()
	promptPrefix := runLogPrefix(detail.GetSummary()) + " | "
	promptSeparator := expectedLogSeparator(promptPrefix, ">")
	if strings.Count(got, promptSeparator) != 1 {
		t.Fatalf("follow log prompt printed %d times; output = %q", strings.Count(got, promptSeparator), got)
	}
	if !strings.Contains(got, "| test prompt\n") ||
		!strings.Contains(got, "| first\n") ||
		!strings.Contains(got, "| second\n") {
		t.Fatalf("follow log output missing expected content: %q", got)
	}
}

func TestIntegrationCLILogsFollowUsesServerStream(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-logs-follow
agents:
  reviewer:
    provider: codex
`)
	var listCalls int
	var getCalls int
	var followCalls int
	server := newRunServiceStubServer(t, runServiceStub{
		listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
			listCalls++
			return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{{
				RunId:     "run-follow",
				ProjectId: req.Msg.GetProjectId(),
				AgentName: "reviewer",
				Status:    agentcomposev2.RunStatus_RUN_STATUS_RUNNING,
				SandboxId: "session-follow",
			}}}), nil
		},
		getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
			getCalls++
			if req.Msg.GetRunId() != "run-follow" {
				t.Fatalf("GetRun request = %#v", req.Msg)
			}
			run := testRunDetail(req.Msg.GetProjectId(), "run-follow", "reviewer", "session-follow", agentcomposev2.RunStatus_RUN_STATUS_RUNNING, 0, "")
			if getCalls == 1 {
				run.Prompt = ""
				run.ResultJson = "{}"
			} else {
				run.Prompt = ""
				run.ResultJson = `{"mode":"command","command":"echo delayed prompt"}`
			}
			return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: run}), nil
		},
		followRunLogs: func(ctx context.Context, req *connect.Request[agentcomposev2.FollowRunLogsRequest], stream *connect.ServerStream[agentcomposev2.RunLogChunk]) error {
			followCalls++
			if req.Msg.GetRunId() != "run-follow" || !req.Msg.GetFollow() || req.Msg.GetTailLines() != 2 {
				t.Fatalf("FollowRunLogs request = %#v", req.Msg)
			}
			if err := stream.Send(&agentcomposev2.RunLogChunk{Data: "first\n", Offset: 6, RunStatus: agentcomposev2.RunStatus_RUN_STATUS_RUNNING, CreatedAt: "2026-07-06T08:01:36.372Z"}); err != nil {
				return err
			}
			return stream.Send(&agentcomposev2.RunLogChunk{Data: "second\n", Offset: 13, RunStatus: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, CreatedAt: "2026-07-06T08:01:36.875Z", IsFinal: true})
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--follow", "--tail", "2", "--timestamp")
	if exitCode != 0 {
		t.Fatalf("logs follow exit code = %d, stderr=%q", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("logs follow stderr = %q, want empty", stderr)
	}
	followPrefix := "reviewer-run-follow [2026-06-11T00:00:01.000Z]| "
	want := expectedLogSeparator(followPrefix, ">") +
		"reviewer-run-follow [2026-06-11T00:00:01.000Z]| echo delayed prompt\n" +
		expectedLogSeparator(followPrefix, "<") +
		"reviewer-run-follow [2026-06-11T00:00:01.000Z]| first\n" +
		"reviewer-run-follow [2026-06-11T00:00:01.000Z]| second\n"
	if stdout != want {
		t.Fatalf("logs follow stdout = %q", stdout)
	}
	if listCalls != 1 || getCalls != 2 || followCalls != 1 {
		t.Fatalf("logs follow list/get/follow calls = %d/%d/%d, want 1/2/1", listCalls, getCalls, followCalls)
	}
}

func TestIntegrationCLILogsFollowPrintsDelayedPromptWithoutOutput(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-logs-follow-empty
agents:
  reviewer:
    provider: codex
`)
	var getCalls int
	server := newRunServiceStubServer(t, runServiceStub{
		listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
			return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{{
				RunId:     "run-follow-empty",
				ProjectId: req.Msg.GetProjectId(),
				AgentName: "reviewer",
				Status:    agentcomposev2.RunStatus_RUN_STATUS_RUNNING,
				SandboxId: "session-follow-empty",
			}}}), nil
		},
		getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
			getCalls++
			run := testRunDetail(req.Msg.GetProjectId(), "run-follow-empty", "reviewer", "session-follow-empty", agentcomposev2.RunStatus_RUN_STATUS_RUNNING, 0, "")
			run.Prompt = ""
			if getCalls == 1 {
				run.ResultJson = "{}"
			} else {
				run.ResultJson = `{"mode":"command","command":"true"}`
			}
			return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: run}), nil
		},
		followRunLogs: func(ctx context.Context, req *connect.Request[agentcomposev2.FollowRunLogsRequest], stream *connect.ServerStream[agentcomposev2.RunLogChunk]) error {
			return stream.Send(&agentcomposev2.RunLogChunk{RunStatus: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, IsFinal: true})
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--follow", "--timestamp")
	followEmptyPrefix := "reviewer-run-follow-empty [2026-06-11T00:00:01.000Z]| "
	want := expectedLogSeparator(followEmptyPrefix, ">") +
		"reviewer-run-follow-empty [2026-06-11T00:00:01.000Z]| true\n"
	if exitCode != 0 || stderr != "" || stdout != want {
		t.Fatalf("logs follow no output code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	if getCalls != 2 {
		t.Fatalf("GetRun calls = %d, want 2", getCalls)
	}
}

func TestLogsJSONFollowIsUsageError(t *testing.T) {
	stdout, stderr, _, exitCode := executeCLICommand("logs", "--json", "--follow")
	if exitCode != exitCodeUsage {
		t.Fatalf("logs --json --follow exit code = %d, want %d", exitCode, exitCodeUsage)
	}
	if stdout != "" {
		t.Fatalf("logs --json --follow stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "cannot be combined") {
		t.Fatalf("logs --json --follow stderr = %q", stderr)
	}
}

func (s runServiceStub) FollowRunLogs(ctx context.Context, req *connect.Request[agentcomposev2.FollowRunLogsRequest], stream *connect.ServerStream[agentcomposev2.RunLogChunk]) error {
	if s.followRunLogs == nil {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("FollowRunLogs stub is not configured"))
	}
	return s.followRunLogs(ctx, req, stream)
}
