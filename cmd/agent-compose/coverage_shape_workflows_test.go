package main

import (
	"bytes"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"

	"github.com/spf13/cobra"
)

func TestCLIConfigAndOutputWorkflow(t *testing.T) {
	testCLIConfigAndOutputWorkflow(t)
}

func TestIntegrationCLIConfigAndOutputWorkflow(t *testing.T) {
	testCLIConfigAndOutputWorkflow(t)
}

func TestE2ECLIConfigAndOutputWorkflow(t *testing.T) {
	testCLIConfigAndOutputWorkflow(t)
}

func testCLIConfigAndOutputWorkflow(t *testing.T) {
	t.Helper()
	TestRootCommandPrintsHelpWithoutStartingDaemon(t)
	TestUnknownCommandFailsWithoutStartingDaemon(t)
	TestVersionCommandPrintsBuildVersionWithoutStartingDaemon(t)
	TestConfigCommandPrintsNormalizedJSONWithoutStartingDaemon(t)
	TestConfigCommandPrintsNormalizedYAMLWithoutStartingDaemon(t)
	TestConfigCommandQuietOnlyValidates(t)
	TestConfigCommandQuietFailureIncludesComposePathAndField(t)
	TestConfigCommandMissingComposeFileWritesStderrAndExitCode(t)
	TestConfigCommandUsesGlobalFileProjectNameAndJSON(t)
	TestCLIClientConfigPriority(t)
	TestCLIClientConfigRejectsInvalidHost(t)
	TestStatusCommandUsesHostFlagBeforeEnvironment(t)
	TestStatusCommandUsesEnvironmentHost(t)
	TestStatusCommandUsesDefaultUnixSocket(t)
	TestStatusCommandUnavailableWritesStderrAndExitCode(t)
	TestStatusCommandReportsUnreadableDaemon(t)
	TestCLIDownFirstRepeatedPartialAndJSON(t)
	TestComposeImageOutputFromProtoAcceptsOCIStatus(t)
	TestLogsJSONFollowIsUsageError(t)
	TestInvalidHostWritesStderrAndUsageExitCode(t)
	testComposeProjectPureHelpers(t)
	testComposeProjectOutputHelpers(t)
	testComposeRunLogAndExecHelpers(t)
	testComposeRunExecAndLogsEdgeHelpers(t)
	testComposeImageStatsAndSessionHelpers(t)
}

func testComposeProjectPureHelpers(t *testing.T) {
	t.Helper()
	project := &compose.NormalizedProjectSpec{Agents: []compose.NormalizedAgentSpec{
		{Image: " guest:latest "},
		{Image: "guest:latest"},
		{Image: "worker:latest"},
		{Image: " "},
	}}
	if refs := projectImageRefs(project); len(refs) != 2 || refs[0] != "guest:latest" || refs[1] != "worker:latest" {
		t.Fatalf("projectImageRefs = %#v", refs)
	}

	item := composeProjectListItemFromSummary(&agentcomposev2.ProjectSummary{
		ProjectId:       "project-1",
		Name:            "Project",
		SourcePath:      "/tmp/project/agent-compose.yml",
		CurrentRevision: 3,
		SpecHash:        "hash",
		AgentCount:      2,
		SchedulerCount:  1,
		RunningRunCount: 4,
		LatestRunId:     "run-1",
		UpdatedAt:       "2026-07-04T00:00:00Z",
		RemovedAt:       "2026-07-05T00:00:00Z",
	})
	if item.ProjectDir != "/tmp/project" || projectListStatus(item) != "removed" {
		t.Fatalf("project list item = %#v", item)
	}
	count := uint32(5)
	item.ServiceCount = &count
	var out bytes.Buffer
	if err := writeProjectListText(&out, []composeProjectListItem{item}, true); err != nil {
		t.Fatalf("writeProjectListText verbose returned error: %v", err)
	}
	if !strings.Contains(out.String(), "SERVICES") || !strings.Contains(out.String(), "removed") || projectServiceCountText(item.ServiceCount) != "5" {
		t.Fatalf("verbose project list output = %q", out.String())
	}
	out.Reset()
	item.RemovedAt = ""
	item.ServiceCount = nil
	if err := writeProjectListText(&out, []composeProjectListItem{item}, false); err != nil {
		t.Fatalf("writeProjectListText returned error: %v", err)
	}
	if !strings.Contains(out.String(), "-") || projectListStatus(item) != "active" || projectServiceCountText(nil) != "-" {
		t.Fatalf("project list output = %q", out.String())
	}

	issues := formatProjectValidationIssues([]*agentcomposev2.ProjectValidationIssue{
		{Path: "agents[0].image", Message: "required"},
		{Message: "top-level error"},
	})
	if issues != "agents[0].image: required; top-level error" {
		t.Fatalf("formatProjectValidationIssues = %q", issues)
	}
	run := &agentcomposev2.RunSummary{CreatedAt: "created", UpdatedAt: "updated", StartedAt: "started", CompletedAt: "completed"}
	if runSortTime(run) != "updated" {
		t.Fatalf("runSortTime = %q", runSortTime(run))
	}
	if execResultExitCode(&agentcomposev2.ExecResult{ExitCode: 7}) != 7 || execResultExitCode(&agentcomposev2.ExecResult{ExitCode: 126}) != exitCodeGeneral {
		t.Fatalf("execResultExitCode mismatch")
	}
	platform, err := parseImagePlatform("linux/amd64/v3")
	if err != nil || platform.GetOs() != "linux" || platform.GetArchitecture() != "amd64" || platform.GetVariant() != "v3" {
		t.Fatalf("parseImagePlatform platform=%#v err=%v", platform, err)
	}
	if platform, err := parseImagePlatform(" "); err != nil || platform != nil {
		t.Fatalf("empty parseImagePlatform platform=%#v err=%v", platform, err)
	}
	if _, err := parseImagePlatform("linux"); err == nil {
		t.Fatalf("expected invalid platform error")
	}
	if imageStoreText(agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON) != "docker" ||
		imageStoreText(agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE) != "oci-cache" ||
		imageAvailabilityStatusText(agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_ERROR) != "error" ||
		imageOperationStatusText(agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED) != "succeeded" {
		t.Fatalf("image text helpers returned unexpected values")
	}

	projectSummary := &agentcomposev2.ProjectSummary{
		ProjectId:       "project-1",
		Name:            "Project",
		SourcePath:      "/tmp/project/agent-compose.yml",
		CurrentRevision: 4,
		SpecHash:        "hash-2",
		AgentCount:      2,
		SchedulerCount:  1,
	}
	changes := []*agentcomposev2.ProjectChange{
		{Action: agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED, ResourceType: "agent", ResourceId: "agent-1", Name: "worker"},
		{Action: agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED, ResourceType: "project_scheduler", ResourceId: "scheduler-1", Name: "worker"},
		{Action: agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED, ResourceType: "loader", ResourceId: "loader-1", Name: "worker scheduler"},
		{Action: agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_REMOVED, ResourceType: "session", ResourceId: "session-1", Name: "old"},
		{Action: agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED, ResourceType: "session", ResourceId: "session-2", Message: "stop failed"},
	}
	displaySpec := &compose.NormalizedProjectSpec{Agents: []compose.NormalizedAgentSpec{{
		Name: "worker",
		Scheduler: &compose.NormalizedSchedulerSpec{Triggers: []compose.NormalizedTriggerSpec{{
			Name: "timer",
		}}},
	}}}
	upResp := &agentcomposev2.ApplyProjectResponse{
		Project: &agentcomposev2.Project{Summary: projectSummary},
		Revision: &agentcomposev2.ProjectRevision{
			Revision: 4,
			SpecHash: "hash-2",
		},
		Applied: true,
		Changes: changes,
	}
	upOutput := composeUpOutputFromResponse(upResp)
	if !upOutput.Applied || upOutput.Project.ID != "project-1" || len(upOutput.Changes) != len(changes) {
		t.Fatalf("composeUpOutputFromResponse = %#v", upOutput)
	}
	out.Reset()
	if err := writeComposeUpText(&out, composeDisplayChangesFromProjectChanges(upResp.GetChanges(), displaySpec)); err != nil {
		t.Fatalf("writeComposeUpText returned error: %v", err)
	}
	if !strings.Contains(out.String(), "ACTION") || !strings.Contains(out.String(), "timer") || strings.Contains(out.String(), "loader") {
		t.Fatalf("compose up text = %q", out.String())
	}
	out.Reset()
	upResp.Applied = false
	if err := writeComposeUpText(&out, composeDisplayChangesFromProjectChanges(upResp.GetChanges(), displaySpec)); err != nil || !strings.Contains(out.String(), "ACTION") {
		t.Fatalf("compose up not-applied text = %q err=%v", out.String(), err)
	}
	out.Reset()
	upResp.Unchanged = true
	if err := writeComposeUpText(&out, composeDisplayChangesFromProjectChanges(upResp.GetChanges(), displaySpec)); err != nil || !strings.Contains(out.String(), "ACTION") {
		t.Fatalf("compose up unchanged text = %q err=%v", out.String(), err)
	}

	downResp := &agentcomposev2.RemoveProjectResponse{
		Project: &agentcomposev2.Project{Summary: projectSummary},
		Changes: changes,
	}
	downOutput := composeDownOutputFromResponse(downResp)
	if downOutput.Status != "partial-failure" || downOutput.FailedSessionStops != 1 || len(composeChangeOutputs(changes)) != len(changes) {
		t.Fatalf("composeDownOutputFromResponse = %#v", downOutput)
	}
	out.Reset()
	if err := writeComposeDownText(&out, composeDownDisplayChanges(downResp, displaySpec)); err != nil {
		t.Fatalf("writeComposeDownText returned error: %v", err)
	}
	if !strings.Contains(out.String(), "MESSAGE") || !strings.Contains(out.String(), "timer") || !strings.Contains(out.String(), "stop failed") || strings.Contains(out.String(), "loader") {
		t.Fatalf("compose down text = %q", out.String())
	}
	sharedTriggerSpec := &compose.NormalizedProjectSpec{Agents: []compose.NormalizedAgentSpec{
		{
			Name:      "worker-a",
			Scheduler: &compose.NormalizedSchedulerSpec{Triggers: []compose.NormalizedTriggerSpec{{Name: "hourly"}}},
		},
		{
			Name:      "worker-b",
			Scheduler: &compose.NormalizedSchedulerSpec{Triggers: []compose.NormalizedTriggerSpec{{Name: "hourly"}}},
		},
	}}
	sharedTriggerChanges := composeDisplayChangesFromProjectChanges([]*agentcomposev2.ProjectChange{
		{Action: agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED, ResourceType: "project_scheduler", ResourceId: "scheduler-a", Name: "worker-a"},
		{Action: agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED, ResourceType: "project_scheduler", ResourceId: "scheduler-b", Name: "worker-b"},
	}, sharedTriggerSpec)
	sharedTriggerCount := 0
	for _, change := range sharedTriggerChanges {
		if change.ResourceType == "trigger" && change.Name == "hourly" {
			sharedTriggerCount++
		}
	}
	if sharedTriggerCount != 2 {
		t.Fatalf("shared trigger display changes = %#v, want 2 hourly triggers", sharedTriggerChanges)
	}
	unchangedDown := composeDownOutputFromResponse(&agentcomposev2.RemoveProjectResponse{Project: &agentcomposev2.Project{Summary: projectSummary}})
	if unchangedDown.Status != "unchanged" {
		t.Fatalf("unchanged down output = %#v", unchangedDown)
	}
	if projectChangeActionText(agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNSPECIFIED) != "unspecified" ||
		countProjectDownFailedSessionStops(changes) != 1 {
		t.Fatalf("project change helpers returned unexpected values")
	}
	_ = domain.SessionSummary{ID: "compile-check"}
}

func testComposeProjectOutputHelpers(t *testing.T) {
	t.Helper()
	project := &agentcomposev2.Project{
		Summary: &agentcomposev2.ProjectSummary{
			ProjectId:       "project-1",
			Name:            "Project",
			SourcePath:      "/repo/agent-compose.yml",
			CurrentRevision: 5,
			SpecHash:        "hash",
			AgentCount:      2,
			SchedulerCount:  1,
			RunningRunCount: 1,
			LatestRunId:     "run-latest",
			CreatedAt:       "created",
			UpdatedAt:       "updated",
			RemovedAt:       "",
		},
		Agents: []*agentcomposev2.ProjectAgent{
			{AgentName: "reviewer", ManagedAgentId: "agent-1", Provider: "codex", Model: "gpt", Image: "guest:latest", Driver: "docker", SchedulerEnabled: true},
			{AgentName: "runner", ManagedAgentId: "agent-2"},
		},
		Schedulers: []*agentcomposev2.ProjectScheduler{
			{AgentName: "reviewer", SchedulerId: "scheduler-1", ManagedLoaderId: "loader-1", Enabled: true, TriggerCount: 2},
		},
	}
	output := composeProjectOutputFromProject(project)
	if output.Project.ID != "project-1" || len(output.Agents) != 2 || len(output.Schedulers) != 1 || !output.Agents[0].SchedulerEnabled {
		t.Fatalf("composeProjectOutputFromProject = %#v", output)
	}
	if summary := composeProjectSummaryOutput(project.GetSummary()); summary.Name != "Project" || summary.CurrentRevision != 5 {
		t.Fatalf("composeProjectSummaryOutput = %#v", summary)
	}
	if agent := composeProjectAgentOutputFromProto(project.GetAgents()[0]); agent.Provider != "codex" || agent.Driver != "docker" {
		t.Fatalf("composeProjectAgentOutputFromProto = %#v", agent)
	}
	if scheduler := composeProjectSchedulerOutputFromProto(project.GetSchedulers()[0]); scheduler.ManagedLoaderID != "loader-1" || scheduler.TriggerCount != 2 {
		t.Fatalf("composeProjectSchedulerOutputFromProto = %#v", scheduler)
	}

	filter, err := composePSStatusFilter(composePSOptions{Status: " running,failed ,, "})
	if err != nil || !filter["running"] || !filter["failed"] {
		t.Fatalf("composePSStatusFilter filter=%#v err=%v", filter, err)
	}
	filter, err = composePSStatusFilter(composePSOptions{All: true})
	if err != nil || filter != nil {
		t.Fatalf("composePSStatusFilter all filter=%#v err=%v", filter, err)
	}
	filter, err = composePSStatusFilter(composePSOptions{})
	if err != nil || len(filter) != 1 || !filter["running"] {
		t.Fatalf("composePSStatusFilter default filter=%#v err=%v", filter, err)
	}

	newerRun := &agentcomposev2.RunSummary{RunId: "run-new", ProjectId: "project-1", SessionId: "session-1", UpdatedAt: "2026-07-02T00:00:00Z"}
	olderRun := &agentcomposev2.RunSummary{RunId: "run-old", ProjectId: "project-1", SessionId: "session-1", CreatedAt: "2026-07-01T00:00:00Z"}
	bySession := latestRunsBySession([]*agentcomposev2.RunSummary{
		olderRun,
		{RunId: "missing-session"},
		newerRun,
	})
	if bySession["session-1"].GetRunId() != "run-new" {
		t.Fatalf("latestRunsBySession = %#v", bySession)
	}
	session := &agentcomposev1.SessionSummary{
		SessionId:     "session-1",
		TriggerSource: "manual project-1 run",
		Tags: []*agentcomposev1.SessionTag{
			{Name: " project_id ", Value: " project-1 "},
			{Name: "", Value: "ignored"},
		},
	}
	if !composePSSessionBelongsToProject(session, project, bySession) {
		t.Fatalf("expected session to belong to project")
	}
	if tags := sessionTagsMap(session.GetTags()); tags["project_id"] != "project-1" {
		t.Fatalf("sessionTagsMap = %#v", tags)
	}
	if composePSSessionBelongsToProject(
		&agentcomposev1.SessionSummary{SessionId: "session-by-name", Tags: []*agentcomposev1.SessionTag{{Name: "project", Value: "Project"}}},
		project,
		map[string]*agentcomposev2.RunSummary{},
	) != true {
		t.Fatalf("expected project-name tag to match project")
	}
	if composePSSessionBelongsToProject(
		&agentcomposev1.SessionSummary{SessionId: "session-by-source", TriggerSource: "started from /repo/agent-compose.yml"},
		project,
		map[string]*agentcomposev2.RunSummary{},
	) != false {
		t.Fatalf("source-path-only trigger source should not match project")
	}
	if composePSSessionBelongsToProject(
		&agentcomposev1.SessionSummary{SessionId: "session-no-match", Tags: []*agentcomposev1.SessionTag{{Name: "project_id", Value: "other"}}},
		project,
		map[string]*agentcomposev2.RunSummary{},
	) {
		t.Fatalf("unexpected session/project match")
	}

	var out bytes.Buffer
	psOutput := composePSOutput{Project: output.Project, Sandboxes: []composePSSandboxOutput{{
		ID: "session-1", ShortID: "session-1", Agent: "reviewer", Status: "running", RunID: "run-new", RunShortID: "run-new", CreatedAt: "created", UpdatedAt: "updated", Driver: "docker", Image: "guest", Workspace: "/repo",
	}}}
	if err := writePSText(&out, psOutput, true); err != nil {
		t.Fatalf("writePSText verbose returned error: %v", err)
	}
	if !strings.Contains(out.String(), "WORKSPACE") || !strings.Contains(out.String(), "session-1") {
		t.Fatalf("verbose ps output = %q", out.String())
	}
	out.Reset()
	if err := writePSText(&out, psOutput, false); err != nil {
		t.Fatalf("writePSText returned error: %v", err)
	}
	if strings.Contains(out.String(), "WORKSPACE") || !strings.Contains(out.String(), "reviewer") {
		t.Fatalf("ps output = %q", out.String())
	}
}

func testComposeRunLogAndExecHelpers(t *testing.T) {
	t.Helper()
	summary := &agentcomposev2.RunSummary{
		RunId:       "run-1",
		ProjectId:   "project-1",
		ProjectName: "Project",
		AgentName:   "reviewer",
		Source:      agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER,
		Status:      agentcomposev2.RunStatus_RUN_STATUS_FAILED,
		SessionId:   "session-1",
		ExitCode:    7,
		Error:       "failed",
		StartedAt:   "started",
		UpdatedAt:   "updated",
		CompletedAt: "completed",
		DurationMs:  12,
		Warnings:    []string{"first", "duplicate"},
	}
	detail := &agentcomposev2.RunDetail{
		Summary:      summary,
		Prompt:       "prompt",
		Output:       "line1\nline2\nline3\n",
		ResultJson:   `{"ok":false}`,
		LogsPath:     "/tmp/logs",
		ArtifactsDir: "/tmp/artifacts",
		CleanupError: "cleanup",
		Driver:       "docker",
		ImageRef:     "guest",
		Warnings:     []string{"duplicate", "second"},
	}
	output := composeRunOutputFromDetailWithOptions(detail, composeLogsOptions{TailLines: 2})
	if output.Status != "failed" || output.Source != "scheduler" || output.Output != "line2\nline3\n" || len(output.Warnings) != 3 {
		t.Fatalf("composeRunOutputFromDetailWithOptions = %#v", output)
	}
	if summaryOutput := composeRunOutputFromSummary(summary, "Fallback", "agent-compose logs"); summaryOutput.ProjectName != "Project" || summaryOutput.LogsCommand == "" {
		t.Fatalf("composeRunOutputFromSummary = %#v", summaryOutput)
	}
	if !runSummaryFailed(summary) || !runSummaryTerminal(summary) || runSummaryExitCode(summary) != 7 {
		t.Fatalf("run summary helpers returned unexpected values")
	}
	pending := &agentcomposev2.RunSummary{Status: agentcomposev2.RunStatus_RUN_STATUS_PENDING, ExitCode: 126}
	if runSummaryFailed(pending) || runSummaryTerminal(pending) || runSummaryExitCode(pending) != exitCodeGeneral {
		t.Fatalf("pending run summary helpers returned unexpected values")
	}
	if runStatusText(agentcomposev2.RunStatus_RUN_STATUS_RUNNING) != "running" ||
		runStatusText(agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED) != "succeeded" ||
		runStatusText(agentcomposev2.RunStatus_RUN_STATUS_CANCELED) != "canceled" ||
		runStatusText(agentcomposev2.RunStatus_RUN_STATUS_UNSPECIFIED) != "unspecified" ||
		runSourceText(agentcomposev2.RunSource_RUN_SOURCE_API) != "api" ||
		runSourceText(agentcomposev2.RunSource_RUN_SOURCE_MANUAL) != "manual" ||
		runSourceText(agentcomposev2.RunSource_RUN_SOURCE_UNSPECIFIED) != "unspecified" {
		t.Fatalf("run status/source helpers returned unexpected values")
	}
	if got := tailLogOutput("a\nb\nc", 2); got != "b\nc" {
		t.Fatalf("tailLogOutput = %q", got)
	}
	if got := runLogTimestamp(summary); got != "completed" {
		t.Fatalf("runLogTimestamp = %q", got)
	}

	var out bytes.Buffer
	if err := writeLogDetails(&out, []*agentcomposev2.RunDetail{detail}, map[string]int{}, composeLogsOptions{TailLines: 1, Timestamp: true}); err != nil {
		t.Fatalf("writeLogDetails returned error: %v", err)
	}
	if !strings.Contains(out.String(), "reviewer-run-1 [completed]| line3") {
		t.Fatalf("log details = %q", out.String())
	}
	out.Reset()
	if err := writeLogsForRun(&out, detail, true, composeLogsOptions{TailLines: 1}); err != nil {
		t.Fatalf("writeLogsForRun json returned error: %v", err)
	}
	if !strings.Contains(out.String(), `"run_id": "run-1"`) || !strings.Contains(out.String(), "line3") {
		t.Fatalf("json logs = %q", out.String())
	}
	out.Reset()
	if err := writePrefixedRunOutput(&out, &agentcomposev2.RunSummary{RunId: "fallback"}, "last-line", false); err != nil {
		t.Fatalf("writePrefixedRunOutput returned error: %v", err)
	}
	if !strings.Contains(out.String(), "fallback | last-line\n") {
		t.Fatalf("prefixed output = %q", out.String())
	}

	execOutput := composeExecOutputFromResult(&agentcomposev2.ExecResult{
		ExecId:    "exec-1",
		SessionId: "session-1",
		RunId:     "run-1",
		Command:   &agentcomposev2.ExecCommand{Command: "bash", Args: []string{"-lc", "echo ok"}},
		Cwd:       "/repo",
		ExitCode:  9,
		Success:   false,
		Stdout:    "out",
		Stderr:    "err",
		Output:    "outerr",
		Error:     "boom",
	})
	if execOutput.Command != "bash" || len(execOutput.Args) != 2 || execOutput.ExitCode != 9 || execOutput.Output != "outerr" {
		t.Fatalf("composeExecOutputFromResult = %#v", execOutput)
	}
	if got := detachedRunLogsCommand(cliOptions{Host: "unix:///tmp/socket path", ComposeFile: "compose file.yml", ProjectName: "project"}, "run '1'"); !strings.Contains(got, "'unix:///tmp/socket path'") || !strings.Contains(got, "'run '\"'\"'1'\"'\"''") {
		t.Fatalf("detachedRunLogsCommand = %q", got)
	}
	if got := shellQuoteCLIArg(""); got != "''" {
		t.Fatalf("shellQuoteCLIArg empty = %q", got)
	}
	if got := appendUniqueStrings([]string{" first ", "second"}, "second", "", "third"); len(got) != 3 || got[0] != "first" || got[2] != "third" {
		t.Fatalf("appendUniqueStrings = %#v", got)
	}
}

func testComposeRunExecAndLogsEdgeHelpers(t *testing.T) {
	t.Helper()
	project := &compose.NormalizedProjectSpec{
		Name: "Project",
		Agents: []compose.NormalizedAgentSpec{{
			Name:     "reviewer",
			Provider: "gemini",
			Scheduler: &compose.NormalizedSchedulerSpec{Triggers: []compose.NormalizedTriggerSpec{
				{Name: "nightly"},
			}},
		}},
	}
	if _, ok := composeRunAgentSpec(nil, "reviewer"); ok {
		t.Fatalf("nil project unexpectedly matched agent")
	}
	if agent, ok := composeRunAgentSpec(project, " reviewer "); !ok || agent.Name != "reviewer" {
		t.Fatalf("composeRunAgentSpec agent=%#v ok=%v", agent, ok)
	}
	if normalizeOptionalRunModeValue(optionalRunModeFlagNoValue) != "" ||
		normalizeOptionalRunModeValue(" prompt ") != "prompt" {
		t.Fatalf("normalizeOptionalRunModeValue returned unexpected values")
	}
	if normalizeInteractivePromptProvider("claude_code") != "claude" ||
		normalizeInteractivePromptProvider("open-code") != "opencode" ||
		normalizeInteractivePromptProvider(" Gemini ") != "gemini" {
		t.Fatalf("normalizeInteractivePromptProvider returned unexpected values")
	}
	if err := validateInteractivePromptProvider(project, "reviewer"); commandExitCode(err) != exitCodeUnsupported {
		t.Fatalf("validateInteractivePromptProvider err=%v code=%d", err, commandExitCode(err))
	}
	project.Agents[0].Provider = "claude-code"
	if err := validateInteractivePromptProvider(project, "reviewer"); err != nil {
		t.Fatalf("validateInteractivePromptProvider claude-code returned error: %v", err)
	}

	failed := &agentcomposev2.RunSummary{RunId: "run-failed", Status: agentcomposev2.RunStatus_RUN_STATUS_FAILED, ExitCode: 9, Error: "boom"}
	failedDetail := &agentcomposev2.RunDetail{Summary: failed, CleanupError: "cleanup boom"}
	if err := composeRunCompletionError("Project", "reviewer", failed, failedDetail); commandExitCode(err) != 9 || !strings.Contains(err.Error(), "cleanup warning") {
		t.Fatalf("failed completion err=%v code=%d", err, commandExitCode(err))
	}
	succeeded := &agentcomposev2.RunSummary{RunId: "run-ok", Status: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED}
	if err := composeRunCompletionError("Project", "reviewer", succeeded, &agentcomposev2.RunDetail{Summary: succeeded, CleanupError: " cleanup failed "}); commandExitCode(err) != exitCodeGeneral {
		t.Fatalf("cleanup completion err=%v code=%d", err, commandExitCode(err))
	}
	if err := composeRunCompletionError("Project", "reviewer", succeeded, &agentcomposev2.RunDetail{Summary: succeeded}); err != nil {
		t.Fatalf("successful completion returned error: %v", err)
	}
	if runDetailCleanupError(nil) != "" {
		t.Fatalf("nil run detail cleanup error should be empty")
	}
	var out bytes.Buffer
	if err := writeRunWarnings(&out, []string{" first ", "first", "", "second"}); err != nil {
		t.Fatalf("writeRunWarnings returned error: %v", err)
	}
	if out.String() != "warning: first\nwarning: second\n" {
		t.Fatalf("warnings output = %q", out.String())
	}
	out.Reset()
	if err := writeDetachedRunText(&out, &agentcomposev2.RunSummary{Status: agentcomposev2.RunStatus_RUN_STATUS_PENDING}, "agent-compose logs"); err != nil {
		t.Fatalf("writeDetachedRunText returned error: %v", err)
	}
	if !strings.Contains(out.String(), "Run: -") || !strings.Contains(out.String(), "Status: pending") {
		t.Fatalf("detached output = %q", out.String())
	}

	logsCmd := &cobra.Command{Use: "logs"}
	logsCmd.Flags().String("agent", "", "")
	if err := logsCmd.Flags().Set("agent", "reviewer"); err != nil {
		t.Fatalf("set agent flag: %v", err)
	}
	if _, err := normalizeComposeLogsOptions(logsCmd, composeLogsOptions{}, []string{"writer"}); commandExitCode(err) != exitCodeUsage {
		t.Fatalf("logs agent conflict err=%v code=%d", err, commandExitCode(err))
	}
	logsCmd = &cobra.Command{Use: "logs"}
	logsCmd.Flags().String("sandbox", "", "")
	if err := logsCmd.Flags().Set("sandbox", "sandbox-logs"); err != nil {
		t.Fatalf("set sandbox flag: %v", err)
	}
	out.Reset()
	logsCmd.SetErr(&out)
	logOptions, err := normalizeComposeLogsOptions(logsCmd, composeLogsOptions{SandboxID: "sandbox-logs", TailLines: -1}, nil)
	if err != nil || logOptions.SandboxID != "sandbox-logs" || logOptions.SessionID != "" || out.String() != "" {
		t.Fatalf("normalizeComposeLogsOptions options=%#v err=%v stderr=%q", logOptions, err, out.String())
	}
	if _, err := normalizeComposeLogsOptions(&cobra.Command{Use: "logs"}, composeLogsOptions{TailLines: -2}, nil); commandExitCode(err) != exitCodeUsage {
		t.Fatalf("logs invalid tail err=%v code=%d", err, commandExitCode(err))
	}

	execCmd := &cobra.Command{Use: "exec"}
	if err := composeExecArgs(execCmd, nil); err == nil {
		t.Fatalf("composeExecArgs without target returned nil error")
	}
	execCmd.Flags().String("run-id", "", "")
	if err := execCmd.Flags().Set("run-id", "run-1"); err != nil {
		t.Fatalf("set exec run-id: %v", err)
	}
	if err := composeExecArgs(execCmd, nil); err != nil {
		t.Fatalf("composeExecArgs with legacy target returned error: %v", err)
	}

	defaultCommand, err := composeExecCommandFromArgs(composeExecOptions{}, nil)
	if err != nil || defaultCommand.GetCommand() != "sh" {
		t.Fatalf("default exec command=%#v err=%v", defaultCommand, err)
	}
	if _, err := composeExecCommandFromArgs(composeExecOptions{Command: "echo ok"}, []string{"pwd"}); commandExitCode(err) != exitCodeUsage {
		t.Fatalf("exec command conflict err=%v code=%d", err, commandExitCode(err))
	}
	normalizedExecProject := &compose.NormalizedProjectSpec{Name: "Project"}
	execSandboxID := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	req, err := normalizeComposeExecRequest(&cobra.Command{Use: "exec"}, cliServiceClients{}, normalizedExecProject, "project-1", composeExecOptions{Cwd: " /repo "}, []string{" " + execSandboxID + " ", "bash", "-lc", "pwd"})
	if err != nil || req.GetSessionId() != execSandboxID || req.GetCwd() != "/repo" || req.GetCommand().GetCommand() != "bash" {
		t.Fatalf("normalizeComposeExecRequest req=%#v err=%v", req, err)
	}
	for _, tc := range []struct {
		name    string
		setup   func(*cobra.Command) composeExecOptions
		args    []string
		wantErr string
	}{
		{
			name: "multiple legacy targets",
			setup: func(cmd *cobra.Command) composeExecOptions {
				cmd.Flags().String("agent", "", "")
				cmd.Flags().String("run-id", "", "")
				_ = cmd.Flags().Set("agent", "reviewer")
				_ = cmd.Flags().Set("run-id", "run-1")
				return composeExecOptions{AgentName: "reviewer", RunID: "run-1"}
			},
			wantErr: "target can only be specified once",
		},
		{
			name: "empty run flag",
			setup: func(cmd *cobra.Command) composeExecOptions {
				cmd.Flags().String("run-id", "", "")
				_ = cmd.Flags().Set("run-id", " ")
				return composeExecOptions{RunID: " "}
			},
			wantErr: "requires a value",
		},
		{
			name: "empty sandbox positional",
			setup: func(cmd *cobra.Command) composeExecOptions {
				return composeExecOptions{}
			},
			args:    []string{" "},
			wantErr: "requires non-empty sandbox",
		},
	} {
		cmd := &cobra.Command{Use: "exec"}
		options := tc.setup(cmd)
		if _, err := normalizeComposeExecRequest(cmd, cliServiceClients{}, normalizedExecProject, "project-1", options, tc.args); commandExitCode(err) != exitCodeUsage || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("%s err=%v code=%d", tc.name, err, commandExitCode(err))
		}
	}
}

func testComposeImageStatsAndSessionHelpers(t *testing.T) {
	t.Helper()
	value := 42.5
	metric := func(unit string, status agentcomposev2.MetricStatus) *agentcomposev2.MetricValue {
		return &agentcomposev2.MetricValue{Value: &value, Unit: unit, Status: status, Message: "metric-message"}
	}
	stats := composeStatsOutputFromProto(&agentcomposev2.SandboxStats{
		SandboxId:        "session-1",
		Driver:           "docker",
		SampledAt:        "sampled",
		CpuPercent:       metric("percent", agentcomposev2.MetricStatus_METRIC_STATUS_OK),
		MemoryUsageBytes: metric("bytes", agentcomposev2.MetricStatus_METRIC_STATUS_OK),
		MemoryLimitBytes: metric("bytes", agentcomposev2.MetricStatus_METRIC_STATUS_UNAVAILABLE),
		MemoryPercent:    metric("percent", agentcomposev2.MetricStatus_METRIC_STATUS_UNKNOWN),
		NetworkRxBytes:   metric("bytes", agentcomposev2.MetricStatus_METRIC_STATUS_OK),
		NetworkTxBytes:   metric("bytes", agentcomposev2.MetricStatus_METRIC_STATUS_OK),
		BlockReadBytes:   metric("bytes", agentcomposev2.MetricStatus_METRIC_STATUS_OK),
		BlockWriteBytes:  metric("bytes", agentcomposev2.MetricStatus_METRIC_STATUS_OK),
		UptimeSeconds:    metric("seconds", agentcomposev2.MetricStatus_METRIC_STATUS_OK),
	})
	if stats.ID != "session-1" || stats.CPUPercent.Status != "ok" || stats.MemoryLimitBytes.Status != "unavailable" || stats.MemoryPercent.Status != "unknown" {
		t.Fatalf("composeStatsOutputFromProto = %#v", stats)
	}
	if nilStats := composeStatsOutputFromProto(nil); nilStats.ID != "" {
		t.Fatalf("nil stats = %#v", nilStats)
	}
	if nilMetric := composeMetricOutputFromProto(nil); nilMetric.Status != "unknown" {
		t.Fatalf("nil metric = %#v", nilMetric)
	}
	var out bytes.Buffer
	if err := writeStatsText(&out, []composeStatsOutput{stats}); err != nil {
		t.Fatalf("writeStatsText returned error: %v", err)
	}
	if !strings.Contains(out.String(), "42.50") || !strings.Contains(out.String(), "42s") || !strings.Contains(out.String(), "-") {
		t.Fatalf("stats text = %q", out.String())
	}
	if formatMetricForText(composeMetricOutput{Status: "ok", Unit: "bytes", Value: &value}) != "42" ||
		formatMetricForText(composeMetricOutput{Status: "unavailable", Unit: "bytes", Value: &value}) != "-" ||
		formatMetricForText(composeMetricOutput{Status: "ok", Unit: "percent", Value: &value}) != "42.50" ||
		formatMetricForText(composeMetricOutput{Status: "ok", Unit: "seconds", Value: &value}) != "42s" ||
		formatMetricForText(composeMetricOutput{Status: "ok"}) != "-" {
		t.Fatalf("formatMetricForText returned unexpected values")
	}

	image := &agentcomposev2.Image{
		ImageId:            "sha256:1234567890abcdef",
		ImageRef:           "guest:latest",
		ResolvedRef:        "guest@sha256:abc",
		RepoTags:           []string{"guest:latest"},
		RepoDigests:        []string{"guest@sha256:abc"},
		Store:              agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE,
		AvailabilityStatus: agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_AVAILABLE,
		Platform:           &agentcomposev2.ImagePlatform{Os: "linux", Architecture: "amd64"},
		SizeBytes:          123,
		VirtualSizeBytes:   456,
		CreatedAt:          "created",
		InspectedAt:        "inspected",
		Dangling:           true,
		ContainerCount:     2,
		Labels:             map[string]string{"a": "b"},
	}
	store := &agentcomposev2.ImageStoreStatus{Store: agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON, Available: true, Endpoint: "unix:///var/run/docker.sock"}
	imageList := composeImageListOutputFromResponse(&agentcomposev2.ListImagesResponse{
		Images:      []*agentcomposev2.Image{image},
		TotalCount:  1,
		HasMore:     true,
		NextOffset:  10,
		StoreStatus: store,
	})
	if len(imageList.Images) != 1 || imageList.StoreStatus.Store != "docker" || !imageList.HasMore || imageList.Images[0].Platform != "linux/amd64" {
		t.Fatalf("composeImageListOutputFromResponse = %#v", imageList)
	}
	pull := composeImagePullOutputFromResponse(&agentcomposev2.PullImageResponse{
		Image:       image,
		Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_FAILED,
		ResolvedRef: "guest@sha256:abc",
		Progress: []*agentcomposev2.ImagePullProgress{
			{Id: "layer-1", Status: "downloading", Progress: "50%", CurrentBytes: 5, TotalBytes: 10},
		},
		Warnings: []string{"warn"},
	})
	if pull.Status != "failed" || len(pull.Progress) != 1 || pull.ImageRef != "guest:latest" {
		t.Fatalf("composeImagePullOutputFromResponse = %#v", pull)
	}
	inspect := composeImageInspectOutputFromResponse(&agentcomposev2.InspectImageResponse{Image: image, StoreStatus: store})
	remove := composeImageRemoveOutputFromResponse(&agentcomposev2.RemoveImageResponse{ImageRef: "guest:latest", UntaggedRefs: []string{"guest:latest"}, DeletedIds: []string{"sha256:123"}, Warnings: []string{"warn"}})
	if inspect.Image.ImageID == "" || remove.DeletedIDs[0] != "123" {
		t.Fatalf("inspect=%#v remove=%#v", inspect, remove)
	}
	cacheItem := &agentcomposev2.CacheItem{
		CacheId:        "cache-1",
		Domain:         agentcomposev2.CacheDomain_CACHE_DOMAIN_SESSION_EPHEMERAL_STATE,
		Driver:         "docker",
		Kind:           "sandbox-dir",
		Path:           "/tmp/session",
		SizeBytes:      789,
		ImageId:        "sha256:image",
		ImageRef:       "guest:latest",
		ResolvedRef:    "guest@sha256:abc",
		SessionId:      "session-1",
		SandboxId:      "sandbox-1",
		Status:         agentcomposev2.CacheStatus_CACHE_STATUS_REFERENCED,
		Removable:      false,
		BlockedReasons: []string{"cache is referenced"},
		LastUsedAt:     "used",
		LastUsedSource: "metadata",
		References: []*agentcomposev2.CacheReference{{
			Type:        "session",
			Id:          "session-1",
			Name:        "Session",
			Path:        "/tmp/session",
			Status:      "running",
			Description: "active sandbox",
		}},
		Warnings: []string{"cache warn"},
	}
	cacheList := composeCacheListOutputFromResponse(&agentcomposev2.ListCachesResponse{Caches: []*agentcomposev2.CacheItem{cacheItem}, Warnings: []string{"list warn"}})
	if len(cacheList.Caches) != 1 || cacheList.Caches[0].Domain != "sandbox-ephemeral-state" || cacheList.Caches[0].Type != "sandbox" || len(cacheList.Caches[0].References) != 1 {
		t.Fatalf("composeCacheListOutputFromResponse = %#v", cacheList)
	}
	cacheInspect := composeCacheInspectOutputFromResponse(&agentcomposev2.InspectCacheResponse{Cache: cacheItem, Warnings: []string{"inspect warn"}})
	if cacheInspect.Cache.ID != "cache-1" || cacheInspect.Cache.Status != "referenced" {
		t.Fatalf("composeCacheInspectOutputFromResponse = %#v", cacheInspect)
	}
	pruneOutput := composeCacheOperationOutputFromPruneResponse(&agentcomposev2.PruneCachesResponse{
		DryRun:   true,
		Matched:  []*agentcomposev2.CacheItem{cacheItem},
		Skipped:  []*agentcomposev2.CacheItem{cacheItem},
		Warnings: []string{"prune warn"},
	})
	removeOutput := composeCacheOperationOutputFromRemoveResponse(&agentcomposev2.RemoveCacheResponse{
		Matched: []*agentcomposev2.CacheItem{cacheItem},
		Removed: []string{
			"cache-1",
		},
		Warnings: []string{"remove warn"},
	})
	if !pruneOutput.DryRun || len(pruneOutput.Matched) != 1 || len(removeOutput.Removed) != 1 {
		t.Fatalf("cache operation outputs prune=%#v remove=%#v", pruneOutput, removeOutput)
	}
	out.Reset()
	if err := writeCacheListText(&out, cacheList); err != nil {
		t.Fatalf("writeCacheListText returned error: %v", err)
	}
	if !strings.Contains(out.String(), "cache-1") || !strings.Contains(out.String(), "list warn") {
		t.Fatalf("cache list text = %q", out.String())
	}
	out.Reset()
	if err := writeCacheInspectText(&out, cacheInspect); err != nil {
		t.Fatalf("writeCacheInspectText returned error: %v", err)
	}
	if !strings.Contains(out.String(), "active sandbox") || !strings.Contains(out.String(), "inspect warn") || !strings.Contains(out.String(), "cache warn") {
		t.Fatalf("cache inspect text = %q", out.String())
	}
	out.Reset()
	if err := writeCacheOperationOutput(&out, false, pruneOutput); err != nil {
		t.Fatalf("writeCacheOperationOutput dry-run returned error: %v", err)
	}
	if !strings.Contains(out.String(), "Dry-run") || !strings.Contains(out.String(), "prune warn") {
		t.Fatalf("cache dry-run text = %q", out.String())
	}
	out.Reset()
	if err := writeCacheOperationOutput(&out, true, removeOutput); err != nil {
		t.Fatalf("writeCacheOperationOutput json returned error: %v", err)
	}
	if !strings.Contains(out.String(), `"removed"`) || !strings.Contains(out.String(), "cache-1") {
		t.Fatalf("cache remove json = %q", out.String())
	}
	if cacheRefSessionText(composeCacheOutput{ImageID: "image-only"}) != "image-only" ||
		cacheDomainText(agentcomposev2.CacheDomain_CACHE_DOMAIN_OCI_IMAGE_STORE) != "oci-image-store" ||
		cacheDomainText(agentcomposev2.CacheDomain_CACHE_DOMAIN_MATERIALIZED_IMAGE_CACHE) != "materialized-image-cache" ||
		cacheDomainText(agentcomposev2.CacheDomain_CACHE_DOMAIN_RUNTIME_DERIVED_CACHE) != "runtime-derived-cache" ||
		cacheTypeText(agentcomposev2.CacheDomain_CACHE_DOMAIN_OCI_IMAGE_STORE) != "oci" ||
		cacheTypeText(agentcomposev2.CacheDomain_CACHE_DOMAIN_MATERIALIZED_IMAGE_CACHE) != "materialized" ||
		cacheTypeText(agentcomposev2.CacheDomain_CACHE_DOMAIN_RUNTIME_DERIVED_CACHE) != "runtime" ||
		cacheStatusText(agentcomposev2.CacheStatus_CACHE_STATUS_ACTIVE) != "active" ||
		cacheStatusText(agentcomposev2.CacheStatus_CACHE_STATUS_UNUSED) != "unused" ||
		cacheStatusText(agentcomposev2.CacheStatus_CACHE_STATUS_EXPIRED) != "expired" ||
		cacheStatusText(agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED) != "orphaned" ||
		cacheStatusText(agentcomposev2.CacheStatus_CACHE_STATUS_UNKNOWN) != "unknown" {
		t.Fatalf("cache text helper returned unexpected values")
	}
	out.Reset()
	if err := writeImagesText(&out, imageList.Images, false); err != nil {
		t.Fatalf("writeImagesText returned error: %v", err)
	}
	if !strings.Contains(out.String(), "1234567890ab") || !strings.Contains(out.String(), "guest:latest") {
		t.Fatalf("images text = %q", out.String())
	}
	if imagePlatformText(&agentcomposev2.ImagePlatform{Os: "linux"}) != "linux" ||
		imagePlatformText(&agentcomposev2.ImagePlatform{Os: "linux", Architecture: "arm64", Variant: "v8"}) != "linux/arm64/v8" ||
		shortImageID("short") != "short" ||
		cloneStringMapForCLI(nil) != nil {
		t.Fatalf("image helper edge cases returned unexpected values")
	}
	if imageStoreText(agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_UNSPECIFIED) != "unspecified" ||
		imageAvailabilityStatusText(agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_MISSING) != "missing" ||
		imageAvailabilityStatusText(agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_ERROR) != "error" ||
		imageAvailabilityStatusText(agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_UNSPECIFIED) != "unspecified" ||
		imageOperationStatusText(agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED) != "succeeded" ||
		imageOperationStatusText(agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_UNSPECIFIED) != "unspecified" ||
		cacheRefSessionText(composeCacheOutput{SandboxID: "sandbox-only"}) != "sandbox-only" ||
		cacheRefSessionText(composeCacheOutput{ResolvedRef: "resolved-only"}) != "resolved-only" ||
		firstNonEmptyString("", " ", "fallback") != "fallback" ||
		runStatusText(agentcomposev2.RunStatus_RUN_STATUS_CANCELED) != "canceled" ||
		runStatusText(agentcomposev2.RunStatus_RUN_STATUS_UNSPECIFIED) != "unspecified" ||
		runSourceText(agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER) != "scheduler" ||
		runSourceText(agentcomposev2.RunSource_RUN_SOURCE_API) != "api" ||
		runSourceText(agentcomposev2.RunSource_RUN_SOURCE_UNSPECIFIED) != "unspecified" ||
		metricStatusText(agentcomposev2.MetricStatus_METRIC_STATUS_UNSPECIFIED) != "unknown" {
		t.Fatalf("text helper edge cases returned unexpected values")
	}
	if err := writeImagesText(failingWriter{}, imageList.Images, false); err == nil {
		t.Fatalf("writeImagesText failing writer returned nil error")
	}
	if err := writeCacheListText(failingWriter{}, cacheList); err == nil {
		t.Fatalf("writeCacheListText failing writer returned nil error")
	}
	if err := writeCacheInspectText(failingWriter{}, cacheInspect); err == nil {
		t.Fatalf("writeCacheInspectText failing writer returned nil error")
	}
	if err := writeStatsText(failingWriter{}, []composeStatsOutput{stats}); err == nil {
		t.Fatalf("writeStatsText failing writer returned nil error")
	}
	if err := writeStringListSection(failingWriter{}, "Warnings", []string{"warn"}); err == nil {
		t.Fatalf("writeStringListSection failing writer returned nil error")
	}
	if err := writeCacheReferencesSection(failingWriter{}, cacheInspect.Cache.References); err == nil {
		t.Fatalf("writeCacheReferencesSection failing writer returned nil error")
	}
	if err := writeCacheOperationTable(failingWriter{}, []composeCacheOutput{cacheInspect.Cache}); err == nil {
		t.Fatalf("writeCacheOperationTable failing writer returned nil error")
	}
	if err := writeSandboxPruneMatchedTable(failingWriter{}, []composePSSandboxOutput{{ID: "sandbox"}}, "matched"); err == nil {
		t.Fatalf("writeSandboxPruneMatchedTable failing writer returned nil error")
	}
	if err := writeSandboxPruneSkippedTable(failingWriter{}, []composeSandboxPruneSkipped{{Sandbox: "sandbox"}}); err == nil {
		t.Fatalf("writeSandboxPruneSkippedTable failing writer returned nil error")
	}

	session := composeSessionOutputFromSummary(&agentcomposev1.SessionSummary{
		SessionId:     "session-1",
		Title:         "title",
		Driver:        "docker",
		VmStatus:      " RUNNING ",
		WorkspacePath: "/repo",
		ProxyPath:     "/proxy",
		GuestImage:    "guest",
		TriggerSource: "manual",
		CreatedAt:     "created",
		UpdatedAt:     "updated",
		CellCount:     3,
		EventCount:    4,
		Tags: []*agentcomposev1.SessionTag{
			{Name: "agent", Value: "reviewer"},
			{Name: " ", Value: "ignored"},
		},
	})
	if session.VMStatus != "running" || session.Tags["agent"] != "reviewer" || session.EventCount != 4 {
		t.Fatalf("composeSessionOutputFromSummary = %#v", session)
	}
	if commandExitCode(nil) != 0 ||
		commandExitCode(commandExitError{Code: exitCodeUsage, Err: connect.NewError(connect.CodeInvalidArgument, nil)}) != exitCodeUsage ||
		commandExitCode(connect.NewError(connect.CodeInternal, nil)) != exitCodeGeneral {
		t.Fatalf("commandExitCode returned unexpected values")
	}
	if commandExitCode(commandExitErrorForConnect(connect.NewError(connect.CodeUnimplemented, nil))) != exitCodeUnsupported ||
		commandExitCode(commandExitErrorForConnect(connect.NewError(connect.CodeUnavailable, nil))) != exitCodeUnavailable ||
		commandExitCode(commandExitErrorForConnect(connect.NewError(connect.CodeNotFound, nil))) != exitCodeUsage {
		t.Fatalf("commandExitErrorForConnect returned unexpected values")
	}
}
