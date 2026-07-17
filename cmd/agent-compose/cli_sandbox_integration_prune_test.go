package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestIntegrationCLISandboxPruneFlagsAndOlderThanUsage(t *testing.T) {
	helpOut, helpErr, runCount, helpCode := executeCLICommand("sandbox", "prune", "--help")
	if helpCode != 0 || helpErr != "" || runCount != 0 {
		t.Fatalf("sandbox prune --help code/stderr/runCount = %d / %q / %d", helpCode, helpErr, runCount)
	}
	for _, want := range []string{"--status", "--agent", "--driver", "--older-than", "--force"} {
		if !strings.Contains(helpOut, want) {
			t.Fatalf("sandbox prune --help output %q does not contain %q", helpOut, want)
		}
	}

	stdout, stderr, _, exitCode := executeCLICommand("sandbox", "prune", "--older-than", "0")
	if exitCode != exitCodeUsage {
		t.Fatalf("sandbox prune invalid older-than exit code = %d, want usage; stderr=%q", exitCode, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, `invalid --older-than "0": duration must be positive`) {
		t.Fatalf("sandbox prune invalid older-than stdout/stderr = %q / %q", stdout, stderr)
	}

}

func TestIntegrationCLISandboxPruneDryRunFiltersAndSafety(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-prune-demo
agents:
  reviewer:
    provider: codex
  worker:
    provider: codex
`)
	project := testCLIProject("project-cli-prune", "cli-prune-demo", composePath)
	oldTime := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	newTime := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339Nano)

	session := func(id, status, projectID, agent, driver, updatedAt string) *agentcomposev2.Sandbox {
		item := testCLISessionSummary(id, status, projectID, agent, "")
		item.Driver = driver
		if parsed, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
			item.UpdatedAt = timestamppb.New(parsed)
		} else if updatedAt != "" {
			item.UpdatedAt = timestamppb.New(time.Time{})
		} else {
			item.UpdatedAt = nil
		}
		return item
	}
	createdFallback := session("session-created-fallback", "STOPPED", "project-cli-prune", "reviewer", "docker", "")
	createdFallback.CreatedAt = timestamppb.New(time.Now().UTC().Add(-48 * time.Hour))
	sessions := []*agentcomposev2.Sandbox{
		session("session-stopped", "STOPPED", "project-cli-prune", "reviewer", "docker", oldTime),
		session("session-failed", "FAILED", "project-cli-prune", "worker", "boxlite", oldTime),
		session("session-running", "RUNNING", "project-cli-prune", "reviewer", "docker", oldTime),
		session("session-pending", "PENDING", "project-cli-prune", "worker", "boxlite", oldTime),
		session("session-error", "ERROR", "project-cli-prune", "worker", "microsandbox", oldTime),
		session("session-micro", "STOPPED", "project-cli-prune", "reviewer", "microsandbox", oldTime),
		session("session-new", "STOPPED", "project-cli-prune", "reviewer", "docker", newTime),
		createdFallback,
		session("session-bad-time", "STOPPED", "project-cli-prune", "reviewer", "docker", "not-a-time"),
		session("session-foreign", "STOPPED", "foreign-project", "reviewer", "docker", oldTime),
	}
	removeCalls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: project}), nil
			},
		},
		run: runServiceStub{
			listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{}}), nil
			},
		},
		session: sessionServiceStub{
			listSessions: func(ctx context.Context, req *connect.Request[agentcomposev2.ListSandboxesRequest]) (*connect.Response[agentcomposev2.ListSandboxesResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListSandboxesResponse{Sandboxes: sessions}), nil
			},
		},
		sandbox: sandboxServiceStub{
			removeSandbox: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
				removeCalls++
				t.Fatalf("dry-run prune called RemoveSandbox with %#v", req.Msg)
				return nil, nil
			},
		},
	})
	defer server.Close()

	runPrune := func(args ...string) composeSandboxPruneOutput {
		t.Helper()
		base := []string{"sandbox", "prune", "--host", server.URL, "--file", composePath, "--json"}
		stdout, stderr, _, exitCode := executeCLICommand(append(base, args...)...)
		if exitCode != 0 || stderr != "" {
			t.Fatalf("sandbox prune %v code/stderr = %d / %q", args, exitCode, stderr)
		}
		var decoded composeSandboxPruneOutput
		if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
			t.Fatalf("sandbox prune %v JSON decode failed: %v\n%s", args, err, stdout)
		}
		if !decoded.DryRun || len(decoded.Removed) != 0 || len(decoded.Skipped) != 0 {
			t.Fatalf("sandbox prune %v output = %#v", args, decoded)
		}
		return decoded
	}
	matched := func(output composeSandboxPruneOutput) map[string]bool {
		t.Helper()
		result := map[string]bool{}
		for _, sandbox := range output.Matched {
			result[sandbox.SandboxID] = true
		}
		return result
	}

	defaultMatches := matched(runPrune())
	for _, want := range []string{"session-stopped", "session-failed"} {
		if !defaultMatches[want] {
			t.Fatalf("default prune matched %#v, want %s", defaultMatches, want)
		}
	}
	for _, notWant := range []string{"session-running", "session-pending", "session-error", "session-foreign"} {
		if defaultMatches[notWant] {
			t.Fatalf("default prune matched %#v, should not include %s", defaultMatches, notWant)
		}
	}

	statusMatches := matched(runPrune("--status", "error"))
	if !reflect.DeepEqual(statusMatches, map[string]bool{"session-error": true}) {
		t.Fatalf("status error matches = %#v", statusMatches)
	}

	agentMatches := matched(runPrune("--agent", "worker"))
	if !agentMatches["session-failed"] || agentMatches["session-error"] || agentMatches["session-pending"] {
		t.Fatalf("agent worker matches = %#v", agentMatches)
	}

	driverMatches := matched(runPrune("--driver", "microsandbox"))
	if !reflect.DeepEqual(driverMatches, map[string]bool{"session-micro": true}) {
		t.Fatalf("driver microsandbox matches = %#v", driverMatches)
	}

	olderOutput := runPrune("--older-than", "24h")
	olderMatches := matched(olderOutput)
	for _, want := range []string{"session-stopped", "session-failed", "session-micro", "session-created-fallback"} {
		if !olderMatches[want] {
			t.Fatalf("older-than matches %#v, want %s", olderMatches, want)
		}
	}
	for _, notWant := range []string{"session-new", "session-bad-time", "session-foreign"} {
		if olderMatches[notWant] {
			t.Fatalf("older-than matches %#v, should not include %s", olderMatches, notWant)
		}
	}
	if len(olderOutput.Warnings) != 1 || !strings.Contains(olderOutput.Warnings[0], "session-bad-time") || !strings.Contains(olderOutput.Warnings[0], "invalid updated_at") {
		t.Fatalf("older-than warnings = %#v", olderOutput.Warnings)
	}
	if removeCalls != 0 {
		t.Fatalf("RemoveSandbox calls = %d, want 0", removeCalls)
	}
}

func TestIntegrationCLISandboxPruneForceRemovesMatchedAndReportsSkipped(t *testing.T) {
	type removeRequest struct {
		sandbox string
		force   bool
	}
	tests := []struct {
		name          string
		failSandbox   string
		wantExitCode  int
		wantRemoved   []string
		wantSkipped   []string
		wantRemoveSeq []removeRequest
	}{
		{
			name:         "all success",
			wantExitCode: 0,
			wantRemoved:  []string{"session-remove-a", "session-remove-b", "session-remove-c"},
			wantRemoveSeq: []removeRequest{
				{sandbox: "session-remove-a"},
				{sandbox: "session-remove-b"},
				{sandbox: "session-remove-c"},
			},
		},
		{
			name:         "partial failure",
			failSandbox:  "session-remove-b",
			wantExitCode: exitCodeGeneral,
			wantRemoved:  []string{"session-remove-a", "session-remove-c"},
			wantSkipped:  []string{"session-remove-b"},
			wantRemoveSeq: []removeRequest{
				{sandbox: "session-remove-a"},
				{sandbox: "session-remove-b"},
				{sandbox: "session-remove-c"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			composePath := writeComposeFile(t, t.TempDir(), `
name: cli-prune-force
agents:
  reviewer:
    provider: codex
  worker:
    provider: codex
`)
			project := testCLIProject("project-cli-prune-force", "cli-prune-force", composePath)
			sessions := []*agentcomposev2.Sandbox{
				testCLISessionSummary("session-remove-a", "STOPPED", "project-cli-prune-force", "reviewer", ""),
				testCLISessionSummary("session-remove-b", "FAILED", "project-cli-prune-force", "worker", ""),
				testCLISessionSummary("session-running", "RUNNING", "project-cli-prune-force", "reviewer", ""),
				testCLISessionSummary("session-foreign", "STOPPED", "foreign-project", "reviewer", ""),
				testCLISessionSummary("session-remove-c", "STOPPED", "project-cli-prune-force", "worker", ""),
			}
			var removed []removeRequest
			server := newComposeServiceStubServer(t, composeServiceStubs{
				project: projectServiceStub{
					getProject: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
						return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: project}), nil
					},
				},
				run: runServiceStub{
					listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
						return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{}}), nil
					},
				},
				session: sessionServiceStub{
					listSessions: func(ctx context.Context, req *connect.Request[agentcomposev2.ListSandboxesRequest]) (*connect.Response[agentcomposev2.ListSandboxesResponse], error) {
						return connect.NewResponse(&agentcomposev2.ListSandboxesResponse{Sandboxes: sessions}), nil
					},
				},
				sandbox: sandboxServiceStub{
					removeSandbox: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
						removed = append(removed, removeRequest{sandbox: req.Msg.GetSandboxId(), force: req.Msg.GetForce()})
						if req.Msg.GetForce() {
							t.Fatalf("sandbox prune RemoveSandbox force = true for %s", req.Msg.GetSandboxId())
						}
						if req.Msg.GetSandboxId() == tc.failSandbox {
							return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete denied"))
						}
						return connect.NewResponse(&agentcomposev2.RemoveSandboxResponse{SandboxId: req.Msg.GetSandboxId(), Removed: true}), nil
					},
				},
			})
			defer server.Close()

			stdout, stderr, _, exitCode := executeCLICommand("sandbox", "prune", "--host", server.URL, "--file", composePath, "--json", "--force")
			if exitCode != tc.wantExitCode {
				t.Fatalf("sandbox prune --force exit code = %d, want %d; stderr=%q", exitCode, tc.wantExitCode, stderr)
			}
			if tc.wantExitCode == 0 && stderr != "" {
				t.Fatalf("sandbox prune --force stderr = %q, want empty", stderr)
			}
			if tc.wantExitCode != 0 && !strings.Contains(stderr, "sandbox prune skipped") {
				t.Fatalf("sandbox prune --force stderr = %q, want skipped summary", stderr)
			}
			var decoded composeSandboxPruneOutput
			if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
				t.Fatalf("sandbox prune --force JSON decode failed: %v\n%s", err, stdout)
			}
			if decoded.DryRun {
				t.Fatalf("sandbox prune --force output is dry-run: %#v", decoded)
			}
			if !reflect.DeepEqual(decoded.Removed, tc.wantRemoved) {
				t.Fatalf("removed = %#v, want %#v", decoded.Removed, tc.wantRemoved)
			}
			var skipped []string
			for _, item := range decoded.Skipped {
				skipped = append(skipped, item.SandboxID)
				if !strings.Contains(item.Reason, "remove failed") {
					t.Fatalf("skipped reason = %q", item.Reason)
				}
				if item.SandboxID == "session-remove-b" && (item.Agent != "worker" || item.Status != "failed") {
					t.Fatalf("skipped metadata = %#v, want worker/failed context", item)
				}
			}
			if !reflect.DeepEqual(skipped, tc.wantSkipped) {
				t.Fatalf("skipped = %#v, want %#v", skipped, tc.wantSkipped)
			}
			if !reflect.DeepEqual(removed, tc.wantRemoveSeq) {
				t.Fatalf("RemoveSandbox calls = %#v, want %#v", removed, tc.wantRemoveSeq)
			}
			for _, item := range decoded.Matched {
				if item.SandboxID == "session-running" || item.SandboxID == "session-foreign" {
					t.Fatalf("matched unsafe/unowned sandbox in forced prune: %#v", decoded.Matched)
				}
			}
		})
	}
}

func TestIntegrationCLISandboxPruneTextOutput(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-prune-text
agents:
  reviewer:
    provider: codex
  worker:
    provider: codex
`)
	project := testCLIProject("project-cli-prune-text", "cli-prune-text", composePath)
	sessions := []*agentcomposev2.Sandbox{
		testCLISessionSummary("session-text-a", "STOPPED", "project-cli-prune-text", "reviewer", ""),
		testCLISessionSummary("session-text-b", "FAILED", "project-cli-prune-text", "worker", ""),
	}
	var fail bool
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: project}), nil
			},
		},
		run: runServiceStub{
			listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{}}), nil
			},
		},
		session: sessionServiceStub{
			listSessions: func(ctx context.Context, req *connect.Request[agentcomposev2.ListSandboxesRequest]) (*connect.Response[agentcomposev2.ListSandboxesResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListSandboxesResponse{Sandboxes: sessions}), nil
			},
		},
		sandbox: sandboxServiceStub{
			removeSandbox: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
				if fail && req.Msg.GetSandboxId() == "session-text-b" {
					return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete denied"))
				}
				return connect.NewResponse(&agentcomposev2.RemoveSandboxResponse{SandboxId: req.Msg.GetSandboxId(), Removed: true}), nil
			},
		},
	})
	defer server.Close()

	dryOut, dryErr, _, dryCode := executeCLICommand("sandbox", "prune", "--host", server.URL, "--file", composePath)
	if dryCode != 0 || dryErr != "" {
		t.Fatalf("sandbox prune text dry-run code/stderr = %d / %q", dryCode, dryErr)
	}
	for _, want := range []string{"Dry-run: 2 matched, 0 skipped, 2 would be removed.", "Use --force", "Matched:", "SANDBOX", "AGENT", "STATUS", "DRIVER", "UPDATED", "REASON", "session-text", "would remove"} {
		if !strings.Contains(dryOut, want) {
			t.Fatalf("sandbox prune dry-run output %q does not contain %q", dryOut, want)
		}
	}

	forceOut, forceErr, _, forceCode := executeCLICommand("sandbox", "prune", "--host", server.URL, "--file", composePath, "--force")
	if forceCode != 0 || forceErr != "" {
		t.Fatalf("sandbox prune text force code/stderr = %d / %q", forceCode, forceErr)
	}
	for _, want := range []string{"Removed 2 sandbox(es); 2 matched, 0 skipped.", "Removed:", "session-text-a", "session-text-b", "Matched:", "matched"} {
		if !strings.Contains(forceOut, want) {
			t.Fatalf("sandbox prune force output %q does not contain %q", forceOut, want)
		}
	}

	fail = true
	skippedOut, skippedErr, _, skippedCode := executeCLICommand("sandbox", "prune", "--host", server.URL, "--file", composePath, "--force")
	if skippedCode != exitCodeGeneral {
		t.Fatalf("sandbox prune text skipped code = %d, want general; stderr=%q", skippedCode, skippedErr)
	}
	if !strings.Contains(skippedErr, "sandbox prune skipped 1 sandbox") {
		t.Fatalf("sandbox prune text skipped stderr = %q", skippedErr)
	}
	for _, want := range []string{"Removed 1 sandbox(es); 2 matched, 1 skipped.", "Skipped:", "session-text-b", "worker", "failed", "remove failed"} {
		if !strings.Contains(skippedOut, want) {
			t.Fatalf("sandbox prune skipped output %q does not contain %q", skippedOut, want)
		}
	}
}

func TestIntegrationCLISandboxPruneRejectsUnsafeStatuses(t *testing.T) {
	for _, status := range []string{"running", "pending"} {
		t.Run(status, func(t *testing.T) {
			stdout, stderr, _, exitCode := executeCLICommand("sandbox", "prune", "--status", status)
			if exitCode != exitCodeUsage {
				t.Fatalf("sandbox prune --status %s exit code = %d, want usage; stderr=%q", status, exitCode, stderr)
			}
			if stdout != "" || !strings.Contains(stderr, "sandbox prune cannot target "+status+" sandboxes") {
				t.Fatalf("sandbox prune --status %s stdout/stderr = %q / %q", status, stdout, stderr)
			}
		})
	}
}

func TestIntegrationCLISandboxPruneRejectsInvalidDriver(t *testing.T) {
	stdout, stderr, _, exitCode := executeCLICommand("sandbox", "prune", "--driver", "micro-sandbox")
	if exitCode != exitCodeUsage {
		t.Fatalf("sandbox prune --driver invalid exit code = %d, want usage; stderr=%q", exitCode, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, `invalid --driver "micro-sandbox": expected docker, boxlite, or microsandbox`) {
		t.Fatalf("sandbox prune --driver invalid stdout/stderr = %q / %q", stdout, stderr)
	}
}
