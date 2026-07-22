package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/spf13/pflag"

	"agent-compose/pkg/identity"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestSchedulerPruneCommandFlags(t *testing.T) {
	command, _, err := newCLISchedulerCommand(&cliOptions{}).Find([]string{"prune"})
	if err != nil {
		t.Fatalf("find scheduler prune: %v", err)
	}
	var flags []string
	command.LocalNonPersistentFlags().VisitAll(func(flag *pflag.Flag) { flags = append(flags, flag.Name) })
	if want := []string{"force", "older-than", "scheduler", "status", "trigger"}; !slices.Equal(flags, want) {
		t.Fatalf("local prune flags=%#v, want %#v", flags, want)
	}

	stdout, stderr, runCount, exitCode := executeCLICommand("scheduler", "prune", "--help")
	if exitCode != 0 || stderr != "" || runCount != 0 {
		t.Fatalf("help code/stdout/stderr/run=%d/%q/%q/%d", exitCode, stdout, stderr, runCount)
	}
	for _, want := range []string{"--scheduler", "--trigger", "--status", "--older-than", "--force", "--json", "dry-run"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("help missing %q:\n%s", want, stdout)
		}
	}
	for _, absent := range []string{"--all-runs", "--limit", "--keep-last"} {
		if strings.Contains(stdout, absent) {
			t.Fatalf("help contains unsupported %q:\n%s", absent, stdout)
		}
	}
}

func TestSchedulerPruneRejectsArgumentsAndInvalidFilters(t *testing.T) {
	composePath := writeSchedulerPruneCompose(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "position argument", args: []string{"extra"}, want: "unknown command"},
		{name: "running status", args: []string{"--status", "running"}, want: "must contain only succeeded, failed, canceled, or skipped"},
		{name: "pending status", args: []string{"--status", "pending"}, want: "must contain only succeeded, failed, canceled, or skipped"},
		{name: "invalid older than", args: []string{"--older-than", "zero"}, want: "invalid --older-than"},
		{name: "zero older than", args: []string{"--older-than", "0s"}, want: "duration must be positive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"scheduler", "prune"}, test.args...)
			args = append(args, "--file", composePath)
			stdout, stderr, runCount, exitCode := executeCLICommand(args...)
			if exitCode != exitCodeUsage || stdout != "" || runCount != 0 || !strings.Contains(stderr, test.want) {
				t.Fatalf("code/stdout/stderr/run=%d/%q/%q/%d, want %q", exitCode, stdout, stderr, runCount, test.want)
			}
		})
	}
}

func TestIntegrationCLISchedulerPruneMapsFiltersAndJSONStats(t *testing.T) {
	composePath := writeSchedulerPruneCompose(t)
	var captured *agentcomposev2.PruneSchedulerRunsRequest
	server := newComposeServiceStubServer(t, composeServiceStubs{project: projectServiceStub{
		pruneSchedulerRuns: func(_ context.Context, req *connect.Request[agentcomposev2.PruneSchedulerRunsRequest]) (*connect.Response[agentcomposev2.PruneSchedulerRunsResponse], error) {
			captured = req.Msg
			return connect.NewResponse(&agentcomposev2.PruneSchedulerRunsResponse{
				DryRun: true,
				Matched: &agentcomposev2.SchedulerRunPruneStats{
					Runs: 2, LoaderEvents: 5, EventDeliveries: 1, EventSandboxLinks: 1, ArtifactDirs: 2, ArtifactBytes: 19,
				},
			}), nil
		},
	}})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand(
		"scheduler", "prune", "--scheduler", "reviewer", "--trigger", "nightly",
		"--status", " failed,SUCCEEDED,failed ", "--older-than", "7d", "--json",
		"--host", server.URL, "--file", composePath,
	)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("prune code/stdout/stderr=%d/%q/%q", exitCode, stdout, stderr)
	}
	if captured == nil || captured.GetAgentName() != "reviewer" || captured.GetTriggerId() == "" ||
		!slices.Equal(captured.GetStatus(), []agentcomposev2.SchedulerRunStatus{
			agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_FAILED,
			agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SUCCEEDED,
		}) || captured.GetOlderThanSeconds() != 7*24*60*60 || captured.GetForce() {
		t.Fatalf("request=%#v", captured)
	}
	var output composeSchedulerPruneOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout)
	}
	if !output.DryRun || output.Matched.Runs != 2 || output.Matched.LoaderEvents != 5 || output.Matched.ArtifactBytes != 19 ||
		output.Scheduler != "reviewer" || output.TriggerID != captured.GetTriggerId() || !slices.Equal(output.Statuses, []string{"failed", "succeeded"}) {
		t.Fatalf("output=%#v", output)
	}
}

func TestIntegrationCLISchedulerPruneSupportsHistoricalTriggerID(t *testing.T) {
	composePath := writeSchedulerPruneCompose(t)
	const historicalTriggerID = "removed-trigger"
	probeRunID := identity.NewRandomID(identity.ResourceRun)
	var pruneRequest *agentcomposev2.PruneSchedulerRunsRequest
	server := newComposeServiceStubServer(t, composeServiceStubs{project: projectServiceStub{
		listSchedulerRuns: func(_ context.Context, req *connect.Request[agentcomposev2.ListSchedulerRunsRequest]) (*connect.Response[agentcomposev2.ListSchedulerRunsResponse], error) {
			if req.Msg.GetAgentName() == "reviewer" && req.Msg.GetTriggerId() == historicalTriggerID {
				return connect.NewResponse(&agentcomposev2.ListSchedulerRunsResponse{Runs: []*agentcomposev2.SchedulerRun{{
					RunId: probeRunID, AgentName: "reviewer", TriggerId: historicalTriggerID,
				}}}), nil
			}
			return connect.NewResponse(&agentcomposev2.ListSchedulerRunsResponse{}), nil
		},
		pruneSchedulerRuns: func(_ context.Context, req *connect.Request[agentcomposev2.PruneSchedulerRunsRequest]) (*connect.Response[agentcomposev2.PruneSchedulerRunsResponse], error) {
			pruneRequest = req.Msg
			return connect.NewResponse(&agentcomposev2.PruneSchedulerRunsResponse{DryRun: true, Matched: &agentcomposev2.SchedulerRunPruneStats{Runs: 1}}), nil
		},
	}})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "prune", "--scheduler", "reviewer", "--trigger", historicalTriggerID, "--host", server.URL, "--file", composePath)
	if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "1 scheduler trigger run(s) matched") {
		t.Fatalf("historical prune code/stdout/stderr=%d/%q/%q", exitCode, stdout, stderr)
	}
	if pruneRequest == nil || pruneRequest.GetAgentName() != "reviewer" || pruneRequest.GetTriggerId() != historicalTriggerID {
		t.Fatalf("prune request=%#v", pruneRequest)
	}
}

func TestIntegrationCLISchedulerPruneForcePartialResultIsNonZero(t *testing.T) {
	composePath := writeSchedulerPruneCompose(t)
	server := newComposeServiceStubServer(t, composeServiceStubs{project: projectServiceStub{
		pruneSchedulerRuns: func(context.Context, *connect.Request[agentcomposev2.PruneSchedulerRunsRequest]) (*connect.Response[agentcomposev2.PruneSchedulerRunsResponse], error) {
			return connect.NewResponse(&agentcomposev2.PruneSchedulerRunsResponse{
				Matched:     &agentcomposev2.SchedulerRunPruneStats{Runs: 3, ArtifactDirs: 2},
				Removed:     &agentcomposev2.SchedulerRunPruneStats{Runs: 2, ArtifactDirs: 1},
				SkippedRuns: 1,
				Residues:    []*agentcomposev2.SchedulerRunPruneResidue{{LoaderId: "loader-1", RunId: "run-1", Path: "/runs/run-1", Error: "permission denied"}},
			}), nil
		},
	}})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "prune", "--force", "--json", "--host", server.URL, "--file", composePath)
	if exitCode != exitCodeGeneral || !strings.Contains(stderr, "1 skipped run(s) and 1 artifact residue(s)") {
		t.Fatalf("partial prune code/stdout/stderr=%d/%q/%q", exitCode, stdout, stderr)
	}
	var output composeSchedulerPruneOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil || output.Removed.Runs != 2 || output.SkippedRuns != 1 || len(output.Residues) != 1 {
		t.Fatalf("partial output=%#v err=%v", output, err)
	}
}

func TestIntegrationCLISchedulerPruneUnimplementedDaemon(t *testing.T) {
	composePath := writeSchedulerPruneCompose(t)
	server := newComposeServiceStubServer(t, composeServiceStubs{project: projectServiceStub{}})
	defer server.Close()
	stdout, stderr, _, exitCode := executeCLICommand("scheduler", "prune", "--host", server.URL, "--file", composePath)
	if exitCode != exitCodeUnsupported || stdout != "" || !strings.Contains(stderr, "daemon does not support scheduler run pruning") {
		t.Fatalf("unimplemented code/stdout/stderr=%d/%q/%q", exitCode, stdout, stderr)
	}
}

func writeSchedulerPruneCompose(t *testing.T) string {
	t.Helper()
	return writeComposeFile(t, t.TempDir(), `
name: scheduler-prune-test
agents:
  reviewer:
    provider: codex
    scheduler:
      triggers:
        - name: nightly
          cron: "0 2 * * *"
          prompt: review nightly
`)
}
