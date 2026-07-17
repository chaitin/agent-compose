package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"bytes"
	"strings"
	"testing"
)

func TestCLIOutputHelpersCoverEdgeBranches(t *testing.T) {
	project := &agentcomposev2.Project{
		Summary: &agentcomposev2.ProjectSummary{
			ProjectId:       "project-1",
			Name:            "Project",
			SourcePath:      "/tmp/agent-compose.yml",
			CurrentRevision: 2,
			SpecHash:        "hash",
			AgentCount:      1,
			SchedulerCount:  1,
		},
	}
	applyResp := &agentcomposev2.ApplyProjectResponse{
		Project:   project,
		Revision:  &agentcomposev2.ProjectRevision{Revision: 2, SpecHash: "hash"},
		Unchanged: true,
		Changes: []*agentcomposev2.ProjectChange{{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED,
			ResourceType: "agent",
			ResourceId:   "agent-1",
			Name:         "reviewer",
		}},
	}
	if output := composeUpOutputFromResponse(applyResp); !output.Unchanged || output.Project.ID != "project-1" || len(output.Changes) != 1 {
		t.Fatalf("composeUpOutputFromResponse = %#v", output)
	}
	var text bytes.Buffer
	if err := writeComposeUpText(&text, composeDisplayChangesFromProjectChanges(applyResp.GetChanges(), nil)); err != nil {
		t.Fatalf("writeComposeUpText returned error: %v", err)
	}
	if !strings.Contains(text.String(), "ACTION") || !strings.Contains(text.String(), "reviewer") {
		t.Fatalf("compose up text = %q", text.String())
	}

	removeResp := &agentcomposev2.RemoveProjectResponse{
		Project: project,
		Changes: []*agentcomposev2.ProjectChange{{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED,
			ResourceType: "sandbox",
			ResourceId:   "sandbox-1",
			Name:         "sandbox-1",
			Message:      "stop failed",
		}},
	}
	down := composeDownOutputFromResponse(removeResp)
	if down.Status != "partial-failure" || down.FailedSandboxStops != 1 {
		t.Fatalf("composeDownOutputFromResponse = %#v", down)
	}
	text.Reset()
	if err := writeComposeDownText(&text, composeDownDisplayChanges(removeResp, nil)); err != nil {
		t.Fatalf("writeComposeDownText returned error: %v", err)
	}
	if !strings.Contains(text.String(), "MESSAGE") || !strings.Contains(text.String(), "stop failed") {
		t.Fatalf("compose down text = %q", text.String())
	}

	serviceCount := uint32(3)
	projects := []composeProjectListItem{{
		ID:             "project-1",
		Name:           "Project",
		ConfigFile:     "/tmp/agent-compose.yml",
		ProjectDir:     "/tmp",
		Revision:       2,
		SpecHash:       "hash",
		AgentCount:     1,
		SchedulerCount: 1,
		ServiceCount:   &serviceCount,
		UpdatedAt:      "2026-07-06T00:00:00Z",
	}, {
		ID:        "project-removed",
		Name:      "Removed",
		RemovedAt: "2026-07-06T00:00:00Z",
	}}
	text.Reset()
	if err := writeProjectListText(&text, projects, true); err != nil {
		t.Fatalf("writeProjectListText verbose returned error: %v", err)
	}
	if !strings.Contains(text.String(), "SERVICES") || !strings.Contains(text.String(), "removed") || projectServiceCountText(nil) != "-" {
		t.Fatalf("project list text = %q", text.String())
	}

	value := 12.5
	stats := composeStatsOutputFromProto(&agentcomposev2.SandboxStats{
		SandboxId:        "sandbox-1",
		Driver:           "boxlite",
		SampledAt:        "2026-07-06T00:00:00Z",
		CpuPercent:       &agentcomposev2.MetricValue{Value: &value, Unit: "percent", Status: agentcomposev2.MetricStatus_METRIC_STATUS_OK},
		MemoryUsageBytes: &agentcomposev2.MetricValue{Value: &value, Unit: "bytes", Status: agentcomposev2.MetricStatus_METRIC_STATUS_UNAVAILABLE, Message: "n/a"},
		UptimeSeconds:    &agentcomposev2.MetricValue{Value: &value, Unit: "seconds", Status: agentcomposev2.MetricStatus_METRIC_STATUS_OK},
	})
	if stats.CPUPercent.Status != "ok" || stats.MemoryUsageBytes.Status != "unavailable" || composeStatsOutputFromProto(nil).SandboxID != "" {
		t.Fatalf("stats output = %#v", stats)
	}
	text.Reset()
	if err := writeStatsText(&text, []composeStatsOutput{stats}); err != nil {
		t.Fatalf("writeStatsText returned error: %v", err)
	}
	if !strings.Contains(text.String(), "12.50") || !strings.Contains(text.String(), "12s") {
		t.Fatalf("stats text = %q", text.String())
	}

	run := testRunDetail("project-1", "run-123456789", "reviewer", "sandbox-1", agentcomposev2.RunStatus_RUN_STATUS_FAILED, 9, "one\ntwo\nthree\n")
	run.Prompt = "prompt"
	run.ResultJson = `{"ok":true}`
	run.CleanupError = "cleanup failed"
	run.Driver = "boxlite"
	run.ImageRef = "agent:latest"
	run.Summary.Source = agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER
	run.Summary.Warnings = []string{"warn-a"}
	run.Warnings = []string{"warn-a", "warn-b"}
	runOutput := composeRunOutputFromDetailWithOptions(run, composeLogsOptions{TailLines: 2})
	if runOutput.Output != "two\nthree\n" || runOutput.Source != "scheduler" || len(runOutput.Warnings) != 2 {
		t.Fatalf("run output = %#v", runOutput)
	}
	text.Reset()
	if err := writeLogsForRun(&text, run, false, composeLogsOptions{TailLines: 1, Timestamp: true}); err != nil {
		t.Fatalf("writeLogsForRun text returned error: %v", err)
	}
	if !strings.Contains(text.String(), "reviewer-run-123456789") || !strings.Contains(text.String(), "three") {
		t.Fatalf("run logs text = %q", text.String())
	}
	text.Reset()
	if err := writeLogsForRun(&text, run, true, composeLogsOptions{TailLines: 1}); err != nil {
		t.Fatalf("writeLogsForRun JSON returned error: %v", err)
	}
	if !strings.Contains(text.String(), `"runs"`) || !strings.Contains(text.String(), `"three\n"`) {
		t.Fatalf("run logs json = %q", text.String())
	}

	execOutput := composeExecOutputFromResult(&agentcomposev2.ExecResult{
		ExecId: "exec-1", SandboxId: "sandbox-1", RunId: "run-1",
		Command: &agentcomposev2.ExecCommand{Command: "bash", Args: []string{"-lc", "false"}},
		Cwd:     "/workspace", ExitCode: 127, Success: false, Stdout: "out", Stderr: "err", Output: "outerr", Error: "failed",
	})
	if execOutput.Command != "bash" || execOutput.Args[0] != "-lc" || execResultExitCode(&agentcomposev2.ExecResult{ExitCode: 127}) != exitCodeGeneral {
		t.Fatalf("exec output = %#v", execOutput)
	}
}
