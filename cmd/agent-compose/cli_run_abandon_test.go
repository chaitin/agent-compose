package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// TestIntegrationCLIAbandonCompletionEndsAStuckCleanup covers the operator path
// out of a run whose sandbox cleanup cannot converge (issue #771). The command
// must reach the RPC, report the terminal state, and say plainly that the
// runtime was not removed - an operator must never read this as a successful
// cleanup.
func TestIntegrationCLIAbandonCompletionEndsAStuckCleanup(t *testing.T) {
	var gotRequest *agentcomposev2.AbandonRunCompletionRequest
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			abandonCompletion: func(_ context.Context, req *connect.Request[agentcomposev2.AbandonRunCompletionRequest]) (*connect.Response[agentcomposev2.AbandonRunCompletionResponse], error) {
				gotRequest = req.Msg
				return connect.NewResponse(&agentcomposev2.AbandonRunCompletionResponse{
					Abandoned: true,
					Run: &agentcomposev2.RunDetail{
						Summary:      &agentcomposev2.RunSummary{RunId: "run-stuck", Status: agentcomposev2.RunStatus_RUN_STATUS_FAILED},
						CleanupError: "sandbox cleanup abandoned by operator; the sandbox runtime may still exist",
					},
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("abandon-completion", "run-stuck", "--reason", "cluster gone", "--host", server.URL)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("abandon-completion code/stderr = %d / %q", exitCode, stderr)
	}
	if gotRequest.GetRunId() != "run-stuck" || gotRequest.GetReason() != "cluster gone" {
		t.Fatalf("request = %#v, want the run id and the operator reason", gotRequest)
	}
	for _, want := range []string{"run-stuck", "failed", "cleanup abandoned", "may still exist"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("abandon-completion output %q does not contain %q", stdout, want)
		}
	}

	stdout, stderr, _, exitCode = executeCLICommand("abandon-completion", "run-stuck", "--host", server.URL, "--json")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("abandon-completion --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeRunOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("abandon-completion JSON decode: %v\n%s", err, stdout)
	}
	if decoded.ID != "run-stuck" || decoded.Status != "failed" || !strings.Contains(decoded.CleanupError, "may still exist") {
		t.Fatalf("decoded output = %#v", decoded)
	}
}

// TestIntegrationCLIAbandonCompletionReportsWhatStillNeedsCleaning keeps the two
// non-abandoning answers distinct: a run that was already terminal is a no-op,
// and a run that is still executing names the command that actually cleans up.
func TestIntegrationCLIAbandonCompletionReportsWhatStillNeedsCleaning(t *testing.T) {
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			abandonCompletion: func(_ context.Context, req *connect.Request[agentcomposev2.AbandonRunCompletionRequest]) (*connect.Response[agentcomposev2.AbandonRunCompletionResponse], error) {
				return connect.NewResponse(&agentcomposev2.AbandonRunCompletionResponse{
					Abandoned: false,
					Run: &agentcomposev2.RunDetail{
						Summary: &agentcomposev2.RunSummary{RunId: req.Msg.GetRunId(), Status: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED},
					},
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("abandon-completion", "run-done", "--host", server.URL)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("abandon-completion code/stderr = %d / %q", exitCode, stderr)
	}
	if !strings.Contains(stdout, "already terminal") || strings.Contains(stdout, "may still exist") {
		t.Fatalf("abandon-completion output = %q, want a no-op answer without a cleanup warning", stdout)
	}

	refusing := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			abandonCompletion: func(context.Context, *connect.Request[agentcomposev2.AbandonRunCompletionRequest]) (*connect.Response[agentcomposev2.AbandonRunCompletionResponse], error) {
				return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("run has no staged sandbox cleanup to abandon; it is still executing, so stop it instead"))
			},
		},
	})
	defer refusing.Close()
	_, stderr, _, exitCode = executeCLICommand("abandon-completion", "run-running", "--host", refusing.URL)
	if exitCode == 0 || !strings.Contains(stderr, "stop it instead") {
		t.Fatalf("abandon-completion refusal code/stderr = %d / %q, want the precondition message", exitCode, stderr)
	}

	stdout, stderr, _, exitCode = executeCLICommand("abandon-completion")
	if exitCode != exitCodeUsage || stdout != "" || !strings.Contains(stderr, "accepts 1 arg") {
		t.Fatalf("abandon-completion without a run = %d / %q / %q, want a usage error", exitCode, stdout, stderr)
	}
}
